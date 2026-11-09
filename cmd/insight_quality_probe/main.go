// insight_quality_probe reconstructs derived review material without serving,
// schema changes, or tenant writes. Provider calls require the explicit
// evaluate-morning flag and separate authorization for the supplied evidence.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"health-receiver/internal/ai"
	"health-receiver/internal/health"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

type reviewMaterial struct {
	Version    string                       `json:"version"`
	Briefing   *health.BriefingResponse     `json:"briefing"`
	Current    ai.AIInsightCorpusCase       `json:"current"`
	Sleep      health.MorningReportSleep    `json:"morning_sleep"`
	Readiness  health.MorningSleepReadiness `json:"episode_readiness"`
	Wake       map[string]any               `json:"wake_diagnostics"`
	Historical []ai.AIInsightCorpusCase     `json:"historical"`
}

func main() {
	schema := flag.String("schema", "", "explicit active tenant schema")
	out := flag.String("out", "", "new private review JSON under /tmp")
	morning := flag.Bool("evaluate-morning", false, "make three explicitly requested provider-only morning evaluations; no cache or delivery writes")
	prepare := flag.Bool("prepare-morning", false, "save the provider-facing morning packet without provider calls")
	packet := flag.String("morning-evidence", "", "explicit saved current-day packet for a morning evaluation")
	runs := flag.Int("morning-runs", 3, "number of authorized morning evaluations, one to three")
	flag.Parse()
	if *runs < 1 || *runs > 3 {
		log.Fatal("morning-runs must be one to three")
	}
	if *morning && *prepare {
		log.Fatal("choose preparation or evaluation")
	}
	if *packet != "" && !*morning {
		log.Fatal("saved evidence requires evaluate-morning")
	}
	if *schema == "" || *out == "" {
		log.Fatal("--schema and --out are required")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(*out))
	if err != nil || !(parent == "/tmp" || strings.HasPrefix(parent, "/tmp/") || parent == "/private/tmp" || strings.HasPrefix(parent, "/private/tmp/")) {
		log.Fatal("output must be in a temporary directory")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	db, err := openReadOnlyReviewSource(ctx, *schema)
	if err != nil {
		log.Fatal("read-only review connection failed")
	}
	defer db.Close()
	if err = db.VerifyHistoricalDailyInsightReadAccess(ctx); err != nil {
		log.Fatal("derived review tables unavailable")
	}
	b, err := db.GetHealthBriefing("ru")
	if err != nil || b == nil {
		log.Fatal("briefing unavailable")
	}
	if *prepare {
		evidence, err := db.BuildMorningInsightReviewEvidence(ctx, b, db.Today(), "ru")
		if err != nil {
			log.Fatal("morning review evidence unavailable")
		}
		f, err := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			log.Fatal("cannot create private morning packet")
		}
		defer f.Close()
		if err = json.NewEncoder(f).Encode(evidence); err != nil {
			log.Fatal("cannot encode morning packet")
		}
		fmt.Println("saved morning evidence; no provider calls, cache or delivery writes")
		return
	}
	if *morning {
		evaluateMorning(ctx, db, b, *out, *packet, *runs)
		return
	}
	current, err := db.BuildTodayInsightSnapshot(ctx, "ru", time.Now())
	if err != nil || current == nil {
		log.Fatal("current insight unavailable")
	}
	sleep, _, err := db.MorningReportEvidence(ctx, storage.AIConfig{}, "ru", db.Today(), b)
	if err != nil {
		log.Fatal("morning evidence unavailable")
	}
	result := reviewMaterial{Version: "insight-quality-material-v1", Briefing: b, Current: reviewCase(current, "ru", "current-review"), Sleep: sleep}
	result.Readiness, _ = db.MorningSleepReadiness(ctx, db.Today(), time.Now())
	location, _ := time.LoadLocation(db.GetNotifyConfig(storage.NotifyConfig{}).Timezone)
	if location == nil {
		location = time.UTC
	}
	candidate, _ := db.WakeCandidateForDate(db.Today(), location)
	result.Wake = map[string]any{"candidate_wake": candidate.CandidateWake, "latest_ingest": candidate.LatestIngest, "signal": candidate.Signal}
	if saved, e := db.GetDerivedMetric(storage.DerivedMetricWakeTime, db.Today()); e == nil {
		var meta map[string]any
		_ = json.Unmarshal(saved.Metadata, &meta)
		result.Wake["saved_wake"] = saved.ValueTimestamp
		result.Wake["full_hash_matches_saved"] = candidate.InputsHash == saved.InputsHash
		result.Wake["saved_input_changed_at"] = meta["sleep_inputs_changed_at"]
		result.Wake["saved_confidence"] = meta["confidence"]
	}
	day, err := time.Parse("2006-01-02", db.Today())
	if err != nil {
		log.Fatal("invalid local date")
	}
	for offset := 1; offset <= 5; offset++ {
		for _, lang := range []string{"en", "ru", "sr"} {
			snapshot, e := db.BuildHistoricalDailyInsightSnapshot(ctx, day.AddDate(0, 0, -offset).Format("2006-01-02"), lang)
			if e != nil || snapshot == nil {
				log.Fatal("historical derived review unavailable")
			}
			result.Historical = append(result.Historical, reviewCase(snapshot, lang, fmt.Sprintf("observed-%02d-%s", offset, lang)))
		}
	}
	f, e := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		log.Fatal("cannot create private review file")
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if e = enc.Encode(result); e != nil {
		log.Fatal("cannot encode review material")
	}
	fmt.Printf("saved current report and %d historical derived cases; no provider or tenant writes\n", len(result.Historical))
}

