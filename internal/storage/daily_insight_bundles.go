package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"time"

	"health-receiver/internal/ai"
	"health-receiver/internal/health"

	"github.com/jackc/pgx/v5"
)

// DailyInsightBundle is the durable narrative/cache state keyed by a
// tenant-local date and report language. Snapshot and narrative are separate:
// a factual update is never hidden just because generation is unavailable.
type DailyInsightBundle struct {
	Date                string
	Lang                string
	MaterialInputHash   string
	DecisionID          string
	SchemaVersion       string
	PolicyVersion       string
	PromptRevision      string
	ProviderFingerprint string
	Snapshot            json.RawMessage
	Narrative           json.RawMessage
	GenerationState     string
	LeaseHash           string
	LeaseUntil          *time.Time
	FailureCount        int
	RetryAfter          *time.Time
	UpdatedAt           time.Time
}

const (
	DailyInsightStateCold       = "cold"
	DailyInsightStateGenerating = "generating"
	DailyInsightStateReady      = "ready"
	DailyInsightStateFailed     = "failed"
	DailyInsightStateDisabled   = "disabled"
	// The generation call has a two-minute total deadline. Keep a full minute
	// of lease margin so another process cannot reclaim while provider retries
	// are still running.
	dailyInsightLeaseDuration  = 3 * time.Minute
	dailyInsightFailureBackoff = 5 * time.Minute
)

