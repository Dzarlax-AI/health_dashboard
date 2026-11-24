import type { Locale } from "../i18n";
import { ClientApiError } from "./client";

const METRICS_CONTRACT_HEADERS = {
  Accept: "application/json",
  "X-Health-Metrics-Contract": "2",
} as const;

const JSON_HEADERS = {
  Accept: "application/json",
} as const;

async function parseJsonResponse<T>(response: Response): Promise<T> {
  if (
    response.redirected ||
    response.headers.get("content-type")?.includes("text/html")
  ) {
    throw new ClientApiError(401, "Interactive authentication is required");
  }
  if (!response.ok) {
    let message = response.statusText;
    try {
      const text = await response.text();
      if (text.trim()) {
        try {
          const parsed = JSON.parse(text) as { error?: string; message?: string };
          message = parsed.error || parsed.message || text.trim();
        } catch {
          message = text.trim();
        }
      }
    } catch {
      // keep statusText
    }
    throw new ClientApiError(
      response.status,
      message || "Client API request failed",
    );
  }
  return (await response.json()) as T;
}

function appendSchema(params: URLSearchParams, schema?: string): void {
  const trimmed = schema?.trim();
  if (trimmed) {
    params.set("schema", trimmed);
  }
}

function withQuery(path: string, params: URLSearchParams): string {
  const qs = params.toString();
  return qs ? `${path}?${qs}` : path;
}

// ---- Unified Metrics API (Contract v2) ------------------------------------

export interface MetricCatalogItem {
  name: string;
  display_name: string;
  units: string;
  count: number;
  min: string;
  max: string;
}

export interface LatestMetricValue {
  metric: string;
  value: number;
  unit: string;
  date: string;
}

export interface MetricPoint {
  date: string;
  qty: number;
  min?: number;
  max?: number;
}

export interface MetricSourceSeries {
  source: string;
  points: MetricPoint[] | null;
}

export interface UnifiedMetricSeriesResponse {
  metric: string;
  bucket: string;
  agg: string;
  by_source?: boolean;
  points?: MetricPoint[] | null;
  points_by_source?: MetricSourceSeries[] | null;
}

export interface UnifiedMetricRangeResponse {
  min: string;
  max: string;
}

export type MetricBucket = "" | "minute" | "hour" | "day";
export type MetricAgg = "" | "AVG" | "SUM" | "MAX" | "MIN";

export interface QueryMetricDataOptions {
  from?: string;
  to: string;
  bucket?: MetricBucket;
  agg?: MetricAgg;
  bySource?: boolean;
  rangeAll?: boolean;
  view?: "quantity" | "full";
}

export async function listMetrics(
  locale: Locale,
  signal?: AbortSignal,
): Promise<MetricCatalogItem[]> {
  const params = new URLSearchParams({ lang: locale });
  const response = await fetch(withQuery("/api/metrics", params), {
    headers: METRICS_CONTRACT_HEADERS,
    signal,
  });
  return parseJsonResponse<MetricCatalogItem[]>(response);
}

export async function getLatestMetricValues(
  signal?: AbortSignal,
): Promise<LatestMetricValue[]> {
  const response = await fetch("/api/metrics/latest", {
    headers: METRICS_CONTRACT_HEADERS,
    signal,
  });
  return parseJsonResponse<LatestMetricValue[]>(response);
}

export async function queryMetricData(
  metric: string,
  options: QueryMetricDataOptions,
  signal?: AbortSignal,
): Promise<UnifiedMetricSeriesResponse> {
  const params = new URLSearchParams({ metric, to: options.to });
  if (options.rangeAll) {
    params.set("range", "all");
  } else if (options.from) {
    params.set("from", options.from);
  }
  if (options.bucket) {
    params.set("bucket", options.bucket);
  }
  if (options.agg) {
    params.set("agg", options.agg);
  }
  params.set("by_source", options.bySource ? "1" : "0");
  params.set("view", options.view ?? (options.bySource ? "full" : "quantity"));
  const response = await fetch(withQuery("/api/metrics/data", params), {
    headers: METRICS_CONTRACT_HEADERS,
    signal,
  });
  return parseJsonResponse<UnifiedMetricSeriesResponse>(response);
}

