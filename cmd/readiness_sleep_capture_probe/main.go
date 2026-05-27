package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"health-receiver/internal/health"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const isoDate = "2006-01-02"

type targetRow struct {
	Date     string
	Eligible bool
	Reason   string
}

type probeResult struct {
	Schema                     string
	Rows                       int
	StrictEligible             int
	Candidate2of3              int
	Rescued2of3                int
	LowCaptureRows             int
	PartialShortRows           int
	MissingCaptureRows         int
	StrictMAERows              int
	StrictMAE                  *float64
	PersistedCandidateRows     int
	PersistedCandidateEligible int
	PersistedCandidateMAERows  int
	PersistedCandidateMAE      *float64
	SampleDates                []string
}

func main() {
	var schemaFlag string
	var from string
	var to string
	flag.StringVar(&schemaFlag, "schema", "", "schema to inspect, comma-separated list, or all; empty uses current search_path")
	flag.StringVar(&from, "from", "", "optional target_snapshots start date (YYYY-MM-DD)")
	flag.StringVar(&to, "to", "", "optional target_snapshots end date (YYYY-MM-DD)")
	flag.Parse()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is required")
		os.Exit(2)
	}
	fromDate, err := parseDateFlag("from", from)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	toDate, err := parseDateFlag("to", to)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if fromDate != nil && toDate != nil && fromDate.After(*toDate) {
		fmt.Fprintf(os.Stderr, "from date %q must not be after to date %q\n", from, to)
		os.Exit(2)
	}

	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(2)
	}
	defer pool.Close()

	schemaCtx, schemaCancel := context.WithTimeout(context.Background(), 30*time.Second)
	schemas, err := resolveSchemas(schemaCtx, pool, schemaFlag)
	schemaCancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "schemas: %v\n", err)
		os.Exit(2)
	}
	if len(schemas) == 0 {
		schemas = []string{""}
	}

	var failed bool
	for _, schema := range schemas {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		conn, err := pool.Acquire(ctx)
		if err != nil {
			cancel()
			fmt.Fprintf(os.Stderr, "schema %q acquire: %v\n", schema, err)
			failed = true
			continue
		}
		if err := setReadOnlySearchPath(ctx, conn, schema); err != nil {
			conn.Release()
			cancel()
			fmt.Fprintf(os.Stderr, "schema %q: %v\n", schema, err)
			failed = true
			continue
		}
		res, err := runProbe(ctx, conn, schemaName(schema), from, to)
		conn.Release()
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "schema %q probe: %v\n", schema, err)
			failed = true
			continue
		}
		printResult(res)
	}
	if failed {
		os.Exit(1)
	}
}

func parseDateFlag(name, value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(isoDate, value)
	if err != nil {
		return nil, fmt.Errorf("%s: parse %q: %w", name, value, err)
	}
	return &parsed, nil
}

func runProbe(ctx context.Context, conn *pgxpool.Conn, schema, from, to string) (probeResult, error) {
	targets, err := loadTargets(ctx, conn, from, to)
	if err != nil {
		return probeResult{}, err
	}
	res := probeResult{Schema: schema, Rows: len(targets)}
	if len(targets) == 0 {
		return res, nil
	}
	loadFrom, loadTo, err := sleepLoadRange(targets)
	if err != nil {
		return probeResult{}, err
	}
	sleepRows, err := loadSleepRows(ctx, conn, loadFrom, loadTo)
	if err != nil {
		return probeResult{}, err
	}

	for _, target := range targets {
		if target.Eligible {
			res.StrictEligible++
		}
		days, err := forwardDates(target.Date)
		if err != nil {
			return probeResult{}, err
		}
		eligibleCount := 0
		lowCapture := false
		partialShort := false
		missingCapture := false
		for _, d := range days {
			row, ok := sleepRows[d]
			if !ok {
				row = health.SleepRow{Date: d}
			}
			eff := health.ComputeSleepEfficiency(row)
			capture := health.ComputeSleepCaptureConfidence(row)
			if eff.Eligible && eff.Efficiency != nil {
				eligibleCount++
			}
			if capture.LowConfidence {
				lowCapture = true
			}
			if capture.Class == health.SleepCapturePartialShort {
				partialShort = true
			}
			if capture.Class == health.SleepCaptureMissing {
				missingCapture = true
			}
		}
		candidate := eligibleCount >= 2
		if candidate {
			res.Candidate2of3++
		}
		if candidate && !target.Eligible {
			res.Rescued2of3++
			if len(res.SampleDates) < 8 {
				res.SampleDates = append(res.SampleDates, target.Date)
			}
		}
		if lowCapture {
			res.LowCaptureRows++
		}
		if partialShort {
			res.PartialShortRows++
		}
		if missingCapture {
			res.MissingCaptureRows++
		}
	}
	if err := loadPersistedTargetMetrics(ctx, conn, from, to, &res); err != nil {
		return probeResult{}, err
	}
	return res, nil
}

