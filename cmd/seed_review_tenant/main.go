package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
)

const (
	isoDate             = "2006-01-02"
	defaultUsername     = "review"
	defaultSchema       = "health_review"
	defaultDays         = 120
	defaultTimezone     = "Europe/Belgrade"
	defaultPassword     = "review-local-only"
	sourceWatch         = "ReviewSeed Apple Watch"
	sourcePhone         = "ReviewSeed iPhone"
	reviewRecordPayload = "synthetic review tenant seed"
)

type seedConfig struct {
	dbURL       string
	username    string
	schema      string
	password    string
	email       string
	days        int
	anchor      string
	timezone    string
	dryRun      bool
	resetWindow bool
}

type dayProfile struct {
	date             time.Time
	index            int
	sleepTotal       float64
	sleepDeep        float64
	sleepREM         float64
	sleepCore        float64
	sleepAwake       float64
	sleepUnspecified float64
	steps            float64
	activeEnergy     float64
	basalEnergy      float64
	exerciseMin      float64
	distanceKM       float64
	hrv              float64
	rhr              float64
	spo2             float64
	resp             float64
	vo2              float64
	wristTemp        float64
	stressLoad       *float64
	stressFlags      []string
}

type seedCounts struct {
	Points          int
	DailyRows       int
	TargetRows      int
	FeatureRows     int
	BaselineRows    int
	EnergyRows      int
	RawRows24h      int
	LatestDailyDate string
}

func main() {
	cfg := parseFlags()
	if err := run(cfg); err != nil {
		log.Fatal(err)
	}
}

func parseFlags() seedConfig {
	var cfg seedConfig
	flag.StringVar(&cfg.dbURL, "db", os.Getenv("DATABASE_URL"), "Postgres connection string; defaults to DATABASE_URL")
	flag.StringVar(&cfg.username, "username", defaultUsername, "review tenant username")
	flag.StringVar(&cfg.schema, "schema", defaultSchema, "review tenant schema")
	flag.StringVar(&cfg.password, "password", defaultPassword, "password used only when creating a new review user")
	flag.StringVar(&cfg.email, "email", "", "optional review user email")
	flag.IntVar(&cfg.days, "days", defaultDays, "rolling synthetic history length")
	flag.StringVar(&cfg.anchor, "anchor", "today", "YYYY-MM-DD anchor date or 'today' in tenant timezone")
	flag.StringVar(&cfg.timezone, "tz", defaultTimezone, "tenant-local timezone")
	flag.BoolVar(&cfg.dryRun, "dry-run", false, "print intended actions without writing")
	flag.BoolVar(&cfg.resetWindow, "reset-window", true, "delete prior ReviewSeed rows in the rolling window before seeding")
	flag.Parse()
	return cfg
}