export async function queryMetricRange(
  metric: string,
  signal?: AbortSignal,
): Promise<UnifiedMetricRangeResponse> {
  const params = new URLSearchParams({ metric });
  const response = await fetch(withQuery("/api/metrics/range", params), {
    headers: METRICS_CONTRACT_HEADERS,
    signal,
  });
  return parseJsonResponse<UnifiedMetricRangeResponse>(response);
}

export interface ReadinessHistoryEntry {
  date: string;
  readiness: number | null;
  hrv_avg: number | null;
  rhr_avg: number | null;
  sleep_total: number | null;
  steps_total: number | null;
}

export async function queryReadinessHistory(
  from: string,
  to: string,
  signal?: AbortSignal,
): Promise<{ history: ReadinessHistoryEntry[] }> {
  const params = new URLSearchParams({ from, to });
  const response = await fetch(withQuery("/api/readiness-history", params), {
    headers: JSON_HEADERS,
    signal,
  });
  return parseJsonResponse<{ history: ReadinessHistoryEntry[] }>(response);
}

// ---- Settings, Import, Webhook, Energy Backfill ---------------------------

export interface NotificationSettingsResponse {
  enabled: boolean;
  is_admin?: boolean;
  preferred_sleep_source: string;
  sleep_sources: string[] | null;
  telegram_token: string;
  telegram_chat_id: string;
  report_lang: string;
  timezone: string;
  report_morning_weekday: number;
  report_morning_weekend: number;
  report_evening_weekday: number;
  report_evening_weekend: number;
  report_daytime_enabled: boolean;
  report_daytime_start_hour: number;
  report_daytime_end_hour: number;
  tenant?: string;
  username?: string;
}

export interface SettingsUpdateResponse {
  status: string;
}

export interface StatusMessage {
  status: string;
  message: string;
}

export interface WebhookStatusResponse {
  state: string;
  reason: string;
  description?: string;
  updated_at: string | null;
}

export interface WebhookRetryResponse {
  status: string;
  reason?: string;
}

export interface ImportStatus {
  running: boolean;
  done: boolean;
  parsed: number;
  inserted: number;
  skipped: number;
  workouts_parsed: number;
  workouts_upserted: number;
  workouts_failed: number;
  bytes_read: number;
  total_bytes: number;
  elapsed_sec: number;
  started_at?: string;
  error?: string;
}

export interface EnergyBackfillProgress {
  done: number;
  errors: number;
  from: string;
  ok: number;
  skipped: number;
  to: string;
  total: number;
  tz: string;
}

export interface EnergyBackfillStatus {
  running: boolean;
  done: boolean;
  progress: EnergyBackfillProgress;
  elapsed_sec: number;
  started_at?: string;
  error?: string;
}

export interface EnergyBackfillSummary {
  complete_daily_scores: number;
  backfilled_snapshots: number;
  earliest_complete?: string;
  yesterday_in_tz?: string;
  tz?: string;
}

export async function getNotificationSettings(
  signal?: AbortSignal,
): Promise<NotificationSettingsResponse> {
  const response = await fetch("/api/settings", {
    headers: JSON_HEADERS,
    signal,
  });
  return parseJsonResponse<NotificationSettingsResponse>(response);
}

export async function updateNotificationSettings(
  body: Record<string, string>,
  signal?: AbortSignal,
): Promise<SettingsUpdateResponse> {
  const response = await fetch("/api/settings", {
    method: "POST",
    headers: { ...JSON_HEADERS, "Content-Type": "application/json" },
    body: JSON.stringify(body),
    signal,
  });
  return parseJsonResponse<SettingsUpdateResponse>(response);
}

export async function sendTestNotification(
  kind: "morning" | "evening",
  signal?: AbortSignal,
): Promise<StatusMessage> {
  const params = new URLSearchParams({ kind });
  const response = await fetch(withQuery("/api/settings/test-notify", params), {
    method: "POST",
    headers: JSON_HEADERS,
    signal,
  });
  return parseJsonResponse<StatusMessage>(response);
}

