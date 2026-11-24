package api

import (
	"time"

	"health-receiver/internal/ai"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
)

type ImportStatus struct {
	Running          bool   `json:"running"`
	Done             bool   `json:"done"`
	Parsed           int64  `json:"parsed"`
	Inserted         int64  `json:"inserted"`
	Skipped          int64  `json:"skipped"`
	WorkoutsParsed   int64  `json:"workouts_parsed"`
	WorkoutsUpserted int64  `json:"workouts_upserted"`
	WorkoutsFailed   int64  `json:"workouts_failed"`
	BytesRead        int64  `json:"bytes_read"`
	TotalBytes       int64  `json:"total_bytes"`
	ElapsedSec       int    `json:"elapsed_sec"`
	StartedAt        string `json:"started_at,omitempty"`
	Err              string `json:"error,omitempty"`
}

type EnergyBackfillStatus struct {
	Running    bool                           `json:"running"`
	Done       bool                           `json:"done"`
	Progress   storage.EnergyBackfillProgress `json:"progress"`
	ElapsedSec int                            `json:"elapsed_sec"`
	StartedAt  string                         `json:"started_at,omitempty"`
	Err        string                         `json:"error,omitempty"`
}

type EnergyBackfillSummary struct {
	CompleteDailyScores int    `json:"complete_daily_scores"`
	BackfilledSnapshots int    `json:"backfilled_snapshots"`
	EarliestComplete    string `json:"earliest_complete,omitempty"`
	YesterdayInTZ       string `json:"yesterday_in_tz,omitempty"`
	TZ                  string `json:"tz,omitempty"`
}