func run(cfg seedConfig) error {
	if cfg.dbURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.days < 30 || cfg.days > 365 {
		return fmt.Errorf("--days must be in [30, 365]")
	}
	if err := registry.ValidateUsername(cfg.username); err != nil {
		return err
	}
	if err := registry.ValidateSchemaName(cfg.schema); err != nil {
		return err
	}
	if cfg.username != defaultUsername || cfg.schema != defaultSchema {
		return fmt.Errorf("refusing to seed non-review tenant %q/%q; use %s/%s", cfg.username, cfg.schema, defaultUsername, defaultSchema)
	}

	loc, err := time.LoadLocation(cfg.timezone)
	if err != nil {
		return fmt.Errorf("load timezone %q: %w", cfg.timezone, err)
	}
	anchor, err := resolveAnchor(cfg.anchor, loc)
	if err != nil {
		return err
	}
	from := anchor.AddDate(0, 0, -(cfg.days - 1))
	to := anchor
	profiles := buildProfiles(from, cfg.days)
	fmt.Printf("review seed window: %s..%s (%d days, tz=%s)\n", from.Format(isoDate), to.Format(isoDate), cfg.days, cfg.timezone)

	if cfg.dryRun {
		fmt.Printf("dry-run: would ensure user=%s schema=%s, resetWindow=%v, seed points~%d\n",
			cfg.username, cfg.schema, cfg.resetWindow, len(pointsForProfiles(profiles, loc)))
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	if err := ensureReviewTenant(ctx, cfg); err != nil {
		return err
	}
	db, err := storage.NewWithSchema(ctx, cfg.dbURL, cfg.schema)
	if err != nil {
		return fmt.Errorf("open review schema: %w", err)
	}
	defer db.Close()

	db.EnsureAIBriefingsTable()
	db.EnsureAIBriefingBlocksTable()
	db.EnsureEnergySnapshotsTable()
	db.EnsureReadinessRedesignTables()
	db.EnsureSubjectiveCheckinsTable()

	seedPool, err := openSchemaPool(ctx, cfg.dbURL, cfg.schema)
	if err != nil {
		return err
	}
	defer seedPool.Close()

	if cfg.resetWindow {
		if err := resetSeedWindow(ctx, seedPool, from.Format(isoDate), to.Format(isoDate)); err != nil {
			return err
		}
	}

	points := pointsForProfiles(profiles, loc)
	inserted, err := db.BulkInsertPoints(reviewRecordPayload, points)
	if err != nil {
		return fmt.Errorf("bulk insert points: %w", err)
	}
	fmt.Printf("metric_points inserted: %d (generated=%d)\n", inserted, len(points))

	dates := make([]string, 0, len(profiles))
	for _, p := range profiles {
		dates = append(dates, p.date.Format(isoDate))
	}
	db.UpsertRecentCache(dates, true)
	if err := upsertDailySyntheticFields(ctx, seedPool, profiles); err != nil {
		return err
	}
	db.RecomputeReadinessSince(from.Format(isoDate))
	db.RunReadinessRedesignBackfillForDatesAt(dates, to)
	if err := backfillEnergy(ctx, db, cfg.timezone, from.Format(isoDate), to.Format(isoDate)); err != nil {
		return err
	}
	if err := syncDailyEnergySnapshots(ctx, seedPool, from.Format(isoDate), to.Format(isoDate)); err != nil {
		return err
	}

	counts, err := loadCounts(ctx, seedPool)
	if err != nil {
		return err
	}
	printSummary(cfg, counts)
	return nil
}

func ensureReviewTenant(ctx context.Context, cfg seedConfig) error {
	reg, err := registry.New(ctx, cfg.dbURL)
	if err != nil {
		return fmt.Errorf("open registry: %w", err)
	}
	defer reg.Close()
	if err := reg.EnsureSchema(ctx); err != nil {
		return fmt.Errorf("ensure registry schema: %w", err)
	}

	user, err := reg.GetByUsername(ctx, cfg.username)
	if err == nil {
		if user.SchemaName != cfg.schema {
			return fmt.Errorf("review user exists with schema %q, want %q", user.SchemaName, cfg.schema)
		}
		fmt.Printf("review user exists: username=%s schema=%s api_key=%s\n", user.Username, user.SchemaName, maskSecret(user.APIKey))
	} else if errors.Is(err, registry.ErrUserNotFound) {
		user, err = reg.CreateUser(ctx, registry.CreateUserReq{
			Username:   cfg.username,
			SchemaName: cfg.schema,
			Password:   cfg.password,
			Email:      cfg.email,
			IsAdmin:    false,
		})
		if err != nil {
			return fmt.Errorf("create review user: %w", err)
		}
		fmt.Printf("created review user: username=%s schema=%s api_key=%s password=<redacted>\n",
			user.Username, user.SchemaName, maskSecret(user.APIKey))
	} else {
		return fmt.Errorf("lookup review user: %w", err)
	}

	bootstrap, err := storage.New(ctx, cfg.dbURL)
	if err != nil {
		return fmt.Errorf("open bootstrap db: %w", err)
	}
	defer bootstrap.Close()
	if err := bootstrap.CreateSchema(ctx, cfg.schema); err != nil {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42P06" {
			return fmt.Errorf("create schema %s: %w", cfg.schema, err)
		}
	}

	db, err := storage.NewWithSchema(ctx, cfg.dbURL, cfg.schema)
	if err != nil {
		return fmt.Errorf("open tenant db: %w", err)
	}
	defer db.Close()
	if err := db.EnsureAllTables(); err != nil {
		return fmt.Errorf("ensure tenant tables: %w", err)
	}
	db.EnsureIndexes()
	db.EnsureAIBriefingsTable()
	db.EnsureAIBriefingBlocksTable()
	db.EnsureEnergySnapshotsTable()
	db.EnsureReadinessRedesignTables()
	db.EnsureSubjectiveCheckinsTable()
	if err := db.VerifyReadinessRedesignSchema(); err != nil {
		return fmt.Errorf("verify readiness schema: %w", err)
	}
	return nil
}

