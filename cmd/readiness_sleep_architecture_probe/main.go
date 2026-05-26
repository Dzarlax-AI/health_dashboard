package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type sample struct {
	date     string
	target   float64
	floor    float64
	features []float64
}

type result struct {
	Schema       string
	Target       string
	Samples      int
	Train        int
	Test         int
	FloorMetric  float64
	ModelMetric  float64
	Delta        float64
	Pass         bool
	Inconclusive bool
	Reason       string
}

func main() {
	var schemaFlag string
	var minSamples int
	flag.StringVar(&schemaFlag, "schema", "", "schema to inspect, comma-separated list, or all; empty uses current search_path")
	flag.IntVar(&minSamples, "min-samples", 40, "minimum rows required for a conclusive probe")
	flag.Parse()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is required")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(2)
	}
	defer pool.Close()

	schemas, err := resolveSchemas(ctx, pool, schemaFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "schemas: %v\n", err)
		os.Exit(2)
	}
	if len(schemas) == 0 {
		schemas = []string{""}
	}

	var failed bool
	for _, schema := range schemas {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "schema %q acquire: %v\n", schema, err)
			failed = true
			continue
		}
		if err := setReadOnlySearchPath(ctx, conn, schema); err != nil {
			conn.Release()
			fmt.Fprintf(os.Stderr, "schema %q: %v\n", schema, err)
			failed = true
			continue
		}
		recoveryRows, err := loadRecoverySamples(ctx, conn)
		if err != nil {
			conn.Release()
			fmt.Fprintf(os.Stderr, "schema %q recovery: %v\n", schema, err)
			failed = true
			continue
		}
		chronicRows, err := loadChronicSamples(ctx, conn)
		if err != nil {
			conn.Release()
			fmt.Fprintf(os.Stderr, "schema %q chronic: %v\n", schema, err)
			failed = true
			continue
		}
		conn.Release()
		printResult(evaluateRecovery(schemaName(schema), recoveryRows, minSamples))
		printResult(evaluateChronic(schemaName(schema), chronicRows, minSamples))
	}
	if failed {
		os.Exit(1)
	}
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

