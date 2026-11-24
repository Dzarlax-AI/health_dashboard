package storage

import (
	"context"
	_ "embed"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

//go:embed sql/hdbt_decision_inputs.sql
var decisionJournalDDL string

const SettingDecisionCapture = "dbt_decision_capture_v1"

// SettingDecisionResultReads switches exact captured decisions independently of chart reads.
const SettingDecisionResultReads = "dbt_decision_result_reads_v1"

const decisionPublishedReadPredicate = `exists (
    select from settings f cross join hdbt_state s
    where f.key='` + SettingDecisionResultReads + `' and f.value='true'
      and s.singleton and s.shadow_enabled
      and not exists (
        select from unnest(array['settings','hdbt_state','hdbt_decision_results']) rel
        where to_regclass(format('%I.%I',current_schema(),rel)) is distinct from to_regclass(rel)
      )
)`

// Only an enabled writer pays for JSON packaging. One flag lookup covers
// every result in a bulk score query. Older or read-only handles keep the
// original solver. A failed receipt insert cannot abort a caller's transaction.
func withDecisionJournal[T any](ctx context.Context, s *DB, read func(*DB, bool) (T, error)) (T, error) {
	if s.profileGraphSchema != "" {
		return read(s, false)
	}
	var result T
	var enabled bool
	attempt := func(db *DB) error {
		err := db.pool.QueryRow(ctx, `select (current_setting('transaction_read_only')='off'
            and to_regclass(format('%I.hdbt_decision_inputs',current_schema()))=to_regclass('hdbt_decision_inputs')
            and to_regclass(format('%I.settings',current_schema()))=to_regclass('settings')
            and exists(select from settings where key=$1 and value='true')) is true`, SettingDecisionCapture).Scan(&enabled)
		if err != nil || !enabled {
			return err
		}
		result, err = read(db, true)
		return err
	}
	var err error
	if unit, ok := s.pool.(*cacheTransaction); ok {
		err = pgx.BeginFunc(ctx, unit, func(tx pgx.Tx) error { return attempt(&DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}) })
	} else {
		err = attempt(s)
	}
	if err == nil && enabled {
		return result, nil
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if err != nil && enabled {
		log.Print("dbt decision capture failed; using ordinary calculation")
	}
	return read(s, false)
}

// A bulk evaluation publishes all its receipts with the result SELECT in the
// same database statement. Retries keep the first observed receipt. Formula
// revision contributes to the ID even when a revision leaves values equal.
const decisionJournalInsertSQL = `, saved_inputs as (
    insert into hdbt_decision_inputs(metric,decision_key,input_hash,captured_at,formula_version,inputs,result)
    select metric,decision_key,encode(sha256(convert_to(jsonb_build_array(formula_version,inputs)::text,'UTF8')),'hex'),
        statement_timestamp(),formula_version,inputs,result from decision_packets
    where exists(select from settings where key='` + SettingDecisionCapture + `' and value='true')
    on conflict(metric,decision_key,input_hash) do nothing
)`

// DecisionJournalParity reports only counts, including receipts for which a
// future or retired formula version has no compatible replay in this graph.
type DecisionJournalParity struct {
	Recorded    int `json:"recorded"`
	Matched     int `json:"matched"`
	Missing     int `json:"missing"`
	Different   int `json:"different"`
	Unsupported int `json:"unsupported"`
}

// CompareLocalDecisionJournal verifies one dated decision, including each
// revision observed on that date. Schema and role come from the local handle.
func (s *DB) CompareLocalDecisionJournal(ctx context.Context, metric, date string) (DecisionJournalParity, error) {
	var summary DecisionJournalParity
	if s.profileGraphSchema == "" {
		return summary, errors.New("journal replay requires an isolated local graph")
	}
	if _, err := time.Parse(time.DateOnly, date); err != nil {
		return summary, err
	}
	var kind, key, model, id, projection string
	var version int
	switch metric {
	case "today":
		kind, key, model, id, version = "today_decision", date, "daily_decision_boundary_v1", "capture", 1
		projection = `jsonb_build_object('mode',m.decision_mode,'evidence_domains',m.evidence_domains,'signal_keys',m.signal_keys)`
	case "energy":
		kind, key, model, id, version = "energy_bank", date, "semantic_energy_bank_v3", "capture_id", DefaultEnergyConfig().FormulaVersion
		projection = `jsonb_build_object('Bank',m.signed_bank,'Display',m.display_bank,'State',m.state,'Flags',m.flags,
            'FormulaVersion',m.formula_version,'AlphaUsed',m.alpha_used,'TodayDrain',m.drain_delta,'TodayRestore',m.restore_delta,'Components',m.components)`
	case "readiness-history", "readiness-morning":
		kind, key, model, id, version = "readiness", strings.TrimPrefix(metric, "readiness-")+"/"+date, "semantic_readiness_score_v1", "case_id", ScoreVersion
		projection = `jsonb_build_object('RawScore',m.raw_score,'DisplayScore',m.solver_display_score,
            'Confidence',coalesce(m.solver_confidence,''),'CapReason',coalesce(m.solver_cap_reason,''))`
	default:
		return summary, errors.New("unsupported decision metric")
	}
	receiptID := "'journal:'||j.input_hash"
	if metric == "today" {
		receiptID = "to_jsonb(" + receiptID + ")"
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err := s.pool.QueryRow(ctx, `select count(*),count(*) filter(where supported and found and same),
        count(*) filter(where supported and not found),count(*) filter(where supported and found and not same),
        count(*) filter(where not supported) from (
        select j.formula_version=$3 as supported,m.`+id+` is not null as found,j.result=`+projection+` as same
        from hdbt_decision_inputs j left join `+s.profileGraphRelation(model)+` m
          on m.profile=current_user and m.`+id+`=`+receiptID+`
        where j.metric=$1 and j.decision_key=$2) records`, kind, key, version).Scan(&summary.Recorded, &summary.Matched, &summary.Missing, &summary.Different, &summary.Unsupported)
	return summary, err
}