export async function getWebhookStatus(
  signal?: AbortSignal,
): Promise<WebhookStatusResponse> {
  const response = await fetch("/api/webhook-status", {
    headers: JSON_HEADERS,
    signal,
  });
  return parseJsonResponse<WebhookStatusResponse>(response);
}

export async function retryWebhookRegistration(
  schema?: string,
  signal?: AbortSignal,
): Promise<WebhookRetryResponse> {
  const params = new URLSearchParams();
  appendSchema(params, schema);
  const response = await fetch(withQuery("/api/webhook-status/retry", params), {
    method: "POST",
    headers: JSON_HEADERS,
    signal,
  });
  return parseJsonResponse<WebhookRetryResponse>(response);
}

export async function uploadHealthExport(
  file: File,
  options: { batch: string; pause: string },
  signal?: AbortSignal,
): Promise<StatusMessage> {
  const params = new URLSearchParams({
    filename: file.name,
    batch: options.batch || "500",
    pause: options.pause || "150",
    size: String(file.size),
  });
  const response = await fetch(withQuery("/api/import/upload", params), {
    method: "POST",
    headers: JSON_HEADERS,
    body: file,
    signal,
  });
  return parseJsonResponse<StatusMessage>(response);
}

export async function getImportStatus(
  signal?: AbortSignal,
): Promise<ImportStatus> {
  const response = await fetch("/api/import/status", {
    headers: JSON_HEADERS,
    signal,
  });
  return parseJsonResponse<ImportStatus>(response);
}

export async function getEnergyBackfillSummary(
  signal?: AbortSignal,
): Promise<EnergyBackfillSummary> {
  const response = await fetch("/api/settings/energy-backfill/summary", {
    headers: JSON_HEADERS,
    signal,
  });
  return parseJsonResponse<EnergyBackfillSummary>(response);
}

export async function getEnergyBackfillStatus(
  signal?: AbortSignal,
): Promise<EnergyBackfillStatus> {
  const response = await fetch("/api/settings/energy-backfill/status", {
    headers: JSON_HEADERS,
    signal,
  });
  return parseJsonResponse<EnergyBackfillStatus>(response);
}

export async function startEnergyBackfill(
  body: { from?: string; to?: string } = {},
  signal?: AbortSignal,
): Promise<EnergyBackfillStatus> {
  const response = await fetch("/api/settings/energy-backfill", {
    method: "POST",
    headers: { ...JSON_HEADERS, "Content-Type": "application/json" },
    body: JSON.stringify(body),
    signal,
  });
  if (response.status === 409) {
    return (await response.json()) as EnergyBackfillStatus;
  }
  return parseJsonResponse<EnergyBackfillStatus>(response);
}

// ---- Admin API (Unified Health API) ---------------------------------------

export interface CacheTableStatus {
  rows: number;
  metrics?: number;
  oldest?: string;
  newest?: string;
}

export interface CacheStatus {
  raw_points: CacheTableStatus;
  minute_cache: CacheTableStatus;
  hourly_cache: CacheTableStatus;
  daily_scores: CacheTableStatus;
  last_sync?: string;
  score_version: number;
  telegram_enabled: boolean;
  redesign_storage: {
    healthy: boolean;
    error?: string;
  };
}

export interface CacheBackfillResponse {
  status: string;
  message: string;
  schema: string;
}

export interface MetricGap {
  from: string;
  to: string;
  days: number;
  partial?: boolean;
  today_missing?: boolean;
}

export interface MetricGapsResponse {
  gaps: MetricGap[] | null;
}

export interface QualityAuditEntry {
  metric: string;
  min: number;
  max: number;
  bad_count: number;
  total_rows: number;
  sample: number;
}

export interface WeeklyQualityStats {
  days: number;
  impossible: Record<string, number>;
  suspect: Record<string, number>;
  missed_nights: string[] | null;
  watch_off_hours: number;
}