func loadPersistedTargetMetrics(ctx context.Context, conn *pgxpool.Conn, from, to string, res *probeResult) error {
	where := []string{
		"ts.sub_score = 'recovery_stability'",
		"ts.target_kind IN ('rolling_3d', 'rolling_3d_candidate_2of3')",
	}
	args := []any{}
	if from != "" {
		args = append(args, from)
		where = append(where, fmt.Sprintf("ts.date >= $%d", len(args)))
	}
	if to != "" {
		args = append(args, to)
		where = append(where, fmt.Sprintf("ts.date <= $%d", len(args)))
	}
	rows, err := conn.Query(ctx, `
		SELECT ts.target_kind,
		       count(*)::int,
		       count(*) FILTER (WHERE ts.eligible)::int,
		       count(*) FILTER (
		         WHERE ts.eligible
		           AND ts.target_value IS NOT NULL
		           AND nb.predicted_value IS NOT NULL
		       )::int,
		       avg(abs(ts.target_value::float8 - nb.predicted_value::float8)) FILTER (
		         WHERE ts.eligible
		           AND ts.target_value IS NOT NULL
		           AND nb.predicted_value IS NOT NULL
		       )::float8
		  FROM target_snapshots ts
		  LEFT JOIN naive_baselines nb
		    ON nb.date = ts.date
		   AND nb.sub_score = ts.sub_score
		   AND nb.target_kind = ts.target_kind
		   AND nb.baseline_kind = 'ewma_45d'
		 WHERE `+strings.Join(where, " AND ")+`
		 GROUP BY ts.target_kind
	`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var targetKind string
		var rowsCount, eligibleCount, maeRows int
		var mae *float64
		if err := rows.Scan(&targetKind, &rowsCount, &eligibleCount, &maeRows, &mae); err != nil {
			return err
		}
		switch targetKind {
		case "rolling_3d":
			res.StrictMAERows = maeRows
			res.StrictMAE = mae
		case "rolling_3d_candidate_2of3":
			res.PersistedCandidateRows = rowsCount
			res.PersistedCandidateEligible = eligibleCount
			res.PersistedCandidateMAERows = maeRows
			res.PersistedCandidateMAE = mae
		}
	}
	return rows.Err()
}

