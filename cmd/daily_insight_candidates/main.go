// daily_insight_candidates materializes anonymized B1 review candidates from
// retained aggregate state. It is an offline, read-only utility: it never
// calls a provider, creates a bundle, writes a tenant setting, or reads raw
// health_records payloads.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"health-receiver/internal/ai"
	"health-receiver/internal/storage"
)

const candidateExportVersion = "daily-insight-narrative-candidates-v1"

func main() {
	from := flag.String("from", "", "inclusive historical start date (YYYY-MM-DD)")
	to := flag.String("to", "", "inclusive historical end date (YYYY-MM-DD)")
	locales := flag.String("locales", "en,ru,sr", "comma-separated locales from en,ru,sr")
	maxCandidates := flag.Int("max", 90, "maximum anonymized candidates to write (1..180)")
	includeCheckinPresence := flag.Bool("include-checkin-presence", false, "include only answered/absent optional check-in provenance")
	schema := flag.String("schema", "health", "tenant schema to read")
	out := flag.String("out", "", "output path outside the repository")
	flag.Parse()

	if *from == "" || *to == "" || *out == "" {
		log.Fatal("--from, --to and --out are required")
	}
	if *maxCandidates < 1 || *maxCandidates > 180 {
		log.Fatal("--max must be between 1 and 180")
	}
	start, end := parseDateRange(*from, *to)
	selectedLocales := parseLocales(*locales)
	if !regexp.MustCompile(`^[a-z][a-z0-9_]*$`).MatchString(*schema) {
		log.Fatal("--schema must be a lowercase PostgreSQL identifier")
	}
	// pgx accepts the standard PGHOST/PGPORT/PGDATABASE/PGUSER environment
	// variables when its connection string is empty. That keeps this offline
	// tool compatible with a read-only local DB profile without printing or
	// materializing credentials into a command argument.
	dsn := os.Getenv("DATABASE_URL")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	db, err := storage.NewWithSchema(ctx, dsn, *schema)
	if err != nil {
		log.Fatalf("open read-only candidate source: %v", err)
	}
	defer db.Close()

	export := ai.DailyInsightNarrativeCandidateExport{Version: candidateExportVersion, Failures: []ai.DailyInsightNarrativeCandidateGap{}}
	allCandidates := make([]ai.DailyInsightNarrativeCandidate, 0)
	index := 0
	for day := end; !day.Before(start); day = day.AddDate(0, 0, -1) {
		for _, locale := range selectedLocales {
			index++
			candidateID := fmt.Sprintf("candidate-%03d", index)
			snapshot, err := db.BuildHistoricalDailyInsightSnapshot(ctx, day.Format("2006-01-02"), locale)
			if err != nil {
				export.Failures = append(export.Failures, ai.DailyInsightNarrativeCandidateGap{CandidateID: candidateID, Reason: candidateFailureReason(err)})
				continue
			}
			item := ai.SanitizeDailyInsightNarrativeCorpusCandidate(*snapshot, locale, candidateID)
			if *includeCheckinPresence {
				// Check-ins are optional product input. The candidate retains only
				// a timely-presence marker for the review-only no_checkin scenario;
				// its answer and timestamps never leave storage. A missing optional
				// table/read leaves provenance unannotated instead of guessing absent.
				if checkin, checkinErr := db.HistoricalCheckinScenario(ctx, day.Format("2006-01-02")); checkinErr == nil {
					item.Scenario.CheckIn = checkin
				}
			}
			allCandidates = append(allCandidates, ai.DailyInsightNarrativeCandidate{
				DailyInsightNarrativeCorpusCase: item,
				ReviewHints:                     ai.DailyInsightNarrativeCandidateReviewHints(item),
			})
		}
	}
	export.Candidates = selectDiverseCandidates(allCandidates, *maxCandidates)
	encoded, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		log.Fatalf("encode candidates: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o700); err != nil {
		log.Fatalf("create output directory: %v", err)
	}
	if err := os.WriteFile(*out, append(encoded, '\n'), 0o600); err != nil {
		log.Fatalf("write candidates: %v", err)
	}
	fmt.Printf("wrote %d anonymized candidates and %d unavailable placeholders\n", len(export.Candidates), len(export.Failures))
}