func loadRecoverySamples(ctx context.Context, conn *pgxpool.Conn) ([]sample, error) {
	rows, err := conn.Query(ctx, `
		SELECT ts.date,
		       ts.target_value::float8,
		       nb.predicted_value::float8,
		       (fs.features->>'waso_hours_7d')::float8,
		       (fs.features->>'explicit_wake_bouts_7d')::float8,
		       (fs.features->>'gap_inferred_wake_bouts_7d')::float8,
		       (fs.features->>'fragmentation_index_7d')::float8,
		       (fs.features->>'architecture_eligible_days_7d')::float8
		  FROM target_snapshots ts
		  JOIN naive_baselines nb
		    ON nb.date = ts.date
		   AND nb.sub_score = ts.sub_score
		   AND nb.target_kind = ts.target_kind
		   AND nb.baseline_kind = 'ewma_45d'
		  JOIN feature_snapshots fs
		    ON fs.date = ts.date
		   AND fs.sub_score = ts.sub_score
		 WHERE ts.sub_score = 'recovery_stability'
		   AND ts.target_kind = 'rolling_3d'
		   AND ts.eligible = TRUE
		   AND ts.target_value IS NOT NULL
		   AND nb.predicted_value IS NOT NULL
		   AND fs.feature_version >= 2
		   AND fs.features ? 'architecture_eligible_days_7d'
		   AND fs.features ? 'waso_hours_7d'
		   AND COALESCE(fs.features->>'architecture_available_through', ts.date) <= ts.date
		 ORDER BY ts.date ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sample
	for rows.Next() {
		var s sample
		s.features = make([]float64, 5)
		if err := rows.Scan(&s.date, &s.target, &s.floor,
			&s.features[0], &s.features[1], &s.features[2], &s.features[3], &s.features[4]); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func loadChronicSamples(ctx context.Context, conn *pgxpool.Conn) ([]sample, error) {
	rows, err := conn.Query(ctx, `
		SELECT ts.date,
		       ts.target_value::float8,
		       nb.predicted_value::float8,
		       (fs.features->>'waso_hours_14d')::float8,
		       (fs.features->>'explicit_wake_bouts_14d')::float8,
		       (fs.features->>'gap_inferred_wake_bouts_14d')::float8,
		       (fs.features->>'fragmentation_index_14d')::float8,
		       (fs.features->>'architecture_eligible_days_14d')::float8
		  FROM target_snapshots ts
		  JOIN naive_baselines nb
		    ON nb.date = ts.date
		   AND nb.sub_score = ts.sub_score
		   AND nb.target_kind = ts.target_kind
		   AND nb.baseline_kind = 'event_base_rate'
		  JOIN feature_snapshots fs
		    ON fs.date = ts.date
		   AND fs.sub_score = ts.sub_score
		 WHERE ts.sub_score = 'chronic_load'
		   AND ts.target_kind = 'chronic_label'
		   AND ts.eligible = TRUE
		   AND ts.target_value IS NOT NULL
		   AND nb.predicted_value IS NOT NULL
		   AND fs.feature_version >= 2
		   AND fs.features ? 'architecture_eligible_days_14d'
		   AND fs.features ? 'waso_hours_14d'
		   AND COALESCE(fs.features->>'architecture_available_through', ts.date) <= ts.date
		 ORDER BY ts.date ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sample
	for rows.Next() {
		var s sample
		s.features = make([]float64, 5)
		if err := rows.Scan(&s.date, &s.target, &s.floor,
			&s.features[0], &s.features[1], &s.features[2], &s.features[3], &s.features[4]); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func evaluateRecovery(schema string, rows []sample, minSamples int) result {
	r := result{Schema: schema, Target: "recovery_stability/rolling_3d"}
	r.Samples = len(rows)
	if len(rows) < minSamples {
		r.Inconclusive = true
		r.Reason = "insufficient v2 feature samples"
		return r
	}
	train, test := splitChronological(rows)
	r.Train, r.Test = len(train), len(test)
	if len(test) == 0 {
		r.Inconclusive = true
		r.Reason = "empty test split"
		return r
	}
	model, err := fitLinear(train)
	if err != nil {
		r.Inconclusive = true
		r.Reason = err.Error()
		return r
	}
	r.FloorMetric = meanAbsoluteError(test, func(s sample) float64 { return s.floor })
	r.ModelMetric = meanAbsoluteError(test, func(s sample) float64 { return predictLinearWithFloor(model, s) })
	if r.FloorMetric > 0 {
		r.Delta = (r.FloorMetric - r.ModelMetric) / r.FloorMetric
	}
	r.Pass = r.Delta >= 0.03
	return r
}

func evaluateChronic(schema string, rows []sample, minSamples int) result {
	r := result{Schema: schema, Target: "chronic_load/chronic_label"}
	r.Samples = len(rows)
	if len(rows) < minSamples {
		r.Inconclusive = true
		r.Reason = "insufficient v2 feature samples"
		return r
	}
	train, test := splitChronological(rows)
	r.Train, r.Test = len(train), len(test)
	if len(test) == 0 {
		r.Inconclusive = true
		r.Reason = "empty test split"
		return r
	}
	model, err := fitLinear(train)
	if err != nil {
		r.Inconclusive = true
		r.Reason = err.Error()
		return r
	}
	r.FloorMetric = precisionAtThreshold(test, func(s sample) float64 { return s.floor }, 0.5)
	r.ModelMetric = precisionAtThreshold(test, func(s sample) float64 { return clamp01(predictLinearWithFloor(model, s)) }, 0.5)
	r.Delta = r.ModelMetric - r.FloorMetric
	r.Pass = r.Delta >= 0.05 &&
		topKPrecision(test, func(s sample) float64 { return clamp01(predictLinearWithFloor(model, s)) }) >=
			topKPrecision(test, func(s sample) float64 { return s.floor })
	return r
}

func splitChronological(rows []sample) ([]sample, []sample) {
	cut := int(math.Round(float64(len(rows)) * 0.7))
	if cut < 1 {
		cut = 1
	}
	if cut >= len(rows) {
		cut = len(rows) - 1
	}
	return rows[:cut], rows[cut:]
}

func fitLinear(rows []sample) ([]float64, error) {
	if len(rows) == 0 {
		return nil, errors.New("no train rows")
	}
	p := len(rows[0].features) + 2 // intercept + floor + architecture features
	xtx := make([][]float64, p)
	xty := make([]float64, p)
	for i := range xtx {
		xtx[i] = make([]float64, p)
	}
	for _, row := range rows {
		x := append([]float64{1, row.floor}, row.features...)
		for i := 0; i < p; i++ {
			xty[i] += x[i] * row.target
			for j := 0; j < p; j++ {
				xtx[i][j] += x[i] * x[j]
			}
		}
	}
	for i := 0; i < p; i++ {
		xtx[i][i] += 1e-6
	}
	return solveLinearSystem(xtx, xty)
}

func predictLinearWithFloor(beta []float64, row sample) float64 {
	x := append([]float64{1, row.floor}, row.features...)
	var out float64
	for i, b := range beta {
		out += b * x[i]
	}
	return out
}

func meanAbsoluteError(rows []sample, pred func(sample) float64) float64 {
	var sum float64
	for _, row := range rows {
		sum += math.Abs(row.target - pred(row))
	}
	return sum / float64(len(rows))
}

func precisionAtThreshold(rows []sample, pred func(sample) float64, threshold float64) float64 {
	var predicted, truePositive int
	for _, row := range rows {
		if pred(row) < threshold {
			continue
		}
		predicted++
		if row.target >= 0.5 {
			truePositive++
		}
	}
	if predicted == 0 {
		return 0
	}
	return float64(truePositive) / float64(predicted)
}

func topKPrecision(rows []sample, pred func(sample) float64) float64 {
	type scored struct {
		score  float64
		target float64
	}
	scoredRows := make([]scored, 0, len(rows))
	positives := 0
	for _, row := range rows {
		if row.target >= 0.5 {
			positives++
		}
		scoredRows = append(scoredRows, scored{score: pred(row), target: row.target})
	}
	if positives == 0 {
		return 0
	}
	sort.Slice(scoredRows, func(i, j int) bool { return scoredRows[i].score > scoredRows[j].score })
	k := positives
	if k > len(scoredRows) {
		k = len(scoredRows)
	}
	var hits int
	for i := 0; i < k; i++ {
		if scoredRows[i].target >= 0.5 {
			hits++
		}
	}
	return float64(hits) / float64(k)
}

func solveLinearSystem(a [][]float64, b []float64) ([]float64, error) {
	n := len(b)
	for i := 0; i < n; i++ {
		pivot := i
		for r := i + 1; r < n; r++ {
			if math.Abs(a[r][i]) > math.Abs(a[pivot][i]) {
				pivot = r
			}
		}
		if math.Abs(a[pivot][i]) < 1e-12 {
			return nil, errors.New("singular design matrix")
		}
		a[i], a[pivot] = a[pivot], a[i]
		b[i], b[pivot] = b[pivot], b[i]
		div := a[i][i]
		for c := i; c < n; c++ {
			a[i][c] /= div
		}
		b[i] /= div
		for r := 0; r < n; r++ {
			if r == i {
				continue
			}
			f := a[r][i]
			for c := i; c < n; c++ {
				a[r][c] -= f * a[i][c]
			}
			b[r] -= f * b[i]
		}
	}
	return b, nil
}

func printResult(r result) {
	status := "fail"
	if r.Inconclusive {
		status = "inconclusive"
	} else if r.Pass {
		status = "pass"
	}
	fmt.Printf("%s\t%s\t%s\tsamples=%d train=%d test=%d floor=%.4f model=%.4f delta=%.4f",
		r.Schema, r.Target, status, r.Samples, r.Train, r.Test, r.FloorMetric, r.ModelMetric, r.Delta)
	if r.Reason != "" {
		fmt.Printf("\treason=%s", r.Reason)
	}
	fmt.Println()
}

func schemaName(schema string) string {
	if schema == "" {
		return "current"
	}
	return schema
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
