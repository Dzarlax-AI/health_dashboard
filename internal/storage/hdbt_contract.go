package storage

import (
	"context"
	"errors"
	"strings"
)

func addHDBTContract(m *ContractManifest) {
	const daily = "date hrv_avg rhr_avg sleep_total sleep_deep sleep_rem sleep_core sleep_awake sleep_unspecified steps calories exercise_min spo2_avg vo2_avg resp_avg"
	const hourly = "metric_name hour source avg_val min_val max_val sample_count"
	const points = "metric_name date source qty quality health_record_id origin units id received_at source_snapshot_at"
	const steps = "hour source avg_val min_val max_val sample_count withdrawn proven_input"
	for _, s := range []struct{ tables, columns, key string }{
		{"hdbt_state", "singleton shadow_enabled read_enabled initialized ready bootstrap_cursor bootstrap_end formula_id source_generation published_at heartbeat_at last_status enrollment_epoch", "singleton"},
		{"hdbt_decision_inputs", "metric decision_key input_hash captured_at formula_version inputs result", "metric decision_key input_hash"},
		{"hdbt_i_decisions", "metric decision_key input_hash", "metric decision_key input_hash"},
		{"hdbt_decision_queue", "metric decision_key input_hash blocked_reason", "metric decision_key input_hash"},
		{"hdbt_decision_results", "metric decision_key input_hash formula_version result", "metric decision_key input_hash"},
		{"hdbt_dirty", "date generation", "date"},
		{"hdbt_daily_withdrawals", "date metric_name generation", "date metric_name"},
		{"hdbt_i_daily_withdrawals", "date metric_name", "date metric_name"},
		{"hdbt_step_repairs", "point_date source generation", "point_date source"},
		{"hdbt_i_repairs", "point_date source", "point_date source"},
		{"hdbt_hourly_metrics hdbt_w_hourly", hourly, "metric_name hour source"},
		{"hdbt_daily_metrics hdbt_w_daily", daily, "date"},
		{"hdbt_step_state hdbt_w_step", steps, "hour source"},
		{"hdbt_step_inputs hdbt_i_points hdbt_w_points", points, "id"},
		{"hdbt_i_calendar hdbt_i_publish hdbt_backfill", "date", "date"},
		{"hdbt_i_records", "id content_type", "id"},
		{"hdbt_work_lease", "singleton attempt", "singleton"},
	} {
		for _, table := range strings.Fields(s.tables) {
			m.Tables = append(m.Tables, table)
			m.Columns[table] = strings.Fields(s.columns)
			m.Constraints = append(m.Constraints, ConstraintDefinition{Table: table, Kind: "p", Columns: strings.Fields(s.key)})
		}
	}
	for _, column := range []struct{ name, typ string }{
		{"metric", "text"}, {"decision_key", "text"}, {"input_hash", "text"},
		{"captured_at", "timestamp with time zone"}, {"formula_version", "integer"},
		{"inputs", "jsonb"}, {"result", "jsonb"},
	} {
		m.ColumnDefinitions = append(m.ColumnDefinitions, ColumnDefinition{
			Table: "hdbt_decision_inputs", Column: column.name, DataType: column.typ,
		})
		if column.name != "captured_at" && column.name != "inputs" {
			m.ColumnDefinitions = append(m.ColumnDefinitions, ColumnDefinition{Table: "hdbt_decision_results", Column: column.name, DataType: column.typ})
			if column.name != "result" && column.name != "formula_version" {
				for _, table := range []string{"hdbt_i_decisions", "hdbt_decision_queue"} {
					m.ColumnDefinitions = append(m.ColumnDefinitions, ColumnDefinition{Table: table, Column: column.name, DataType: column.typ})
				}
			}
		}
	}
	m.ColumnDefinitions = append(m.ColumnDefinitions, ColumnDefinition{Table: "hdbt_decision_queue", Column: "blocked_reason", DataType: "text", Nullable: true})
	for _, col := range []string{"shadow_enabled", "read_enabled", "initialized", "ready"} {
		m.ColumnDefinitions = append(m.ColumnDefinitions, ColumnDefinition{Table: "hdbt_state", Column: col, DataType: "boolean", Default: "false", RequireDefault: true})
	}
	for _, index := range []struct{ name, table, key string }{
		{"hdbt_hourly_day", "hdbt_hourly_metrics", "substring(hour,1,10)"},
		{"hdbt_step_day", "hdbt_step_state", "substring(hour,1,10)"},
		{"hdbt_step_input_day", "hdbt_step_inputs", "substring(date,1,10)"},
	} {
		m.Indexes = append(m.Indexes, index.name)
		m.IndexDefinitions = append(m.IndexDefinitions, IndexDefinition{Name: index.name, Table: index.table, AccessMethod: "btree", Keys: []string{index.key}})
	}
	m.RequiredRows = append(m.RequiredRows, RequiredRow{Table: "hdbt_state", Values: map[string]string{"singleton": "true"}})
}

func (s *DB) verifyHDBTEnrollmentTrigger(ctx context.Context) error {
	var valid bool
	err := s.pool.QueryRow(ctx, `select exists(select from pg_trigger t join pg_class c on c.oid=t.tgrelid
        join pg_namespace n on n.oid=c.relnamespace join pg_proc f on f.oid=t.tgfoid
        where n.nspname=current_schema() and c.relname='hdbt_state' and t.tgname='hdbt_enrollment_change'
        and t.tgenabled in ('O','A') and t.tgtype=19 and f.pronamespace=n.oid
        and f.proname='hdbt_reset_enrollment' and not f.prosecdef
        and t.tgattr::text=(select attnum::text from pg_attribute
            where attrelid=c.oid and attname='shadow_enabled' and not attisdropped)
        and position('whenold.shadow_enabledisdistinctfromnew.shadow_enabledexecutefunction' in
            regexp_replace(lower(pg_get_triggerdef(t.oid)), '[[:space:]()]', '', 'g'))>0)`).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return errors.New("missing aggregate enrollment reset trigger")
	}
	err = s.pool.QueryRow(ctx, `select exists(select from pg_trigger t join pg_class c on c.oid=t.tgrelid
		join pg_namespace n on n.oid=c.relnamespace join pg_proc f on f.oid=t.tgfoid
		where n.nspname=current_schema() and c.relname='hdbt_decision_inputs' and t.tgname='hdbt_enqueue_decision'
		and t.tgenabled in ('O','A') and t.tgtype=5 and f.pronamespace=n.oid
		and f.proname='hdbt_enqueue_decision' and not f.prosecdef)`).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return errors.New("missing decision queue trigger")
	}
	return nil
}