export interface QualityAuditResponse {
  entries: QualityAuditEntry[] | null;
  weekly: WeeklyQualityStats;
}

export interface QualityFixResponse {
  impossible_flagged: number;
  suspect_flagged: number;
  per_metric: Record<string, number>;
}

export interface QualityDigestResponse {
  ok: boolean;
}

export interface CheckinCoverageSummary {
  total_days: number;
  prompted: number;
  answered: number;
  late_answered: number;
  expired: number;
  missing: number;
  pending: number;
  prompted_coverage_percent: number;
  answered_coverage_percent: number;
  average_response_seconds?: number;
  answer_counts: Record<string, number>;
}

export interface CheckinCoverageRow {
  date: string;
  status: string;
  answer?: string;
  source: string;
  prompted_at?: string;
  answered_at?: string;
  expires_at?: string;
  response_latency_seconds?: number;
}

export interface CheckinCoverage {
  days: number;
  from: string;
  to: string;
  source: string;
  enabled_since?: string;
  sla_active: boolean;
  summary: CheckinCoverageSummary;
  sla_summary: CheckinCoverageSummary;
  rows: CheckinCoverageRow[] | null;
  sla_rows: CheckinCoverageRow[] | null;
}

export interface TodayInsightsSettings {
  schema: string;
  b0_enabled: boolean;
  b1_enabled: boolean;
  b1_preview_enabled: boolean;
  b1_narrative_mode: string;
  b1_quality_gate_approved: boolean;
  b1_quality_gate_matches_active_ai: boolean;
}

export interface AIProviderSettings {
  id: string;
  display_name: string;
  default_model: string;
  supports_reasoning: boolean;
  reasoning_efforts: string[];
  api_key_placeholder: string;
  configured: boolean;
  model: string;
  reasoning_effort: string;
}

export interface AdminAISettingsResponse {
  provider: string;
  max_output_tokens: number;
  enabled: boolean;
  providers: AIProviderSettings[];
}

export interface AdminAISettingsRequest {
  provider: string;
  api_key?: string;
  clear_api_key?: boolean;
  model?: string;
  reasoning_effort?: string;
  max_output_tokens: number;
}

export interface AIModelInfo {
  id: string;
  display_name?: string;
}

export interface AIModelsResponse {
  models: AIModelInfo[];
}

export interface AdminEnergySettingsResponse {
  "energy.beta": number;
  "energy.z_threshold": number;
  "energy.stress_drain_enabled": boolean;
  effective_beta: number;
  schema: string;
}

export interface StressValidation {
  verdict: string;
  reason: string;
  days: number;
  window_days: number;
  flags?: string[] | null;
  channel_1_hrv: { n: number; r?: number; sparse?: boolean };
  channel_2_rhr: { n: number; r?: number; sparse?: boolean };
  channel_3_sleep: {
    n: number;
    agreement_votes: number;
    awake_r?: number;
    deep_pct_first_third_r?: number;
    onset_latency_r?: number;
  };
}

export interface StressObservability {
  applied: boolean;
  mode: string;
  beta: number;
  effective_beta: number;
  z_threshold: number;
  stress_drain_enabled: boolean;
  timezone: string;
  from: string;
  to: string;
  window_days: number;
  distribution: {
    days: number;
    empty: number;
    from: string;
    to: string;
    load_mean: number;
    load_median: number;
    load_p90: number;
    load_max: number;
    flag_counts: Record<string, number>;
  };
  validation: StressValidation;
}

export interface AdminUser {
  username: string;
  schema_name: string;
  api_key_masked: string;
  email?: string;
  is_admin: boolean;
  provisioning_state?: string;
  tenant_id?: string;
  db_role?: string;
  db_credential_version?: number;
}

export interface AdminUsersResponse {
  users: AdminUser[];
}

export interface AdminUserKey {
  username: string;
  api_key: string;
}

export interface AdminCreateUser {
  username: string;
  password?: string;
  email?: string;
  SchemaName?: string;
  IsAdmin?: boolean;
}