// selectDiverseCandidates prevents the bounded review artifact from becoming
// an accidental "latest N" sample. It first keeps one candidate for each
// closed, locale-aware structural signature, then fills remaining slots at
// evenly distributed positions across the scan. The source date never leaves
// this function: selected candidates are renumbered with opaque IDs.
func selectDiverseCandidates(candidates []ai.DailyInsightNarrativeCandidate, limit int) []ai.DailyInsightNarrativeCandidate {
	if limit <= 0 || len(candidates) == 0 {
		return nil
	}
	if limit > len(candidates) {
		limit = len(candidates)
	}
	selected := make(map[int]struct{}, limit)
	// Reserve first/middle/last representatives before structural selection.
	// Otherwise a stream whose newest candidates all have distinct signatures
	// degenerates into an accidental latest-N corpus.
	if limit > 1 && len(candidates) > 1 {
		temporalSlots := min(limit, 3)
		for slot := 0; slot < temporalSlots; slot++ {
			selected[slot*(len(candidates)-1)/(temporalSlots-1)] = struct{}{}
		}
	}
	seenSignatures := make(map[string]struct{})
	for index, candidate := range candidates {
		if len(selected) == limit {
			break
		}
		signature := candidateStructuralSignature(candidate)
		if _, seen := seenSignatures[signature]; seen {
			continue
		}
		seenSignatures[signature] = struct{}{}
		selected[index] = struct{}{}
	}
	if len(selected) < limit && len(candidates) > 1 {
		for slot := 0; slot < limit && len(selected) < limit; slot++ {
			index := slot * (len(candidates) - 1) / (limit - 1)
			selected[index] = struct{}{}
		}
	}
	for index := range candidates {
		if len(selected) == limit {
			break
		}
		selected[index] = struct{}{}
	}
	indices := make([]int, 0, len(selected))
	for index := range selected {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	out := make([]ai.DailyInsightNarrativeCandidate, 0, len(indices))
	for index, original := range indices {
		candidate := candidates[original]
		candidate.ID = fmt.Sprintf("candidate-%03d", index+1)
		out = append(out, candidate)
	}
	return out
}

func candidateStructuralSignature(candidate ai.DailyInsightNarrativeCandidate) string {
	parts := []string{candidate.Locale, strings.Join(candidate.ReviewHints, ",")}
	for _, domain := range candidate.Snapshot.Domains {
		parts = append(parts, strings.Join([]string{
			domain.Key,
			domain.Band,
			domain.DataState,
			domain.Confidence,
			domain.Insight.State,
			domain.Insight.AnswerKind,
			domain.Insight.ClaimID,
			candidate.NarrativeSubjects[domain.Key],
		}, ":"))
	}
	return strings.Join(parts, "|")
}

func candidateFailureReason(err error) string {
	if err == nil {
		return ""
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return "storage_sqlstate_" + pgErr.Code
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "no retained metrics"):
		return "no_retained_metrics"
	case strings.Contains(message, "evaluate historical recent sleep claim"):
		return "sleep_claim_unavailable"
	case strings.Contains(message, "build historical daily insight snapshot"):
		return "snapshot_unavailable"
	default:
		return "storage_unavailable"
	}
}

func parseDateRange(from, to string) (time.Time, time.Time) {
	start, err := time.Parse("2006-01-02", from)
	if err != nil {
		log.Fatalf("invalid --from: %v", err)
	}
	end, err := time.Parse("2006-01-02", to)
	if err != nil {
		log.Fatalf("invalid --to: %v", err)
	}
	if end.Before(start) {
		log.Fatal("--to must not precede --from")
	}
	return start, end
}

func parseLocales(raw string) []string {
	seen := make(map[string]struct{})
	locales := make([]string, 0, 3)
	for _, locale := range strings.Split(raw, ",") {
		locale = strings.TrimSpace(locale)
		if locale != "en" && locale != "ru" && locale != "sr" {
			log.Fatalf("unsupported locale %q", locale)
		}
		if _, found := seen[locale]; found {
			continue
		}
		seen[locale] = struct{}{}
		locales = append(locales, locale)
	}
	if len(locales) == 0 {
		log.Fatal("--locales must contain at least one locale")
	}
	return locales
}