func loadTargets(ctx context.Context, conn *pgxpool.Conn, from, to string) ([]targetRow, error) {
	where := []string{"sub_score = 'recovery_stability'", "target_kind = 'rolling_3d'"}
	args := []any{}
	if from != "" {
		args = append(args, from)
		where = append(where, fmt.Sprintf("date >= $%d", len(args)))
	}
	if to != "" {
		args = append(args, to)
		where = append(where, fmt.Sprintf("date <= $%d", len(args)))
	}
	rows, err := conn.Query(ctx, `
		SELECT date, eligible, eligibility_reason
		  FROM target_snapshots
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY date ASC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []targetRow
	for rows.Next() {
		var r targetRow
		if err := rows.Scan(&r.Date, &r.Eligible, &r.Reason); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func sleepLoadRange(targets []targetRow) (string, string, error) {
	first, err := time.Parse(isoDate, targets[0].Date)
	if err != nil {
		return "", "", err
	}
	last, err := time.Parse(isoDate, targets[len(targets)-1].Date)
	if err != nil {
		return "", "", err
	}
	return first.AddDate(0, 0, 1).Format(isoDate), last.AddDate(0, 0, 3).Format(isoDate), nil
}

func loadSleepRows(ctx context.Context, conn *pgxpool.Conn, from, to string) (map[string]health.SleepRow, error) {
	rows, err := conn.Query(ctx, `
		SELECT date, sleep_total, sleep_deep, sleep_rem, sleep_core, sleep_awake, sleep_unspecified
		  FROM daily_scores
		 WHERE date BETWEEN $1 AND $2
		 ORDER BY date ASC
	`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]health.SleepRow{}
	for rows.Next() {
		var r health.SleepRow
		var total, deep, rem, core, awake, unsp *float32
		if err := rows.Scan(&r.Date, &total, &deep, &rem, &core, &awake, &unsp); err != nil {
			return nil, err
		}
		r.Total = liftFloat(total)
		r.Deep = liftFloat(deep)
		r.REM = liftFloat(rem)
		r.Core = liftFloat(core)
		r.Awake = liftFloat(awake)
		r.Unspecified = liftFloat(unsp)
		out[r.Date] = r
	}
	return out, rows.Err()
}

func liftFloat(p *float32) *float64 {
	if p == nil {
		return nil
	}
	v := float64(*p)
	return &v
}

func forwardDates(date string) ([]string, error) {
	t, err := time.Parse(isoDate, date)
	if err != nil {
		return nil, err
	}
	return []string{
		t.AddDate(0, 0, 1).Format(isoDate),
		t.AddDate(0, 0, 2).Format(isoDate),
		t.AddDate(0, 0, 3).Format(isoDate),
	}, nil
}

func resolveSchemas(ctx context.Context, pool *pgxpool.Pool, raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	switch raw {
	case "":
		return nil, nil
	case "all":
		rows, err := pool.Query(ctx, `SELECT schema_name FROM health_registry.users ORDER BY schema_name`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var schema string
			if err := rows.Scan(&schema); err != nil {
				return nil, err
			}
			out = append(out, schema)
		}
		return out, rows.Err()
	default:
		parts := strings.Split(raw, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		return out, nil
	}
}

func setReadOnlySearchPath(ctx context.Context, conn *pgxpool.Conn, schema string) error {
	if _, err := conn.Exec(ctx, `SET default_transaction_read_only = on`); err != nil {
		return err
	}
	if schema == "" {
		return nil
	}
	_, err := conn.Exec(ctx, "SET search_path TO "+pgx.Identifier{schema}.Sanitize())
	return err
}

func printResult(r probeResult) {
	strictPct := pct(r.StrictEligible, r.Rows)
	candidatePct := pct(r.Candidate2of3, r.Rows)
	fmt.Printf("%s\trecovery_stability/rolling_3d\trows=%d\tstrict=%d/%d (%.1f%%)\tcandidate_2of3=%d/%d (%.1f%%)\trescued=%d\tlow_capture=%d\tpartial_short=%d\tmissing_capture=%d",
		r.Schema, r.Rows,
		r.StrictEligible, r.Rows, strictPct,
		r.Candidate2of3, r.Rows, candidatePct,
		r.Rescued2of3, r.LowCaptureRows, r.PartialShortRows, r.MissingCaptureRows)
	if r.StrictMAE != nil {
		fmt.Printf("\tstrict_ewma45_mae=%.4f/%d", *r.StrictMAE, r.StrictMAERows)
	}
	if r.PersistedCandidateRows > 0 {
		fmt.Printf("\tpersisted_candidate=%d/%d", r.PersistedCandidateEligible, r.PersistedCandidateRows)
		if r.PersistedCandidateMAE != nil {
			fmt.Printf("\tcandidate_ewma45_mae=%.4f/%d", *r.PersistedCandidateMAE, r.PersistedCandidateMAERows)
			if r.StrictMAE != nil && *r.StrictMAE > 0 {
				delta := (*r.StrictMAE - *r.PersistedCandidateMAE) / *r.StrictMAE * 100
				fmt.Printf("\tcandidate_mae_delta=%.1f%%", delta)
			}
		}
	}
	if len(r.SampleDates) > 0 {
		sort.Strings(r.SampleDates)
		fmt.Printf("\trescued_samples=%s", strings.Join(r.SampleDates, ","))
	}
	fmt.Println()
}

func pct(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) * 100 / float64(d)
}

func schemaName(schema string) string {
	if schema == "" {
		return "current"
	}
	return schema
}