export interface AdminNewUserResponse {
  status: string;
  user: {
    Username?: string;
    username?: string;
    SchemaName?: string;
    schema_name?: string;
    APIKey?: string;
    api_key?: string;
    Email?: string;
    email?: string;
    IsAdmin?: boolean;
    is_admin?: boolean;
  };
}

export interface ChronicLoadValues {
  min_acute_density: number;
  min_breach_days: number;
}

export interface ChronicLoadConfigStatus {
  defaults: ChronicLoadValues;
  effective: ChronicLoadValues;
  raw_settings: ChronicLoadValues;
  matches_defaults: boolean;
  corrected_to_defaults: boolean;
}

export interface ChronicLoadSettings {
  schema: string;
  chronic_load_config: ChronicLoadConfigStatus;
}

export interface ReadinessBackfillResponse {
  schema: string;
  from: string;
  to: string;
  days: number;
  force: boolean;
  chronic_load_config: ChronicLoadConfigStatus;
  sub_scores: Record<string, { written: number; error?: string }>;
  schema_health: { healthy: boolean; error?: string };
}

export interface ChipCalibration {
  tenant: string;
  SubScore: string;
  TargetKind: string;
  Method: string;
  Status: string;
  SourceEpoch: string;
  CalibrationWindowDays: number;
  NEligible: number;
  NPositives: number;
  BaseRate: number | null;
  Cutoff: number | null;
  P80: number | null;
}

export interface ChipCalibrationResponse {
  tenants: string[] | null;
  rows: ChipCalibration[] | null;
  recompute_results?: Array<{
    tenant: string;
    results: Array<{
      sub_score: string;
      target_kind: string;
      error?: string;
      saved?: Omit<ChipCalibration, "tenant">;
    }> | null;
  }> | null;
}

export interface OperationalRow {
  tenant: string;
  Date: string;
  SubScore: string;
  TargetKind: string;
  BaselineKind: string;
  BaselineReason: string | null;
  CalibrationStatus: string | null;
  CurrentSourceEpoch: string | null;
  SourceEpoch: string | null;
  SourceEpochChanged: boolean;
  Cutoff: number | null;
  PredictedValue: number | null;
  TargetEligible: boolean | null;
  TargetEligibilityReason: string | null;
}

export interface OperationalReport {
  from: string;
  to: string;
  days: number;
  tenants: string[] | null;
  rows: OperationalRow[] | null;
}

export interface MonitoringCoverageRow {
  SubScore: string;
  TargetKind: string;
  WindowFrom: string;
  WindowTo: string;
  ExpectedRows: number;
  Rows: number;
  MissingRows: number;
  Eligible: number;
  EligiblePct: number;
  FloorPct: number;
  Status: string;
  TopReason: string;
  TopReasonRows: number;
  InputStalenessStatus: string;
  InputStalenessDays: number;
  InputStalenessReason: string;
  InputStableTo: string;
  ContractLagDays: number;
  ReasonCounts: Record<string, number>;
}

export interface MonitoringRow {
  tenant: string;
  AsOfDate: string;
  OverallStatus: string;
  WindowDays: number;
  BaselineDays: number;
  DriftDays: number;
  CoverageRows: MonitoringCoverageRow[] | null;
  DriftRows: Array<{
    SubScore: string;
    TargetKind: string;
    BaselineEligible: number;
    BaselinePositives: number;
    BaselineRate: number;
    RecentEligible: number;
    RecentPositives: number;
    RecentRate: number;
    Delta: number;
    Threshold: number;
    Status: string;
  }> | null;
  UnknownRateRows: Array<{
    SubScore: string;
    BaselineRows: number;
    BaselineUnknown: number;
    BaselineRate: number;
    RecentRows: number;
    RecentUnknown: number;
    RecentRate: number;
    Status: string;
  }> | null;
  SourceEpochAlerts: Array<{
    EpochID: string;
    Kind: string;
    Status: string;
    Message: string;
    StartDate: string;
    EndDate: string;
  }> | null;
}

export interface MonitoringReport {
  tenants: string[] | null;
  rows: MonitoringRow[] | null;
}