type AdminAISettingsRequest struct {
	Provider        string `json:"provider"`
	APIKey          string `json:"api_key,omitempty"`
	ClearAPIKey     bool   `json:"clear_api_key,omitempty"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	MaxOutputTokens int    `json:"max_output_tokens"`
}

type TodayInsightsB1QualityGateRequest struct {
	Corpus     ai.DailyInsightNarrativeCorpus           `json:"corpus"`
	Evaluation ai.DailyInsightNarrativeEvaluationOutput `json:"evaluation"`
}

type StatusMessage struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type DateBoundsRequest struct {
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

type CacheBackfillResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Schema  string `json:"schema"`
}

type MetricGapsResponse struct {
	Gaps []storage.DataGap `json:"gaps"`
}

type QualityAuditResponse struct {
	Entries []storage.QualityAuditEntry `json:"entries"`
	Weekly  storage.WeeklyQualityStats  `json:"weekly"`
}

type QualityFixResponse struct {
	ImpossibleFlagged int            `json:"impossible_flagged"`
	SuspectFlagged    int            `json:"suspect_flagged"`
	PerMetric         map[string]int `json:"per_metric"`
}

type QualityDigestResponse struct {
	OK bool `json:"ok"`
}

type CoverageUpdateRequest struct {
	EnabledSince string `json:"enabled_since,omitempty"`
}

type WebhookStatusResponse struct {
	State       string     `json:"state"`
	Reason      string     `json:"reason"`
	Description *string    `json:"description,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at"`
}

type WebhookRetryResponse struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type AIModelsRequest struct {
	Provider string `json:"provider"`
	APIKey   string `json:"api_key,omitempty"`
}

type AIModelsResponse struct {
	Models []ai.Model `json:"models"`
}

type AIProviderSettings struct {
	ID                string   `json:"id"`
	DisplayName       string   `json:"display_name"`
	DefaultModel      string   `json:"default_model"`
	SupportsReasoning bool     `json:"supports_reasoning"`
	ReasoningEfforts  []string `json:"reasoning_efforts"`
	Placeholder       string   `json:"api_key_placeholder"`
	Configured        bool     `json:"configured"`
	Model             string   `json:"model"`
	ReasoningEffort   string   `json:"reasoning_effort"`
}

type AdminAISettingsResponse struct {
	Provider        string               `json:"provider"`
	MaxOutputTokens int                  `json:"max_output_tokens"`
	Enabled         bool                 `json:"enabled"`
	Providers       []AIProviderSettings `json:"providers"`
}

type AdminEnergySettingsResponse struct {
	Beta               float64 `json:"energy.beta"`
	ZThreshold         float64 `json:"energy.z_threshold"`
	StressDrainEnabled bool    `json:"energy.stress_drain_enabled"`
	EffectiveBeta      float64 `json:"effective_beta"`
	Schema             string  `json:"schema"`
}

type TodayInsightsSettings struct {
	Schema    string `json:"schema"`
	B0Enabled bool   `json:"b0_enabled"`
	B1Enabled bool   `json:"b1_enabled"`
	Preview   bool   `json:"b1_preview_enabled"`
	Mode      string `json:"b1_narrative_mode"`
	Approved  bool   `json:"b1_quality_gate_approved"`
	Matches   bool   `json:"b1_quality_gate_matches_active_ai"`
}

type QualityGateResponse struct {
	Schema   string                                     `json:"schema"`
	Approval storage.TodayInsightsB1QualityGateApproval `json:"approval"`
	Gate     ai.DailyInsightNarrativeQualityGate        `json:"quality_gate"`
}

type AdminUser struct {
	Username            string                     `json:"username"`
	SchemaName          string                     `json:"schema_name"`
	APIKey              string                     `json:"api_key_masked"`
	Email               string                     `json:"email,omitempty"`
	IsAdmin             bool                       `json:"is_admin"`
	ProvisioningState   registry.ProvisioningState `json:"provisioning_state"`
	TenantID            string                     `json:"tenant_id,omitempty"`
	DBRole              string                     `json:"db_role,omitempty"`
	DBCredentialVersion int                        `json:"db_credential_version,omitempty"`
}

type AdminUsersResponse struct {
	Users []AdminUser `json:"users"`
}

type AdminNewUserResponse struct {
	Status string        `json:"status"`
	User   registry.User `json:"user"`
}

type AdminUserKey struct {
	Username string `json:"username"`
	APIKey   string `json:"api_key"`
}

type AdminCreateUser struct {
	Username   string `json:"username"`
	Password   string `json:"password,omitempty"`
	Email      string `json:"email,omitempty"`
	SchemaName string `json:"SchemaName,omitempty"`
	IsAdmin    bool   `json:"IsAdmin,omitempty"`
}

type ChronicLoadSettings struct {
	Schema string                          `json:"schema"`
	Config storage.ChronicLoadConfigStatus `json:"chronic_load_config"`
}

type ReadinessBackfillResult struct {
	Written int    `json:"written"`
	Error   string `json:"error,omitempty"`
}

type ReadinessBackfillResponse struct {
	Schema string                             `json:"schema"`
	From   string                             `json:"from"`
	To     string                             `json:"to"`
	Days   int                                `json:"days"`
	Force  bool                               `json:"force"`
	Config storage.ChronicLoadConfigStatus    `json:"chronic_load_config"`
	Scores map[string]ReadinessBackfillResult `json:"sub_scores"`
	Health storage.RedesignStorageStatus      `json:"schema_health"`
}

type ChipRecompute struct {
	Tenant  string                                   `json:"tenant"`
	Results []storage.ChipCalibrationRecomputeResult `json:"results"`
}

type ChipCalibrationResponse struct {
	Tenants []string             `json:"tenants"`
	Rows    []ChipCalibrationRow `json:"rows"`
	Results []ChipRecompute      `json:"recompute_results,omitempty"`
}

type OperationalReport struct {
	From    string           `json:"from"`
	To      string           `json:"to"`
	Days    int              `json:"days"`
	Tenants []string         `json:"tenants"`
	Rows    []OperationalRow `json:"rows"`
}

type MonitoringReport struct {
	Tenants []string        `json:"tenants"`
	Rows    []MonitoringRow `json:"rows"`
}

type ChipCalibrationRow struct {
	Tenant string `json:"tenant"`
	storage.ChipCalibration
}

type OperationalRow struct {
	Tenant string `json:"tenant"`
	storage.OperationalContractRow
}

type MonitoringRow struct {
	Tenant string `json:"tenant"`
	*storage.ReadinessMonitoringSummary
}
