// Command wake_detection_probe evaluates the wake-time candidate algorithm
// against historical sleep rows. It is strictly read-only: it does not create
// tables, persist derived metrics, or send notifications.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"time"

	_ "time/tzdata"

	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL environment variable is required")
	}
	tzName := flag.String("tz", envOr("REPORT_TZ", "UTC"), "Tenant timezone")
	fromValue := flag.String("from", "", "Start date YYYY-MM-DD; default 90 days before --to")
	toValue := flag.String("to", "", "End date YYYY-MM-DD; default yesterday")
	schema := flag.String("schema", "", "Tenant schema; empty uses DATABASE_URL search_path")
	reference := flag.String("reference", "08:00", "Reference weekday wake time HH:MM")
	weekdaysOnly := flag.Bool("weekdays-only", true, "Exclude Saturday and Sunday")
	flag.Parse()

	loc, err := time.LoadLocation(*tzName)
	if err != nil {
		log.Fatalf("load timezone: %v", err)
	}
	refMin, err := parseClock(*reference)
	if err != nil {
		log.Fatal(err)
	}
	to := time.Now().In(loc).AddDate(0, 0, -1)
	if *toValue != "" {
		to, err = time.ParseInLocation("2006-01-02", *toValue, loc)
		if err != nil {
			log.Fatalf("parse --to: %v", err)
		}
	}
	from := to.AddDate(0, 0, -89)
	if *fromValue != "" {
		from, err = time.ParseInLocation("2006-01-02", *fromValue, loc)
		if err != nil {
			log.Fatalf("parse --from: %v", err)
		}
	}
	if from.After(to) {
		log.Fatal("--from must not be after --to")
	}

	ctx := context.Background()
	var db *storage.DB
	isolation, isolationErr := tenants.ParseTenantIsolationConfig(os.LookupEnv)
	if isolationErr != nil {
		log.Fatalf("parse tenant isolation config: %v", isolationErr)
	}
	if isolation.Enabled {
		if *schema == "" {
			log.Fatal("--schema is required when tenant database isolation is enabled")
		}
		reg, err := registry.New(ctx, isolation.RegistryDSN)
		if err != nil {
			log.Fatalf("open registry: %v", err)
		}
		defer reg.Close()
		user, err := reg.GetBySchema(ctx, *schema)
		if err != nil {
			log.Fatalf("resolve tenant schema: %v", err)
		}
		password, err := isolation.Credentials.Derive(user.TenantID, user.DBRole, user.DBCredentialVersion)
		if err != nil {
			log.Fatalf("derive restricted tenant credential: %v", err)
		}
		db, err = storage.NewRestrictedTenant(ctx, isolation.TenantDSNBase, user.DBRole, password, user.SchemaName)
	} else if *schema == "" {
		db, err = storage.New(ctx, dbURL)
	} else {
		db, err = storage.NewWithSchema(ctx, dbURL, *schema)
	}
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	type observation struct {
		date     string
		total    int
		detailed int
		summary  int
		source   string
	}
	var observations []observation
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		if *weekdaysOnly && (day.Weekday() == time.Saturday || day.Weekday() == time.Sunday) {
			continue
		}
		date := day.Format("2006-01-02")
		variants, err := db.WakeCandidateVariantsForDate(date, loc)
		if err != nil {
			log.Fatalf("detect %s: %v", date, err)
		}
		observation := observation{
			date:     date,
			total:    minuteOf(variants.SleepTotalEnd, loc),
			detailed: minuteOf(variants.DetailedSessionEnd, loc),
			summary:  minuteOf(variants.SummarySessionEnd, loc),
			source:   variants.SelectedSource,
		}
		observations = append(observations, observation)
		fmt.Printf("%s\ttotal=%s\tdetailed=%s\tsummary=%s\tdetailed_rows=%d\t%s\n",
			date,
			formatOptionalMinute(observation.total),
			formatOptionalMinute(observation.detailed),
			formatOptionalMinute(observation.summary),
			variants.DetailedRows,
			observation.source,
		)
	}

	if len(observations) == 0 {
		fmt.Println("summary\tobserved=0")
		return
	}
	totalMinutes := make([]int, 0, len(observations))
	detailedMinutes := make([]int, 0, len(observations))
	summaryMinutes := make([]int, 0, len(observations))
	for _, observation := range observations {
		if observation.total >= 0 {
			totalMinutes = append(totalMinutes, observation.total)
		}
		if observation.detailed >= 0 {
			detailedMinutes = append(detailedMinutes, observation.detailed)
		}
		if observation.summary >= 0 {
			summaryMinutes = append(summaryMinutes, observation.summary)
		}
	}
	printSummary("sleep_total_end", totalMinutes, len(observations), refMin, *reference)
	printSummary("detailed_session_end", detailedMinutes, len(observations), refMin, *reference)
	printSummary("summary_sleep_plus_awake", summaryMinutes, len(observations), refMin, *reference)
}

func parseClock(value string) (int, error) {
	parsed, err := time.Parse("15:04", value)
	if err != nil {
		return 0, fmt.Errorf("parse --reference: %w", err)
	}
	return parsed.Hour()*60 + parsed.Minute(), nil
}

func percentile(sorted []int, p float64) int {
	if len(sorted) == 0 {
		return 0
	}
	index := int(p * float64(len(sorted)-1))
	return sorted[index]
}

func formatMinute(minute int) string {
	return fmt.Sprintf("%02d:%02d", minute/60, minute%60)
}

func minuteOf(value time.Time, loc *time.Location) int {
	if value.IsZero() {
		return -1
	}
	local := value.In(loc)
	return local.Hour()*60 + local.Minute()
}

func formatOptionalMinute(minute int) string {
	if minute < 0 {
		return "-"
	}
	return formatMinute(minute)
}

func printSummary(name string, minutes []int, attempted, refMin int, reference string) {
	if len(minutes) == 0 {
		fmt.Printf("summary\tvariant=%s\tobserved=0\tmissing=%d\treference=%s\n", name, attempted, reference)
		return
	}
	sort.Ints(minutes)
	within30, within60 := 0, 0
	for _, minute := range minutes {
		delta := minute - refMin
		if abs(delta) <= 30 {
			within30++
		}
		if abs(delta) <= 60 {
			within60++
		}
	}
	fmt.Printf(
		"summary\tvariant=%s\tobserved=%d\tmissing=%d\tp25=%s\tmedian=%s\tp75=%s\twithin_30m=%d/%d\twithin_60m=%d/%d\treference=%s\n",
		name, len(minutes), attempted-len(minutes),
		formatMinute(percentile(minutes, 0.25)),
		formatMinute(percentile(minutes, 0.50)),
		formatMinute(percentile(minutes, 0.75)),
		within30, len(minutes), within60, len(minutes), reference,
	)
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