export async function getCacheStatus(
  schema?: string,
  signal?: AbortSignal,
): Promise<CacheStatus> {
  const params = new URLSearchParams();
  appendSchema(params, schema);
  const response = await fetch(withQuery("/api/admin/status", params), {
    headers: JSON_HEADERS,
    signal,
  });
  return parseJsonResponse<CacheStatus>(response);
}

export async function startCacheBackfill(
  force: boolean,
  schema?: string,
  signal?: AbortSignal,
): Promise<CacheBackfillResponse> {
  const params = new URLSearchParams({ force: force ? "1" : "0" });
  appendSchema(params, schema);
  const response = await fetch(withQuery("/api/admin/backfill", params), {
    method: "POST",
    headers: JSON_HEADERS,
    signal,
  });
  return parseJsonResponse<CacheBackfillResponse>(response);
}

export async function getMetricGaps(
  schema?: string,
  signal?: AbortSignal,
): Promise<MetricGapsResponse> {
  const params = new URLSearchParams();
  appendSchema(params, schema);
  const response = await fetch(withQuery("/api/admin/gaps", params), {
    headers: JSON_HEADERS,
    signal,
  });
  return parseJsonResponse<MetricGapsResponse>(response);
}

export async function getQualityAudit(
  schema?: string,
  signal?: AbortSignal,
): Promise<QualityAuditResponse> {
  const params = new URLSearchParams();
  appendSchema(params, schema);
  const response = await fetch(withQuery("/api/admin/quality-audit", params), {
    headers: JSON_HEADERS,
    signal,
  });
  return parseJsonResponse<QualityAuditResponse>(response);
}

export async function applyQualityFix(
  schema?: string,
  signal?: AbortSignal,
): Promise<QualityFixResponse> {
  const params = new URLSearchParams();
  appendSchema(params, schema);
  const response = await fetch(withQuery("/api/admin/quality-fix", params), {
    method: "POST",
    headers: JSON_HEADERS,
    signal,
  });
  return parseJsonResponse<QualityFixResponse>(response);
}

export async function sendQualityDigest(
  schema?: string,
  signal?: AbortSignal,
): Promise<QualityDigestResponse> {
  const params = new URLSearchParams();
  appendSchema(params, schema);
  const response = await fetch(withQuery("/api/admin/quality-digest", params), {
    method: "POST",
    headers: JSON_HEADERS,
    signal,
  });
  return parseJsonResponse<QualityDigestResponse>(response);
}

export async function getCheckinCoverage(
  days = 14,
  schema?: string,
  signal?: AbortSignal,
): Promise<CheckinCoverage> {
  const params = new URLSearchParams({ days: String(days) });
  appendSchema(params, schema);
  const response = await fetch(
    withQuery("/api/admin/checkin-coverage", params),
    { headers: JSON_HEADERS, signal },
  );
  return parseJsonResponse<CheckinCoverage>(response);
}

export async function updateCheckinCoverage(
  enabledSince: string,
  days = 14,
  schema?: string,
  signal?: AbortSignal,
): Promise<CheckinCoverage> {
  const params = new URLSearchParams({ days: String(days) });
  appendSchema(params, schema);
  const response = await fetch(
    withQuery("/api/admin/checkin-coverage", params),
    {
      method: "POST",
      headers: { ...JSON_HEADERS, "Content-Type": "application/json" },
      body: JSON.stringify({ enabled_since: enabledSince }),
      signal,
    },
  );
  return parseJsonResponse<CheckinCoverage>(response);
}

export async function getTodayInsightsConfig(
  schema?: string,
  signal?: AbortSignal,
): Promise<TodayInsightsSettings> {
  const params = new URLSearchParams();
  appendSchema(params, schema);
  const response = await fetch(
    withQuery("/api/admin/today-insights/config", params),
    { headers: JSON_HEADERS, signal },
  );
  return parseJsonResponse<TodayInsightsSettings>(response);
}