func (s *DB) EnsureDailyInsightBundlesTableContext(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS daily_insight_bundles (
			date                TEXT NOT NULL,
			lang                TEXT NOT NULL,
			material_input_hash TEXT NOT NULL,
			decision_id         TEXT NOT NULL,
			schema_version      TEXT NOT NULL,
			policy_version      TEXT NOT NULL,
			prompt_revision     TEXT NOT NULL,
			provider_fingerprint TEXT NOT NULL,
			snapshot            JSONB NOT NULL,
			narrative           JSONB,
			generation_state    TEXT NOT NULL,
			lease_hash          TEXT,
			lease_until         TIMESTAMPTZ,
			failure_count       INTEGER NOT NULL DEFAULT 0,
			retry_after         TIMESTAMPTZ,
			updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (date, lang)
		)
	`)
	return err
}

// GetDailyInsightBundle returns nil when no bundle exists yet. A present cold
// cache is returned with GenerationState=DailyInsightStateCold. Storage failures are
// returned to the caller; they must not be mistaken for an empty cache. A
// malformed narrative never discards a valid factual snapshot: it is
// withheld and reported as failed so the caller can safely regenerate it.
func (s *DB) GetDailyInsightBundle(date, lang string) (*DailyInsightBundle, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	var bundle DailyInsightBundle
	err := s.pool.QueryRow(ctx, `
		SELECT material_input_hash, decision_id, schema_version, policy_version,
		       prompt_revision, provider_fingerprint, snapshot, narrative,
		       generation_state, COALESCE(lease_hash, ''), lease_until,
		       failure_count, retry_after, updated_at
		  FROM daily_insight_bundles WHERE date=$1 AND lang=$2`, date, lang).
		Scan(&bundle.MaterialInputHash, &bundle.DecisionID, &bundle.SchemaVersion, &bundle.PolicyVersion,
			&bundle.PromptRevision, &bundle.ProviderFingerprint, &bundle.Snapshot, &bundle.Narrative,
			&bundle.GenerationState, &bundle.LeaseHash, &bundle.LeaseUntil,
			&bundle.FailureCount, &bundle.RetryAfter, &bundle.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if !json.Valid(bundle.Snapshot) {
		return nil, errors.New("invalid stored daily insight snapshot")
	}
	if len(bundle.Narrative) > 0 && !json.Valid(bundle.Narrative) {
		bundle.Narrative = nil
		if bundle.GenerationState == DailyInsightStateReady {
			bundle.GenerationState = DailyInsightStateFailed
		}
	}
	bundle.Date, bundle.Lang = date, lang
	return &bundle, nil
}

// UpsertDailyInsightSnapshot atomically replaces factual state. The matching
// material hash is retained only if it still belongs to this snapshot, making
// any previous narrative non-renderable after a decision-changing update.
func (s *DB) UpsertDailyInsightSnapshot(ctx context.Context, bundle DailyInsightBundle) error {
	if bundle.Date == "" || bundle.Lang == "" || bundle.MaterialInputHash == "" || bundle.DecisionID == "" || !json.Valid(bundle.Snapshot) {
		return errors.New("invalid daily insight snapshot")
	}
	if bundle.SchemaVersion == "" {
		bundle.SchemaVersion = health.DailyInsightSnapshotVersion
	}
	if bundle.PolicyVersion == "" {
		bundle.PolicyVersion = health.DailyInsightPolicyVersion
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO daily_insight_bundles(
			date, lang, material_input_hash, decision_id, schema_version, policy_version,
			prompt_revision, provider_fingerprint, snapshot, generation_state, updated_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'cold',NOW())
		ON CONFLICT(date,lang) DO UPDATE SET
			material_input_hash=excluded.material_input_hash,
			decision_id=excluded.decision_id,
			schema_version=excluded.schema_version,
			policy_version=excluded.policy_version,
			prompt_revision=excluded.prompt_revision,
			provider_fingerprint=excluded.provider_fingerprint,
			snapshot=excluded.snapshot,
			narrative=CASE WHEN daily_insight_bundles.material_input_hash=excluded.material_input_hash
				AND daily_insight_bundles.schema_version=excluded.schema_version
				AND daily_insight_bundles.policy_version=excluded.policy_version
				AND daily_insight_bundles.prompt_revision=excluded.prompt_revision
				AND daily_insight_bundles.provider_fingerprint=excluded.provider_fingerprint
				THEN daily_insight_bundles.narrative ELSE NULL END,
			generation_state=CASE WHEN daily_insight_bundles.material_input_hash=excluded.material_input_hash
				AND daily_insight_bundles.schema_version=excluded.schema_version
				AND daily_insight_bundles.policy_version=excluded.policy_version
				AND daily_insight_bundles.prompt_revision=excluded.prompt_revision
				AND daily_insight_bundles.provider_fingerprint=excluded.provider_fingerprint
				THEN daily_insight_bundles.generation_state ELSE 'cold' END,
			lease_hash=CASE WHEN daily_insight_bundles.material_input_hash=excluded.material_input_hash
				AND daily_insight_bundles.schema_version=excluded.schema_version
				AND daily_insight_bundles.policy_version=excluded.policy_version
				AND daily_insight_bundles.prompt_revision=excluded.prompt_revision
				AND daily_insight_bundles.provider_fingerprint=excluded.provider_fingerprint
				THEN daily_insight_bundles.lease_hash ELSE NULL END,
			lease_until=CASE WHEN daily_insight_bundles.material_input_hash=excluded.material_input_hash
				AND daily_insight_bundles.schema_version=excluded.schema_version
				AND daily_insight_bundles.policy_version=excluded.policy_version
				AND daily_insight_bundles.prompt_revision=excluded.prompt_revision
				AND daily_insight_bundles.provider_fingerprint=excluded.provider_fingerprint
				THEN daily_insight_bundles.lease_until ELSE NULL END,
			failure_count=CASE WHEN daily_insight_bundles.material_input_hash=excluded.material_input_hash
				AND daily_insight_bundles.schema_version=excluded.schema_version
				AND daily_insight_bundles.policy_version=excluded.policy_version
				AND daily_insight_bundles.prompt_revision=excluded.prompt_revision
				AND daily_insight_bundles.provider_fingerprint=excluded.provider_fingerprint
				THEN daily_insight_bundles.failure_count ELSE 0 END,
			retry_after=CASE WHEN daily_insight_bundles.material_input_hash=excluded.material_input_hash
				AND daily_insight_bundles.schema_version=excluded.schema_version
				AND daily_insight_bundles.policy_version=excluded.policy_version
				AND daily_insight_bundles.prompt_revision=excluded.prompt_revision
				AND daily_insight_bundles.provider_fingerprint=excluded.provider_fingerprint
				THEN daily_insight_bundles.retry_after ELSE NULL END,
			updated_at=NOW()`,
		bundle.Date, bundle.Lang, bundle.MaterialInputHash, bundle.DecisionID, bundle.SchemaVersion,
		bundle.PolicyVersion, bundle.PromptRevision, bundle.ProviderFingerprint, json.RawMessage(bundle.Snapshot))
	return err
}

// RefreshTodayInsightSnapshot creates the factual side of the bundle after a
// derived-state refresh. It has no provider dependency and is safe to call
// repeatedly for an unchanged decision.
func (s *DB) RefreshTodayInsightSnapshot(ctx context.Context, lang string) (*health.DailyInsightSnapshot, error) {
	return s.RefreshTodayInsightSnapshotWithConfig(ctx, lang, AIConfig{})
}