func openSchemaPool(ctx context.Context, dbURL, schema string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return nil, err
	}
	config.MaxConns = 4
	config.MinConns = 1
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	quoted := pgx.Identifier{schema}.Sanitize()
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path = "+quoted)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func resetSeedWindow(ctx context.Context, pool *pgxpool.Pool, from, to string) error {
	stmts := []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM energy_snapshots WHERE $1 = ANY(flags)`, []any{"backfilled"}},
		{`DELETE FROM target_snapshots`, nil},
		{`DELETE FROM feature_snapshots`, nil},
		{`DELETE FROM naive_baselines`, nil},
		{`DELETE FROM hourly_metrics WHERE source LIKE 'ReviewSeed%'`, nil},
		{`DELETE FROM metric_points WHERE source LIKE 'ReviewSeed%'`, nil},
		{`DELETE FROM health_records WHERE automation_name = $1`, []any{reviewRecordPayload}},
		{`DELETE FROM daily_scores`, nil},
	}
	for _, stmt := range stmts {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			return fmt.Errorf("reset window %s..%s: %w", from, to, err)
		}
	}
	return nil
}

func buildProfiles(from time.Time, days int) []dayProfile {
	profiles := make([]dayProfile, 0, days)
	for i := 0; i < days; i++ {
		d := from.AddDate(0, 0, i)
		weekday := d.Weekday()
		weekend := weekday == time.Saturday || weekday == time.Sunday
		phase := float64(i) / 7.0
		longPhase := float64(i) / 29.0

		sleep := 7.35 + 0.35*math.Sin(phase*2*math.Pi) + 0.25*math.Sin(longPhase*2*math.Pi)
		steps := 7900 + 1800*math.Sin(phase*2*math.Pi+0.8) + 900*math.Sin(longPhase*2*math.Pi)
		exercise := 38 + 18*math.Sin(phase*2*math.Pi+0.4)
		hrv := 54 + 7*math.Sin(longPhase*2*math.Pi+1.2)
		rhr := 58 - 3*math.Sin(longPhase*2*math.Pi+1.2)
		resp := 15.8 + 0.3*math.Sin(phase*2*math.Pi)
		spo2 := 97.2 + 0.4*math.Sin(longPhase*2*math.Pi+0.5)
		vo2 := 42 + 1.5*math.Sin(longPhase*2*math.Pi+0.2)
		temp := 0.0 + 0.08*math.Sin(phase*2*math.Pi)

		if weekend {
			sleep += 0.45
			steps += 1100
			exercise += 8
		}

		flags := []string{}
		switch {
		case i >= 18 && i <= 23:
			sleep -= 1.35
			hrv -= 10
			rhr += 5
			resp += 0.8
			temp += 0.18
			steps -= 1600
			exercise -= 18
			flags = append(flags, "acute_stress")
		case i >= 47 && i <= 52:
			sleep -= 2.0
			hrv -= 14
			rhr += 7
			resp += 1.2
			temp += 0.45
			steps -= 3000
			exercise -= 28
			flags = append(flags, "illness_signature", "recovery_debt")
		case i >= 75 && i <= 80:
			steps += 3500
			exercise += 38
			hrv -= 6
			rhr += 4
			flags = append(flags, "sustained_load")
		case i >= 100 && i <= 106:
			sleep += 0.55
			hrv += 8
			rhr -= 3
			steps += 900
		}

		sleep = clamp(sleep, 4.2, 9.1)
		steps = clamp(steps, 1800, 16000)
		exercise = clamp(exercise, 0, 110)
		hrv = clamp(hrv, 24, 82)
		rhr = clamp(rhr, 48, 78)
		resp = clamp(resp, 14.2, 19.4)
		spo2 = clamp(spo2, 94.5, 98.8)
		vo2 = clamp(vo2, 37, 48)

		awake := clamp(0.45+(8.0-sleep)*0.08, 0.25, 1.1)
		deep := sleep * 0.19
		rem := sleep * 0.23
		core := sleep - deep - rem
		activeEnergy := 420 + steps*0.035 + exercise*3.2
		distance := steps * 0.00074
		load := (rhr-58)/5 + math.Max(0, (6500-steps)/6500) + math.Max(0, (7.1-sleep)/2)
		stressLoad := round2(load)

		profiles = append(profiles, dayProfile{
			date:             d,
			index:            i,
			sleepTotal:       round2(sleep),
			sleepDeep:        round2(deep),
			sleepREM:         round2(rem),
			sleepCore:        round2(core),
			sleepAwake:       round2(awake),
			sleepUnspecified: 0,
			steps:            math.Round(steps),
			activeEnergy:     round2(activeEnergy),
			basalEnergy:      round2(1580 + 40*math.Sin(longPhase*2*math.Pi)),
			exerciseMin:      round2(exercise),
			distanceKM:       round2(distance),
			hrv:              round2(hrv),
			rhr:              round2(rhr),
			spo2:             round2(spo2),
			resp:             round2(resp),
			vo2:              round2(vo2),
			wristTemp:        round2(temp),
			stressLoad:       &stressLoad,
			stressFlags:      flags,
		})
	}

	// Keep a recent stale-data patch for confidence/freshness review, but
	// leave today and the last month mostly populated for normal screenshots.
	if len(profiles) > 42 {
		for i := len(profiles) - 42; i < len(profiles)-39; i++ {
			profiles[i].stressFlags = append(profiles[i].stressFlags, "data_accruing")
		}
	}
	return profiles
}

func pointsForProfiles(profiles []dayProfile, loc *time.Location) []storage.MetricPoint {
	points := make([]storage.MetricPoint, 0, len(profiles)*22)
	for _, p := range profiles {
		date := p.date.Format(isoDate)
		sleepDate := date + " 00:00:00 " + tzOffset(p.date, loc)
		noon := date + " 12:00:00 " + tzOffset(p.date, loc)
		morning := date + " 07:30:00 " + tzOffset(p.date, loc)
		evening := date + " 19:00:00 " + tzOffset(p.date, loc)

		add := func(name, units, dt string, qty float64, source string) {
			points = append(points, storage.MetricPoint{MetricName: name, Units: units, Date: dt, Qty: qty, Source: source})
		}
		add("sleep_total", "hr", sleepDate, p.sleepTotal, sourceWatch)
		add("night_sleep_total", "hr", sleepDate, p.sleepTotal, sourceWatch)
		add("sleep_deep", "hr", sleepDate, p.sleepDeep, sourceWatch)
		add("sleep_rem", "hr", sleepDate, p.sleepREM, sourceWatch)
		add("sleep_core", "hr", sleepDate, p.sleepCore, sourceWatch)
		add("sleep_awake", "hr", sleepDate, p.sleepAwake, sourceWatch)
		add("step_count", "count", noon, p.steps, sourcePhone)
		add("active_energy", "kcal", noon, p.activeEnergy, sourceWatch)
		add("basal_energy_burned", "kcal", noon, p.basalEnergy, sourceWatch)
		add("apple_exercise_time", "min", noon, p.exerciseMin, sourceWatch)
		add("walking_running_distance", "km", noon, p.distanceKM, sourcePhone)
		add("time_in_daylight", "min", noon, clamp(35+0.004*p.steps, 20, 110), sourceWatch)
		add("heart_rate_variability", "ms", morning, p.hrv, sourceWatch)
		add("resting_heart_rate", "count/min", morning, p.rhr, sourceWatch)
		add("heart_rate", "count/min", morning, p.rhr+7, sourceWatch)
		add("heart_rate", "count/min", noon, p.rhr+18, sourceWatch)
		add("heart_rate", "count/min", evening, p.rhr+10, sourceWatch)
		add("blood_oxygen_saturation", "%", morning, p.spo2, sourceWatch)
		add("respiratory_rate", "br/min", morning, p.resp, sourceWatch)
		add("vo2_max", "ml/kg/min", noon, p.vo2, sourceWatch)
		add("wrist_temperature", "degC", morning, p.wristTemp, sourceWatch)
		add("walking_heart_rate_average", "count/min", noon, p.rhr+28+(10000-p.steps)/2200, sourceWatch)
	}
	return points
}

func upsertDailySyntheticFields(ctx context.Context, pool *pgxpool.Pool, profiles []dayProfile) error {
	batch := &pgx.Batch{}
	for _, p := range profiles {
		batch.Queue(`
			INSERT INTO daily_scores
				(date, hrv_avg, rhr_avg, sleep_total, sleep_deep, sleep_rem, sleep_core,
				 sleep_awake, sleep_unspecified, steps, calories, exercise_min, spo2_avg,
				 vo2_avg, resp_avg, baseline_hr_overnight, sustained_hr_load, stress_flags,
				 computed_at)
			VALUES
				($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,NOW()::TEXT)
			ON CONFLICT(date) DO UPDATE SET
				hrv_avg = EXCLUDED.hrv_avg,
				rhr_avg = EXCLUDED.rhr_avg,
				sleep_total = EXCLUDED.sleep_total,
				sleep_deep = EXCLUDED.sleep_deep,
				sleep_rem = EXCLUDED.sleep_rem,
				sleep_core = EXCLUDED.sleep_core,
				sleep_awake = EXCLUDED.sleep_awake,
				sleep_unspecified = EXCLUDED.sleep_unspecified,
				steps = EXCLUDED.steps,
				calories = EXCLUDED.calories,
				exercise_min = EXCLUDED.exercise_min,
				spo2_avg = EXCLUDED.spo2_avg,
				vo2_avg = EXCLUDED.vo2_avg,
				resp_avg = EXCLUDED.resp_avg,
				baseline_hr_overnight = EXCLUDED.baseline_hr_overnight,
				sustained_hr_load = EXCLUDED.sustained_hr_load,
				stress_flags = EXCLUDED.stress_flags,
				computed_at = EXCLUDED.computed_at`,
			p.date.Format(isoDate), p.hrv, p.rhr, p.sleepTotal, p.sleepDeep, p.sleepREM, p.sleepCore,
			p.sleepAwake, p.sleepUnspecified, p.steps, p.activeEnergy, p.exerciseMin, p.spo2,
			p.vo2, p.resp, p.rhr, p.stressLoad, p.stressFlags)
	}
	br := pool.SendBatch(ctx, batch)
	defer br.Close()
	for range profiles {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("upsert daily synthetic fields: %w", err)
		}
	}
	return br.Close()
}

func backfillEnergy(ctx context.Context, db *storage.DB, tz, from, to string) error {
	lastDone := -1
	progress, err := db.BackfillEnergyRange(ctx, tz, from, to, false, func(p storage.EnergyBackfillProgress) {
		if p.Done == lastDone {
			return
		}
		lastDone = p.Done
		if p.Done == p.Total || p.Done%10 == 0 {
			fmt.Printf("energy backfill progress: %d/%d ok=%d skipped=%d errors=%d\n", p.Done, p.Total, p.OK, p.Skipped, p.Errors)
		}
	})
	if err != nil {
		return err
	}
	fmt.Printf("energy backfill: ok=%d skipped=%d errors=%d\n", progress.OK, progress.Skipped, progress.Errors)
	return nil
}

func syncDailyEnergySnapshots(ctx context.Context, pool *pgxpool.Pool, from, to string) error {
	tag, err := pool.Exec(ctx, `
		UPDATE daily_scores ds
		SET
			energy_capacity = 100,
			energy_eod_current = es.bank,
			energy_drain = es.drain_delta,
			energy_verdict = CASE
				WHEN es.bank <= 15 THEN 'rest'
				WHEN es.bank <= 41 THEN 'active_recovery'
				WHEN es.bank >= 55 THEN 'push_hard'
				ELSE 'moderate'
			END,
			computed_at = NOW()::TEXT
		FROM energy_snapshots es
		WHERE ds.date = es.date
		  AND ds.date BETWEEN $1 AND $2
		  AND 'backfilled' = ANY(es.flags)`, from, to)
	if err != nil {
		return fmt.Errorf("sync daily energy snapshots: %w", err)
	}
	fmt.Printf("daily energy rows synced: %d\n", tag.RowsAffected())
	return nil
}

func loadCounts(ctx context.Context, pool *pgxpool.Pool) (seedCounts, error) {
	var c seedCounts
	err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM metric_points WHERE source LIKE 'ReviewSeed%'),
			(SELECT COUNT(*) FROM daily_scores),
			(SELECT COUNT(*) FROM target_snapshots),
			(SELECT COUNT(*) FROM feature_snapshots),
			(SELECT COUNT(*) FROM naive_baselines),
			(SELECT COUNT(*) FROM energy_snapshots WHERE 'backfilled' = ANY(flags)),
			(SELECT COUNT(*) FROM health_records WHERE automation_name = $1 AND received_at > now() - interval '24 hours'),
			COALESCE((SELECT MAX(date) FROM daily_scores), '')
	`, reviewRecordPayload).Scan(&c.Points, &c.DailyRows, &c.TargetRows, &c.FeatureRows, &c.BaselineRows, &c.EnergyRows, &c.RawRows24h, &c.LatestDailyDate)
	return c, err
}