export async function updateTodayInsightsConfig(
  body: Record<string, boolean>,
  schema?: string,
  signal?: AbortSignal,
): Promise<TodayInsightsSettings> {
  const params = new URLSearchParams();
  appendSchema(params, schema);
  const response = await fetch(
    withQuery("/api/admin/today-insights/config", params),
    {
      method: "POST",
      headers: { ...JSON_HEADERS, "Content-Type": "application/json" },
      body: JSON.stringify(body),
      signal,
    },
  );
  return parseJsonResponse<TodayInsightsSettings>(response);
}

export async function getAISettings(
  signal?: AbortSignal,
): Promise<AdminAISettingsResponse> {
  const response = await fetch("/api/admin/settings", {
    headers: JSON_HEADERS,
    signal,
  });
  return parseJsonResponse<AdminAISettingsResponse>(response);
}

export async function updateAISettings(
  body: AdminAISettingsRequest,
  signal?: AbortSignal,
): Promise<SettingsUpdateResponse> {
  const response = await fetch("/api/admin/settings", {
    method: "POST",
    headers: { ...JSON_HEADERS, "Content-Type": "application/json" },
    body: JSON.stringify(body),
    signal,
  });
  return parseJsonResponse<SettingsUpdateResponse>(response);
}

export async function listProviderModels(
  provider: string,
  apiKey?: string,
  signal?: AbortSignal,
): Promise<AIModelsResponse> {
  const response = await fetch("/api/admin/ai-models", {
    method: "POST",
    headers: { ...JSON_HEADERS, "Content-Type": "application/json" },
    body: JSON.stringify({ provider, api_key: apiKey || "" }),
    signal,
  });
  return parseJsonResponse<AIModelsResponse>(response);
}

export async function getEnergySettings(
  schema?: string,
  signal?: AbortSignal,
): Promise<AdminEnergySettingsResponse> {
  const params = new URLSearchParams();
  appendSchema(params, schema);
  const response = await fetch(
    withQuery("/api/admin/energy-settings", params),
    { headers: JSON_HEADERS, signal },
  );
  return parseJsonResponse<AdminEnergySettingsResponse>(response);
}

export async function updateEnergySettings(
  body: {
    "energy.beta": string;
    "energy.z_threshold": string;
    "energy.stress_drain_enabled": string;
  },
  schema?: string,
  signal?: AbortSignal,
): Promise<SettingsUpdateResponse> {
  const params = new URLSearchParams();
  appendSchema(params, schema);
  const response = await fetch(
    withQuery("/api/admin/energy-settings", params),
    {
      method: "POST",
      headers: { ...JSON_HEADERS, "Content-Type": "application/json" },
      body: JSON.stringify(body),
      signal,
    },
  );
  return parseJsonResponse<SettingsUpdateResponse>(response);
}

export async function getStressObservability(
  windowDays = 30,
  schema?: string,
  signal?: AbortSignal,
): Promise<StressObservability> {
  const params = new URLSearchParams({ window: String(windowDays) });
  appendSchema(params, schema);
  const response = await fetch(
    withQuery("/api/admin/stress-observability", params),
    { headers: JSON_HEADERS, signal },
  );
  return parseJsonResponse<StressObservability>(response);
}

export async function getStressValidation(
  windowDays = 30,
  schema?: string,
  signal?: AbortSignal,
): Promise<StressValidation> {
  const params = new URLSearchParams({ window: String(windowDays) });
  appendSchema(params, schema);
  const response = await fetch(
    withQuery("/api/admin/stress-validation", params),
    { headers: JSON_HEADERS, signal },
  );
  return parseJsonResponse<StressValidation>(response);
}

export async function listUsers(
  signal?: AbortSignal,
): Promise<AdminUsersResponse> {
  const response = await fetch("/api/admin/users", {
    headers: JSON_HEADERS,
    signal,
  });
  return parseJsonResponse<AdminUsersResponse>(response);
}

export async function createUser(
  body: AdminCreateUser,
  signal?: AbortSignal,
): Promise<AdminNewUserResponse> {
  const response = await fetch("/api/admin/users", {
    method: "POST",
    headers: { ...JSON_HEADERS, "Content-Type": "application/json" },
    body: JSON.stringify(body),
    signal,
  });
  return parseJsonResponse<AdminNewUserResponse>(response);
}