// BuildTodayInsightSnapshot is the single factual builder for both the
// request path and background derived-state refresh. B0 is deliberately
// applied here, before a bundle is fingerprinted or a B1 worker can see it:
// otherwise the background worker would persist a claim-less snapshot while a
// browser request constructed a different, B0-enriched one for the same day.
//
// Canonical-night lookup is additive. A transient read failure must not make
// the whole Today response unavailable; it leaves the deterministic factual
// answer ladder intact and logs the missing optional claim for operators.
func (s *DB) BuildTodayInsightSnapshot(ctx context.Context, lang string, now time.Time) (*health.DailyInsightSnapshot, error) {
	if now.IsZero() {
		now = time.Now()
	}
	briefing, err := s.GetHealthBriefing(lang)
	if err != nil {
		return nil, err
	}
	snapshot := health.BuildDailyInsightSnapshot(briefing, lang)
	if snapshot == nil {
		return nil, errors.New("today insight snapshot unavailable")
	}
	// Today Insights is intentionally today-only. Do not attach an action or a
	// finalized claim to a stale dashboard frame while ingestion is catching up.
	if snapshot.Date != s.Today() || !TodayInsightsB0Enabled(s) {
		return snapshot, nil
	}
	claim, claimErr := s.EvaluateRecentSleepBelowReference(ctx, snapshot.Date, now)
	if claimErr != nil {
		log.Printf("today insights: evaluate canonical sleep claim: %v", claimErr)
		return snapshot, nil
	}
	return health.ApplyRecentSleepBelowReference(snapshot, claim, lang), nil
}

// DailyInsightGenerationFingerprint is a secret-free fingerprint of every
// output-affecting generation setting. It deliberately includes the
// server-owned policy/action revisions and language, but never the API key.
func DailyInsightGenerationFingerprint(cfg AIConfig, lang string) string {
	if !cfg.Enabled() {
		return "disabled|" + lang + "|" + health.DailyInsightSnapshotVersion + "|" + health.DailyInsightPolicyVersion + "|" + health.DailyInsightActionCatalogVersion
	}
	provider, err := ai.GetProvider(cfg.Provider)
	model := cfg.ActiveSettings().Model
	reasoning := cfg.ActiveSettings().ReasoningEffort
	if err == nil {
		descriptor := provider.Descriptor()
		if model == "" {
			model = descriptor.DefaultModel
		}
		if reasoning == "" {
			reasoning = descriptor.DefaultReasoning
		}
	}
	maxTokens := cfg.MaxOutputTokens
	if maxTokens <= 0 || maxTokens > ai.DailyInsightMaxTokens {
		maxTokens = ai.DailyInsightMaxTokens
	}
	identity := ai.DailyInsightNarrativeCurrentReviewIdentity()
	return ai.HashForGeneration("", ai.GenerationFingerprint{
		Provider:        cfg.Provider,
		Model:           model,
		ReasoningEffort: reasoning,
		MaxOutputTokens: maxTokens,
		// The literal B1 prompt/schema fingerprint must invalidate cached prose
		// together with the model and claim-packet versions. The durable gate
		// already fails closed on the same identity; retaining it here prevents
		// an old narrative from surviving a static-contract edit.
		PromptRevision: identity.PromptRevision + "|" + identity.Fingerprint + "|" + health.DailyInsightSnapshotVersion + "|" + health.DailyInsightPolicyVersion + "|" + health.DailyInsightActionCatalogVersion + "|" + lang,
	})
}

