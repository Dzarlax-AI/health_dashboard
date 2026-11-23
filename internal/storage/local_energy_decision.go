package storage

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/jackc/pgx/v5"
)

type localEnergyCaptureKey struct{}

// LocalEnergyDecision is the solver input and result observed together when
// running the product calculation on a local profile copy. The capture time is
// independent of the target date. It makes no claim about older sent reports.
type LocalEnergyDecision struct {
	Inputs json.RawMessage `json:"inputs"`
	Result BankResult      `json:"result"`
}

func (s *DB) CaptureLocalEnergyDecision(ctx context.Context, tz, date string) (LocalEnergyDecision, error) {
	var packet LocalEnergyDecision
	if s.profileGraphSchema == "" {
		return packet, errors.New("energy capture requires an isolated local profile graph")
	}
	_, err := s.ComputeBankForDate(context.WithValue(ctx, localEnergyCaptureKey{}, &packet), tz, date)
	if err == nil && packet.Inputs == nil {
		err = errors.New("energy solver did not capture its inputs")
	}
	return packet, err
}

// VerifyLocalEnergyDecision feeds a previously captured packet to the installed
// dbt model. Insertions into its owned source tables are transaction-private
// and always rolled back. The fitted coefficients and selected days come
// entirely from the packet, even after the profile receives another sync.
func (s *DB) VerifyLocalEnergyDecision(ctx context.Context, energySchema string, packet LocalEnergyDecision) error {
	if s.profileGraphSchema == "" || energySchema == "" || !json.Valid(packet.Inputs) {
		return errors.New("expected local profile graph and energy packet")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadWrite})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var owned bool
	if err = tx.QueryRow(ctx, `select exists(select from pg_namespace where nspname=$1 and nspowner=current_user::regrole)
        and $2::jsonb->>'database'=current_database() and $2::jsonb->>'role'=current_user
        and $2::jsonb->>'source_schema'=current_schema()`, energySchema, packet.Inputs).Scan(&owned); err != nil {
		return err
	}
	if !owned {
		return errors.New("energy capture or replay schema belongs to another profile")
	}
	id := rand.Text()
	runs := pgx.Identifier{energySchema, "runs"}.Sanitize()
	days := pgx.Identifier{energySchema, "days"}.Sanitize()
	// The product reader runs the solver without writing a snapshot. Choose
	// retrospective_eod only for the source schema's snapshot_kind constraint.
	// Compare the calculated fields, which precede snapshot-specific flags.
	if _, err = tx.Exec(ctx, "insert into "+runs+` (profile,capture_id,target_day,snapshot_kind,formula_version,alpha,factor,stress_enabled,beta)
        select current_user,$1,target_day,'retrospective_eod',formula_version,alpha,factor,stress_enabled,beta
        from jsonb_to_record($2::jsonb->'run') as r(target_day date,formula_version int,alpha float8,factor float8,stress_enabled bool,beta float8)`, id, packet.Inputs); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "insert into "+days+` (profile,capture_id,day,sleep_total,deep,rem,awake,active_kcal,hr_load,stress_flags)
        select current_user,$1,day,sleep_total,deep,rem,awake,active_kcal,hr_load,stress_flags from
        jsonb_to_recordset($2::jsonb->'days') as d(day date,sleep_total float8,deep float8,rem float8,awake float8,active_kcal float8,hr_load float8,stress_flags text[])`, id, packet.Inputs); err != nil {
		return err
	}
	var got BankResult
	err = tx.QueryRow(ctx, `select signed_bank,display_bank,state,flags,formula_version,alpha_used,drain_delta,restore_delta,components
        from `+s.profileGraphRelation("semantic_energy_bank_v3")+` where profile=current_user and capture_id=$1`, id).Scan(&got.Bank, &got.Display, &got.State, &got.Flags, &got.FormulaVersion, &got.AlphaUsed, &got.TodayDrain, &got.TodayRestore, &got.Components)
	if err != nil {
		return err
	}
	if len(got.Flags) == 0 {
		got.Flags = nil
	}
	if !reflect.DeepEqual(got, packet.Result) {
		return errors.New("replayed dbt Energy result differs from captured product result")
	}
	return tx.Rollback(ctx)
}