func evaluateMorning(ctx context.Context, db *storage.DB, b *health.BriefingResponse, out, packet string, runs int) {
	if b.Date != db.Today() {
		log.Fatal("current-day briefing required")
	}
	var evidence health.MorningInsightEvidence
	if packet == "" {
		var err error
		evidence, err = db.BuildMorningInsightReviewEvidence(ctx, b, db.Today(), "ru")
		if err != nil {
			log.Fatal("morning review evidence unavailable")
		}
	} else {
		data, err := os.ReadFile(packet)
		if err != nil || json.Unmarshal(data, &evidence) != nil || evidence.Date != db.Today() || evidence.NightSleep == nil || evidence.NightSleep.ReportDate != evidence.Date {
			log.Fatal("saved current-day morning evidence unavailable")
		}
	}
	health.NormalizeMorningInsightEvidence(&evidence, "ru")
	encoded, err := json.Marshal(evidence)
	if err != nil {
		log.Fatal("invalid review evidence")
	}
	isolation, err := tenants.ParseTenantIsolationConfig(os.LookupEnv)
	if err != nil {
		log.Fatal("invalid isolated environment")
	}
	reg, err := registry.NewWithExpectedIdentity(ctx, isolation.RegistryDSN, tenants.DatabaseRegistryRole)
	if err != nil {
		log.Fatal("provider registry unavailable")
	}
	defer reg.Close()
	settings, err := reg.LoadAllGlobalSettings(ctx)
	if err != nil {
		log.Fatal("provider config unavailable")
	}
	// Parse only the active provider configuration; credentials never enter output.
	cfg := storage.AIConfig{Provider: settings["ai_provider"], Providers: map[string]storage.AIProviderSettings{}}
	for _, d := range ai.ProviderDescriptors() {
		cfg.SetSettingsFor(d.ID, storage.AIProviderSettings{APIKey: settings[d.ID+"_api_key"], Model: settings[d.ID+"_model"], ReasoningEffort: settings[d.ID+"_reasoning_effort"]})
	}
	fmt.Sscan(settings["ai_max_output_tokens"], &cfg.MaxOutputTokens)
	provider, active, err := storage.ResolveTodayInsightsB1ProviderConfig(cfg)
	if err != nil || active.APIKey == "" {
		log.Fatal("active provider unavailable")
	}
	if active.MaxOutputTokens <= 0 || active.MaxOutputTokens > ai.SynthesisMaxTokens {
		active.MaxOutputTokens = ai.SynthesisMaxTokens
	}
	type run struct {
		Blocks       map[string]string `json:"blocks,omitempty"`
		Invalid      map[string]string `json:"invalid,omitempty"`
		Failed       bool              `json:"failed"`
		InputTokens  int64             `json:"input_tokens"`
		OutputTokens int64             `json:"output_tokens"`
	}
	result := struct {
		Version          string                        `json:"version"`
		Model            string                        `json:"model"`
		Reasoning        string                        `json:"reasoning"`
		Prompt           string                        `json:"prompt_revision"`
		MaxOutputTokens  int                           `json:"max_output_tokens"`
		EvidenceHash     string                        `json:"evidence_hash"`
		Evidence         health.MorningInsightEvidence `json:"evidence"`
		ProviderEvidence json.RawMessage               `json:"provider_evidence"`
		Runs             []run                         `json:"runs"`
	}{Version: "morning-quality-evaluation-v1", Model: active.Model, Reasoning: active.ReasoningEffort, Prompt: ai.PromptRevision, MaxOutputTokens: active.MaxOutputTokens, EvidenceHash: ai.HashInsightBundle(evidence), Evidence: evidence}
	result.ProviderEvidence, err = ai.MorningProviderPayload(evidence)
	if err != nil {
		log.Fatal("provider packet unavailable")
	}
	for i := 0; i < runs; i++ {
		bounded, cancel := context.WithTimeout(ctx, 90*time.Second)
		generated, e := ai.GenerateInsightBundle(bounded, provider, active, encoded, "ru")
		cancel()
		result.Runs = append(result.Runs, run{Blocks: generated.Blocks, Invalid: generated.InvalidBlocks, Failed: e != nil, InputTokens: generated.InputTokens, OutputTokens: generated.OutputTokens})
		log.Printf("morning evaluation %d/%d complete valid=%v", i+1, runs, e == nil)
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		log.Fatal("cannot create private morning review")
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err = enc.Encode(result); err != nil {
		log.Fatal("cannot encode morning review")
	}
	fmt.Printf("saved %d morning provider evaluations; no cache or delivery writes\n", runs)
}

func reviewCase(snapshot *health.DailyInsightSnapshot, lang, id string) ai.AIInsightCorpusCase {
	primary := snapshot.Primary
	primary.Narrative = nil
	primary.EvidenceIDs = []string{"primary"}
	domains := append([]health.DailyInsightDomain(nil), snapshot.Domains...)
	for i := range domains {
		domains[i].AsOf = nil
		domains[i].AIInsight = nil
		domains[i].Insight.Narrative = nil
		domains[i].Insight.EvidenceIDs = []string{domains[i].Key}
	}
	facts := append([]health.DailyInsightNarrativeFact(nil), snapshot.NarrativeFacts...)
	for i := range facts {
		facts[i].EvidenceIDs = []string{facts[i].ID}
	}
	return ai.AIInsightCorpusCase{ID: id, Locale: lang, Origin: "observed_aggregate", Tags: []string{"agreement"}, Primary: primary, Domains: domains, Facts: facts}
}

func openReadOnlyReviewSource(ctx context.Context, schema string) (*storage.DB, error) {
	cfg, err := tenants.ParseTenantIsolationConfig(os.LookupEnv)
	if err != nil || !cfg.Enabled {
		return nil, fmt.Errorf("isolated environment required")
	}
	reg, err := registry.NewWithExpectedIdentity(ctx, cfg.RegistryDSN, tenants.DatabaseRegistryRole)
	if err != nil {
		return nil, err
	}
	defer reg.Close()
	u, err := reg.GetBySchema(ctx, schema)
	if err != nil || u == nil || !u.DBIsolationReady || u.ProvisioningState != registry.ProvisioningStateActive || u.TenantID == uuid.Nil || u.DBRole != tenants.TenantRoleName(u.TenantID) || u.SchemaName != schema {
		return nil, fmt.Errorf("active isolated identity required")
	}
	password, err := cfg.Credentials.Derive(u.TenantID, u.DBRole, u.DBCredentialVersion)
	if err != nil {
		return nil, err
	}
	poolCfg, err := storage.RestrictedTenantPoolConfig(cfg.TenantDSNBase, u.DBRole, password, schema)
	if err != nil {
		return nil, err
	}
	poolCfg.MaxConns = 2
	poolCfg.MinConns = 0
	poolCfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	poolCfg.ConnConfig.RuntimeParams["statement_timeout"] = "20000"
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, err
	}
	fail := func(e error) (*storage.DB, error) { pool.Close(); return nil, e }
	var sessionUser, currentUser, currentSchema, readOnly string
	if err = pool.QueryRow(ctx, `SELECT session_user,current_user,current_schema(),current_setting('default_transaction_read_only')`).Scan(&sessionUser, &currentUser, &currentSchema, &readOnly); err != nil {
		return fail(err)
	}
	if sessionUser != u.DBRole || currentUser != u.DBRole || currentSchema != schema || readOnly != "on" {
		return fail(fmt.Errorf("read-only tenant identity mismatch"))
	}
	// This offline reader supports the existing schema while testing new pure
	// logic. Its permanent identity must still match the active registry exactly;
	// no migration/Ensure path or request-serving pool is opened here.
	var tenantID uuid.UUID
	var version int
	var checksum string
	if err = pool.QueryRow(ctx, `SELECT tenant_id,schema_contract_version,schema_contract_checksum FROM __tenant_identity`).Scan(&tenantID, &version, &checksum); err != nil {
		return fail(err)
	}
	if tenantID != u.TenantID || version != u.SchemaContractVersion || checksum != u.SchemaContractChecksum {
		return fail(fmt.Errorf("permanent tenant identity mismatch"))
	}
	return storage.NewFromPool(pool), nil
}