// RefreshTodayInsightSnapshotWithConfig creates the factual side of the
// bundle and records the generation fingerprint alongside it. This keeps a
// provider/model/policy change from serving a narrative made for old rules.
func (s *DB) RefreshTodayInsightSnapshotWithConfig(ctx context.Context, lang string, aiCfg AIConfig) (*health.DailyInsightSnapshot, error) {
	snapshot, err := s.BuildTodayInsightSnapshot(ctx, lang, time.Now())
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	if err := s.UpsertDailyInsightSnapshot(ctx, DailyInsightBundle{
		Date:                snapshot.Date,
		Lang:                lang,
		MaterialInputHash:   health.DailyInsightMaterialHash(snapshot),
		DecisionID:          snapshot.DecisionID,
		SchemaVersion:       health.DailyInsightSnapshotVersion,
		PolicyVersion:       health.DailyInsightPolicyVersion,
		PromptRevision:      DailyInsightNarrativeStaticRevision(),
		ProviderFingerprint: DailyInsightGenerationFingerprint(aiCfg, lang),
		Snapshot:            payload,
	}); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// DailyInsightNarrativeStaticRevision is persisted with a factual bundle and
// exposed to the HTTP writer so every lifecycle path records the same exact
// literal prompt/schema/claim-contract identity.
func DailyInsightNarrativeStaticRevision() string {
	identity := ai.DailyInsightNarrativeCurrentReviewIdentity()
	return identity.PromptRevision + "|" + identity.Fingerprint
}

// ClaimDailyInsightGeneration leases one exact snapshot/config generation and
// returns a unique lease token. Expired leases are reclaimable after a process
// restart; an active lease is never stolen by a concurrent request. The token,
// rather than the material hash, identifies this particular worker attempt so
// a late worker cannot win after a lease is reclaimed for the same inputs.
func (s *DB) ClaimDailyInsightGeneration(ctx context.Context, date, lang, materialHash, providerFingerprint string, now time.Time) (string, error) {
	if date == "" || lang == "" || materialHash == "" {
		return "", errors.New("invalid daily insight generation claim")
	}
	leaseToken, err := newDailyInsightLeaseToken()
	if err != nil {
		return "", err
	}
	leaseUntil := now.Add(dailyInsightLeaseDuration)
	tag, err := s.pool.Exec(ctx, `
		UPDATE daily_insight_bundles
		   SET generation_state='generating', lease_hash=$4, lease_until=$5, updated_at=$6
		 WHERE date=$1 AND lang=$2 AND material_input_hash=$3
		   AND provider_fingerprint=$7
		   AND generation_state IN ('cold','failed','generating')
		   AND (lease_until IS NULL OR lease_until <= $6)
		   AND (retry_after IS NULL OR retry_after <= $6)`,
		date, lang, materialHash, leaseToken, leaseUntil, now, providerFingerprint)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() != 1 {
		return "", nil
	}
	return leaseToken, nil
}

// SaveDailyInsightNarrative is compare-and-swap. A stale provider response
// cannot overwrite a newer factual snapshot because the exact expected hash
// must still be current when the row is updated.
func (s *DB) SaveDailyInsightNarrative(ctx context.Context, date, lang, expectedHash string, narrative json.RawMessage) (bool, error) {
	return s.SaveDailyInsightNarrativeForGeneration(ctx, date, lang, expectedHash, "", "", narrative)
}

// SaveDailyInsightNarrativeForGeneration commits only while both the factual
// hash and generation fingerprint still match the claimed row, and while the
// caller still owns the exact lease token returned by Claim. The empty
// fingerprint/token are retained only for the legacy compatibility wrapper;
// new generation code must provide both.
func (s *DB) SaveDailyInsightNarrativeForGeneration(ctx context.Context, date, lang, expectedHash, providerFingerprint, leaseToken string, narrative json.RawMessage) (bool, error) {
	if !json.Valid(narrative) {
		return false, errors.New("invalid daily insight narrative")
	}
	fingerprintClause := "provider_fingerprint=$4"
	leaseClause := "lease_hash=$5"
	args := []any{date, lang, expectedHash, providerFingerprint, leaseToken, json.RawMessage(narrative)}
	if providerFingerprint == "" {
		fingerprintClause = "TRUE"
	}
	if leaseToken == "" {
		leaseClause = "(lease_hash IS NULL OR lease_hash=$3)"
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE daily_insight_bundles
		   SET narrative=$6, generation_state='ready', lease_hash=NULL, lease_until=NULL,
		       failure_count=0, retry_after=NULL, updated_at=NOW()
		 WHERE date=$1 AND lang=$2 AND material_input_hash=$3 AND `+fingerprintClause+`
		   AND `+leaseClause, args...)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// InvalidateDailyInsightNarrative drops an unreadable overlay for the exact
// current bundle and makes it eligible for a fresh lease. It deliberately
// preserves the factual snapshot and never touches a newer configuration.
func (s *DB) InvalidateDailyInsightNarrative(ctx context.Context, date, lang, materialHash, providerFingerprint string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE daily_insight_bundles
		   SET narrative=NULL, generation_state='failed', lease_hash=NULL, lease_until=NULL,
		       retry_after=NULL, updated_at=NOW()
		 WHERE date=$1 AND lang=$2 AND material_input_hash=$3
		   AND provider_fingerprint=$4 AND generation_state='ready'`,
		date, lang, materialHash, providerFingerprint)
	return err
}

// FailDailyInsightGeneration releases a claim only for the same generation
// hash, provider fingerprint, and lease token, then applies bounded backoff.
// A stale worker cannot mark a newer row as failed.
func (s *DB) FailDailyInsightGeneration(ctx context.Context, date, lang, materialHash, providerFingerprint, leaseToken string, now time.Time) (bool, error) {
	if leaseToken == "" {
		return false, errors.New("missing daily insight lease token")
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE daily_insight_bundles
		   SET generation_state='failed', lease_hash=NULL, lease_until=NULL,
		       failure_count=failure_count+1, retry_after=$5, updated_at=$6
		 WHERE date=$1 AND lang=$2 AND material_input_hash=$3
		   AND provider_fingerprint=$4 AND lease_hash=$7`,
		date, lang, materialHash, providerFingerprint, now.Add(dailyInsightFailureBackoff), now, leaseToken)
	return tag.RowsAffected() == 1, err
}

func newDailyInsightLeaseToken() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}