func printSummary(cfg seedConfig, c seedCounts) {
	fmt.Println("review tenant seed complete")
	fmt.Printf("user=%s schema=%s latest_daily=%s\n", cfg.username, cfg.schema, c.LatestDailyDate)
	fmt.Printf("counts: metric_points=%d daily_scores=%d target_snapshots=%d feature_snapshots=%d naive_baselines=%d energy_snapshots=%d raw_rows_24h=%d\n",
		c.Points, c.DailyRows, c.TargetRows, c.FeatureRows, c.BaselineRows, c.EnergyRows, c.RawRows24h)
	fmt.Println("rollback SQL:")
	fmt.Printf("  DELETE FROM health_registry.users WHERE username = '%s';\n", cfg.username)
	fmt.Printf("  DROP SCHEMA IF EXISTS %s CASCADE;\n", pgx.Identifier{cfg.schema}.Sanitize())
}

func resolveAnchor(anchor string, loc *time.Location) (time.Time, error) {
	if strings.EqualFold(anchor, "today") {
		now := time.Now().In(loc)
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc), nil
	}
	t, err := time.ParseInLocation(isoDate, anchor, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("anchor must be YYYY-MM-DD or today: %w", err)
	}
	return t, nil
}

func tzOffset(t time.Time, loc *time.Location) string {
	_, offset := t.In(loc).Zone()
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	h := offset / 3600
	m := (offset % 3600) / 60
	return fmt.Sprintf("%s%02d%02d", sign, h, m)
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

func maskSecret(s string) string {
	if len(s) <= 10 {
		return "****"
	}
	return s[:6] + "..." + s[len(s)-4:]
}