export async function getUserAPIKey(
  username: string,
  signal?: AbortSignal,
): Promise<AdminUserKey> {
  const response = await fetch(`/api/admin/users/${encodeURIComponent(username)}/api-key`, {
    headers: JSON_HEADERS,
    signal,
  });
  return parseJsonResponse<AdminUserKey>(response);
}

export async function getReadinessOperationalContract(
  days = 14,
  schema?: string,
  signal?: AbortSignal,
): Promise<OperationalReport> {
  const params = new URLSearchParams({ days: String(days) });
  appendSchema(params, schema);
  const response = await fetch(
    withQuery("/api/admin/readiness-redesign/operational-contract", params),
    { headers: JSON_HEADERS, signal },
  );
  return parseJsonResponse<OperationalReport>(response);
}

export async function getReadinessMonitoring(
  schema?: string,
  signal?: AbortSignal,
): Promise<MonitoringReport> {
  const params = new URLSearchParams();
  appendSchema(params, schema);
  const response = await fetch(
    withQuery("/api/admin/readiness-redesign/monitoring", params),
    { headers: JSON_HEADERS, signal },
  );
  return parseJsonResponse<MonitoringReport>(response);
}

export async function getReadinessConfig(
  schema?: string,
  signal?: AbortSignal,
): Promise<ChronicLoadSettings> {
  const params = new URLSearchParams();
  appendSchema(params, schema);
  const response = await fetch(
    withQuery("/api/admin/readiness-redesign/config", params),
    { headers: JSON_HEADERS, signal },
  );
  return parseJsonResponse<ChronicLoadSettings>(response);
}

export async function updateReadinessConfig(
  body: {
    "chronic_load.min_acute_density"?: number;
    "chronic_load.min_breach_days"?: number;
  },
  schema?: string,
  signal?: AbortSignal,
): Promise<ChronicLoadSettings> {
  const params = new URLSearchParams();
  appendSchema(params, schema);
  const response = await fetch(
    withQuery("/api/admin/readiness-redesign/config", params),
    {
      method: "POST",
      headers: { ...JSON_HEADERS, "Content-Type": "application/json" },
      body: JSON.stringify(body),
      signal,
    },
  );
  return parseJsonResponse<ChronicLoadSettings>(response);
}

export async function backfillReadiness(
  options: { from: string; to: string; subScore?: string; force?: boolean },
  schema?: string,
  signal?: AbortSignal,
): Promise<ReadinessBackfillResponse> {
  const params = new URLSearchParams({
    from: options.from,
    to: options.to,
    force: options.force ? "1" : "0",
  });
  if (options.subScore && options.subScore !== "all") {
    params.set("sub_score", options.subScore);
  }
  appendSchema(params, schema);
  const response = await fetch(
    withQuery("/api/admin/readiness-redesign/backfill", params),
    {
      method: "POST",
      headers: JSON_HEADERS,
      signal,
    },
  );
  return parseJsonResponse<ReadinessBackfillResponse>(response);
}

export async function listChipCalibrations(
  schema?: string,
  signal?: AbortSignal,
): Promise<ChipCalibrationResponse> {
  const params = new URLSearchParams();
  appendSchema(params, schema);
  const response = await fetch(
    withQuery("/api/admin/readiness-redesign/chip-calibrations", params),
    { headers: JSON_HEADERS, signal },
  );
  return parseJsonResponse<ChipCalibrationResponse>(response);
}

export async function recomputeChipCalibrations(
  schema?: string,
  signal?: AbortSignal,
): Promise<ChipCalibrationResponse> {
  const params = new URLSearchParams();
  appendSchema(params, schema);
  const response = await fetch(
    withQuery("/api/admin/readiness-redesign/chip-calibrations", params),
    {
      method: "POST",
      headers: JSON_HEADERS,
      signal,
    },
  );
  return parseJsonResponse<ChipCalibrationResponse>(response);
}
