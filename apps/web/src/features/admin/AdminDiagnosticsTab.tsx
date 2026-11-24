import { useCallback, useEffect, useState } from "react";

import {
  applyQualityFix,
  getCacheStatus,
  getCheckinCoverage,
  getMetricGaps,
  getQualityAudit,
  getTodayInsightsConfig,
  sendQualityDigest,
  startCacheBackfill,
  updateCheckinCoverage,
  updateTodayInsightsConfig,
  type CacheStatus,
  type CheckinCoverage,
  type CheckinCoverageRow,
  type CheckinCoverageSummary,
  type MetricGap,
  type QualityAuditResponse,
  type TodayInsightsSettings,
} from "../../api/operations";
import type { Locale } from "../../i18n";
import { tp } from "../../i18n/pages";

interface Props {
  locale: Locale;
  schema?: string;
}

function fmtDuration(seconds?: number): string {
  if (seconds == null || !Number.isFinite(seconds)) return "—";
  if (seconds < 60) return `${Math.round(seconds)}s`;
  const min = Math.floor(seconds / 60);
  const sec = Math.round(seconds % 60);
  return `${min}m ${sec}s`;
}

function renderCheckinBlock(
  locale: Locale,
  title: string,
  summary: CheckinCoverageSummary,
  rows: CheckinCoverageRow[] | null,
  inactive: boolean,
) {
  if (inactive) {
    return (
      <div className="field">
        <strong>{title}</strong>
        <div className="notice">{tp(locale, "adminCheckinSlaInactive")}</div>
      </div>
    );
  }
  const counts = summary.answer_counts ?? {};
  return (
    <div className="field">
      <strong>{title}</strong>
      <div className="kpi-strip">
        <div className="kpi-box">
          <span>{tp(locale, "adminCheckinTotal")}</span>
          <strong>{summary.total_days}</strong>
        </div>
        <div className="kpi-box">
          <span>{tp(locale, "adminCheckinPrompted")}</span>
          <strong>{summary.prompted_coverage_percent}%</strong>
        </div>
        <div className="kpi-box">
          <span>{tp(locale, "adminCheckinAnswered")}</span>
          <strong>{summary.answered_coverage_percent}%</strong>
        </div>
        <div className="kpi-box">
          <span>{tp(locale, "adminCheckinAvgResponse")}</span>
          <strong>
            {summary.average_response_seconds == null
              ? tp(locale, "adminCheckinNoResponse")
              : fmtDuration(summary.average_response_seconds)}
          </strong>
        </div>
      </div>
      <p className="subpage__muted">
        {["great", "ok", "meh", "sick"]
          .map((k) => `${k}: ${counts[k] ?? 0}`)
          .join(" · ")}
      </p>
      {rows && rows.length > 0 ? (
        <div className="table-scroll">
          <table className="data-table">
            <thead>
              <tr>
                <th>{tp(locale, "adminCheckinDate")}</th>
                <th>{tp(locale, "adminCheckinStatus")}</th>
                <th>{tp(locale, "adminCheckinAnswer")}</th>
                <th className="is-num">{tp(locale, "adminCheckinLatency")}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={row.date}>
                  <td>
                    <code>{row.date}</code>
                  </td>
                  <td>
                    <span className="pill">{row.status}</span>
                  </td>
                  <td>{row.answer || "—"}</td>
                  <td className="is-num">
                    {fmtDuration(row.response_latency_seconds)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
    </div>
  );
}

export function AdminDiagnosticsTab({ locale, schema }: Props) {
  // Cache status
  const [cache, setCache] = useState<CacheStatus | null>(null);
  const [cacheError, setCacheError] = useState<string | null>(null);
  const [cacheLoading, setCacheLoading] = useState(false);

  // Gaps
  const [gaps, setGaps] = useState<MetricGap[] | null>(null);
  const [gapsError, setGapsError] = useState<string | null>(null);
  const [gapsLoading, setGapsLoading] = useState(false);

  // Quality audit
  const [audit, setAudit] = useState<QualityAuditResponse | null>(null);
  const [auditError, setAuditError] = useState<string | null>(null);
  const [auditLoading, setAuditLoading] = useState(false);

  // Check-in coverage
  const [checkin, setCheckin] = useState<CheckinCoverage | null>(null);
  const [checkinSince, setCheckinSince] = useState("");
  const [checkinError, setCheckinError] = useState<string | null>(null);
  const [checkinMsg, setCheckinMsg] = useState<string | null>(null);
  const [checkinLoading, setCheckinLoading] = useState(false);

  // Backfill
  const [backfillMsg, setBackfillMsg] = useState<{ tone: "good" | "danger"; text: string } | null>(null);
  const [backfillRunning, setBackfillRunning] = useState(false);

  // Today insights config
  const [insightsCfg, setInsightsCfg] = useState<TodayInsightsSettings | null>(null);
  const [b0Enabled, setB0Enabled] = useState(false);
  const [b1Preview, setB1Preview] = useState(false);
  const [insightsSaving, setInsightsSaving] = useState(false);
  const [insightsMsg, setInsightsMsg] = useState<{ tone: "good" | "danger"; text: string } | null>(null);

  // Quality maintenance
  const [qualityMsg, setQualityMsg] = useState<{ tone: "good" | "danger"; text: string } | null>(null);
  const [qualityBusy, setQualityBusy] = useState(false);

  const loadCache = useCallback(async () => {
    setCacheLoading(true);
    setCacheError(null);
    try {
      setCache(await getCacheStatus(schema));
    } catch (error: unknown) {
      setCacheError(error instanceof Error ? error.message : tp(locale, "errorGeneric"));
    } finally {
      setCacheLoading(false);
    }
  }, [locale, schema]);

  const loadGaps = useCallback(async () => {
    setGapsLoading(true);
    setGapsError(null);
    try {
      const res = await getMetricGaps(schema);
      setGaps(res.gaps ?? []);
    } catch (error: unknown) {
      setGapsError(error instanceof Error ? error.message : tp(locale, "errorGeneric"));
    } finally {
      setGapsLoading(false);
    }
  }, [locale, schema]);

  const loadAudit = useCallback(async () => {
    setAuditLoading(true);
    setAuditError(null);
    try {
      setAudit(await getQualityAudit(schema));
    } catch (error: unknown) {
      setAuditError(error instanceof Error ? error.message : tp(locale, "errorGeneric"));
    } finally {
      setAuditLoading(false);
    }
  }, [locale, schema]);

  const loadCheckin = useCallback(async () => {
    setCheckinLoading(true);
    setCheckinError(null);
    try {
      const res = await getCheckinCoverage(14, schema);
      setCheckin(res);
      setCheckinSince(res.enabled_since ?? "");
    } catch (error: unknown) {
      setCheckinError(error instanceof Error ? error.message : tp(locale, "errorGeneric"));
    } finally {
      setCheckinLoading(false);
    }
  }, [locale, schema]);

  useEffect(() => {
    const controller = new AbortController();
    getCacheStatus(schema, controller.signal)
      .then((res) => {
        if (!controller.signal.aborted) setCache(res);
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setCacheError(error instanceof Error ? error.message : tp(locale, "errorGeneric"));
        }
      });
    getCheckinCoverage(14, schema, controller.signal)
      .then((res) => {
        if (!controller.signal.aborted) {
          setCheckin(res);
          setCheckinSince(res.enabled_since ?? "");
        }
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setCheckinError(error instanceof Error ? error.message : tp(locale, "errorGeneric"));
        }
      });
    getTodayInsightsConfig(schema, controller.signal)
      .then((res) => {
        if (!controller.signal.aborted) {
          setInsightsCfg(res);
          setB0Enabled(res.b0_enabled);
          setB1Preview(res.b1_preview_enabled);
        }
      })
      .catch(() => undefined);
    return () => controller.abort();
  }, [locale, schema]);

  const handleSaveCheckinSince = async () => {
    setCheckinMsg(tp(locale, "saving"));
    try {
      const res = await updateCheckinCoverage(checkinSince, 14, schema);
      setCheckin(res);
      setCheckinSince(res.enabled_since ?? "");
      setCheckinMsg(tp(locale, "saved"));
    } catch (error: unknown) {
      setCheckinMsg(error instanceof Error ? error.message : tp(locale, "errorGeneric"));
    }
  };

  const handleBackfill = async (force: boolean) => {
    if (force && !window.confirm(tp(locale, "adminForceConfirm"))) return;
    setBackfillRunning(true);
    setBackfillMsg(null);
    try {
      const res = await startCacheBackfill(force, schema);
      setBackfillMsg({ tone: "good", text: res.message || res.status });
    } catch (error: unknown) {
      setBackfillMsg({
        tone: "danger",
        text: error instanceof Error ? error.message : tp(locale, "errorGeneric"),
      });
    } finally {
      setBackfillRunning(false);
    }
  };

  const handleSaveInsights = async () => {
    setInsightsSaving(true);
    setInsightsMsg(null);
    try {
      const res = await updateTodayInsightsConfig(
        {
          today_insights_b0_enabled: b0Enabled,
          today_insights_b1_preview_enabled: b1Preview,
        },
        schema,
      );
      setInsightsCfg(res);
      setB0Enabled(res.b0_enabled);
      setB1Preview(res.b1_preview_enabled);
      setInsightsMsg({ tone: "good", text: tp(locale, "saved") });
    } catch (error: unknown) {
      setInsightsMsg({
        tone: "danger",
        text: error instanceof Error ? error.message : tp(locale, "errorGeneric"),
      });
    } finally {
      setInsightsSaving(false);
    }
  };

  const handleQualityFix = async () => {
    setQualityBusy(true);
    setQualityMsg(null);
    try {
      const res = await applyQualityFix(schema);
      setQualityMsg({
        tone: "good",
        text: tp(locale, "adminQualityFixed", {
          impossible: res.impossible_flagged,
          suspect: res.suspect_flagged,
        }),
      });
      void loadAudit();
    } catch (error: unknown) {
      setQualityMsg({
        tone: "danger",
        text: error instanceof Error ? error.message : tp(locale, "errorGeneric"),
      });
    } finally {
      setQualityBusy(false);
    }
  };

  const handleQualityDigest = async () => {
    setQualityBusy(true);
    setQualityMsg(null);
    try {
      await sendQualityDigest(schema);
      setQualityMsg({ tone: "good", text: tp(locale, "adminQualityDigestSent") });
    } catch (error: unknown) {
      setQualityMsg({
        tone: "danger",
        text: error instanceof Error ? error.message : tp(locale, "errorGeneric"),
      });
    } finally {
      setQualityBusy(false);
    }
  };

  const b1StatusText = !insightsCfg
    ? tp(locale, "loading")
    : insightsCfg.b1_enabled
      ? tp(locale, "adminTodayInsightsB1Enabled")
      : insightsCfg.b1_quality_gate_approved &&
          !insightsCfg.b1_quality_gate_matches_active_ai
        ? tp(locale, "adminTodayInsightsB1Stale")
        : insightsCfg.b1_quality_gate_approved
          ? tp(locale, "adminTodayInsightsB1Ready")
          : tp(locale, "adminTodayInsightsB1Gated");

  return (
    <>
      <div className="admin-overview-grid">
        <button type="button" className="admin-overview-card" onClick={() => void loadCache()}>
          <span>{tp(locale, "adminCacheTitle")}</span>
          <strong>
            {cache ? `${cache.daily_scores.rows} daily` : tp(locale, "refresh")}
          </strong>
        </button>
        <button type="button" className="admin-overview-card" onClick={() => void loadGaps()}>
          <span>{tp(locale, "adminGapsTitle")}</span>
          <strong>{gaps ? `${gaps.length}` : tp(locale, "adminGapsCheck")}</strong>
        </button>
        <button type="button" className="admin-overview-card" onClick={() => void loadAudit()}>
          <span>{tp(locale, "adminQualityTitle")}</span>
          <strong>
            {audit ? `${audit.entries?.length ?? 0}` : tp(locale, "adminQualityRun")}
          </strong>
        </button>
        <button type="button" className="admin-overview-card" onClick={() => void loadCheckin()}>
          <span>{tp(locale, "adminCheckinTitle")}</span>
          <strong>
            {checkin ? `${checkin.summary.answered_coverage_percent}%` : tp(locale, "refresh")}
          </strong>
        </button>
      </div>

      {/* Cache Status */}
      <details className="panel panel--collapsible" open>
        <summary>{tp(locale, "adminCacheTitle")}</summary>
        <div className="panel__body">
          <div className="btn-row">
            <button
              type="button"
              className="btn btn--small"
              disabled={cacheLoading}
              onClick={() => void loadCache()}
            >
              {tp(locale, "refresh")}
            </button>
          </div>
          {cacheError ? <div className="notice notice--danger">{cacheError}</div> : null}
          {cache ? (
            <>
              <div className="kpi-strip">
                <div className="kpi-box">
                  <span>{tp(locale, "adminCacheLastSync")}</span>
                  <strong>{cache.last_sync ? cache.last_sync.slice(0, 16).replace("T", " ") : "—"}</strong>
                </div>
                <div className="kpi-box">
                  <span>{tp(locale, "adminCacheScoreVersion")}</span>
                  <strong>v{cache.score_version}</strong>
                </div>
                <div className="kpi-box">
                  <span>{tp(locale, "adminCacheTelegram")}</span>
                  <strong>
                    {cache.telegram_enabled
                      ? tp(locale, "adminEnabled")
                      : tp(locale, "adminDisabled")}
                  </strong>
                </div>
                <div className="kpi-box">
                  <span>{tp(locale, "adminCacheRedesignStorage")}</span>
                  <strong>
                    <span
                      className={
                        cache.redesign_storage.healthy
                          ? "pill pill--good"
                          : "pill pill--danger"
                      }
                    >
                      {cache.redesign_storage.healthy
                        ? tp(locale, "adminHealthy")
                        : tp(locale, "adminUnhealthy")}
                    </span>
                  </strong>
                </div>
              </div>
              <div className="table-scroll">
                <table className="data-table">
                  <thead>
                    <tr>
                      <th>{tp(locale, "adminCacheTable")}</th>
                      <th className="is-num">{tp(locale, "adminCacheRows")}</th>
                      <th className="is-num">{tp(locale, "adminCacheMetrics")}</th>
                      <th>{tp(locale, "adminCacheRange")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {(
                      [
                        ["metric_points", cache.raw_points],
                        ["minute_metrics", cache.minute_cache],
                        ["hourly_metrics", cache.hourly_cache],
                        ["daily_scores", cache.daily_scores],
                      ] as const
                    ).map(([name, t]) => (
                      <tr key={name}>
                        <td>
                          <code>{name}</code>
                        </td>
                        <td className="is-num">{t.rows.toLocaleString()}</td>
                        <td className="is-num">{t.metrics ?? "—"}</td>
                        <td>
                          {t.oldest || "—"} → {t.newest || "—"}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          ) : (
            <p className="subpage__muted">{tp(locale, "loading")}</p>
          )}
        </div>
      </details>

      {/* Data gaps */}
      <details className="panel panel--collapsible">
        <summary>{tp(locale, "adminGapsTitle")}</summary>
        <div className="panel__body">
          <div className="btn-row">
            <button
              type="button"
              className="btn btn--small"
              disabled={gapsLoading}
              onClick={() => void loadGaps()}
            >
              {tp(locale, "adminGapsCheck")}
            </button>
          </div>
          {gapsError ? <div className="notice notice--danger">{gapsError}</div> : null}
          {gaps ? (
            gaps.length === 0 ? (
              <div className="notice notice--good">{tp(locale, "adminGapsNone")}</div>
            ) : (
              <div className="table-scroll">
                <table className="data-table">
                  <tbody>
                    {gaps.map((gap) => (
                      <tr key={`${gap.from}-${gap.to}`}>
                        <td>
                          <code>{gap.from}</code> → <code>{gap.to}</code>
                        </td>
                        <td className="is-num">
                          {tp(locale, "adminGapsDays", { days: gap.days })}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )
          ) : null}
        </div>
      </details>

      {/* Data quality audit */}
      <details className="panel panel--collapsible">
        <summary>{tp(locale, "adminQualityTitle")}</summary>
        <div className="panel__body">
          <div className="btn-row">
            <button
              type="button"
              className="btn btn--small"
              disabled={auditLoading}
              onClick={() => void loadAudit()}
            >
              {tp(locale, "adminQualityRun")}
            </button>
          </div>
          {auditError ? <div className="notice notice--danger">{auditError}</div> : null}
          {audit ? (
            <>
              {(audit.entries?.length ?? 0) === 0 ? (
                <div className="notice notice--good">
                  {tp(locale, "adminQualityClean")}
                </div>
              ) : (
                <div className="table-scroll">
                  <table className="data-table">
                    <thead>
                      <tr>
                        <th>{tp(locale, "adminQualityMetric")}</th>
                        <th>{tp(locale, "adminQualityRange")}</th>
                        <th className="is-num">{tp(locale, "adminQualityBad")}</th>
                        <th className="is-num">{tp(locale, "adminQualityTotal")}</th>
                        <th className="is-num">{tp(locale, "adminQualitySample")}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {(audit.entries ?? []).map((entry) => (
                        <tr key={entry.metric}>
                          <td>
                            <code>{entry.metric}</code>
                          </td>
                          <td>
                            {entry.min} … {entry.max}
                          </td>
                          <td className="is-num">{entry.bad_count}</td>
                          <td className="is-num">{entry.total_rows}</td>
                          <td className="is-num">{entry.sample}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
              {audit.weekly ? (
                <div className="field">
                  <strong>{tp(locale, "adminQualityWeek")}</strong>
                  {Object.keys(audit.weekly.impossible ?? {}).length > 0 ? (
                    <p className="subpage__muted">
                      {tp(locale, "adminQualityImpossible")}:{" "}
                      {Object.entries(audit.weekly.impossible)
                        .map(([k, v]) => `${k} (${v})`)
                        .join(", ")}
                    </p>
                  ) : null}
                  {Object.keys(audit.weekly.suspect ?? {}).length > 0 ? (
                    <p className="subpage__muted">
                      {tp(locale, "adminQualitySuspect")}:{" "}
                      {Object.entries(audit.weekly.suspect)
                        .map(([k, v]) => `${k} (${v})`)
                        .join(", ")}
                    </p>
                  ) : null}
                  {(audit.weekly.missed_nights?.length ?? 0) > 0 ? (
                    <p className="subpage__muted">
                      {tp(locale, "adminQualityMissed")}:{" "}
                      {(audit.weekly.missed_nights ?? []).join(", ")}
                    </p>
                  ) : null}
                </div>
              ) : null}
            </>
          ) : null}
        </div>
      </details>

      {/* Check-in coverage */}
      <details className="panel panel--collapsible">
        <summary>{tp(locale, "adminCheckinTitle")}</summary>
        <div className="panel__body">
          <div className="field__row-inline">
            <label className="field__label" htmlFor="checkin-enabled-since">
              {tp(locale, "adminCheckinEnabledSince")}
            </label>
            <input
              id="checkin-enabled-since"
              type="date"
              className="field__input"
              value={checkinSince}
              onChange={(event) => setCheckinSince(event.target.value)}
            />
            <button
              type="button"
              className="btn btn--primary btn--small"
              onClick={() => void handleSaveCheckinSince()}
            >
              {tp(locale, "save")}
            </button>
            <button
              type="button"
              className="btn btn--small"
              disabled={checkinLoading}
              onClick={() => void loadCheckin()}
            >
              {tp(locale, "refresh")}
            </button>
            {checkinMsg ? <span className="field__hint">{checkinMsg}</span> : null}
          </div>
          {checkinError ? (
            <div className="notice notice--danger">{checkinError}</div>
          ) : null}
          {checkin ? (
            <>
              {renderCheckinBlock(
                locale,
                tp(locale, "adminCheckinCalendarSla"),
                checkin.sla_summary,
                checkin.sla_rows,
                !checkin.sla_active,
              )}
              {renderCheckinBlock(
                locale,
                tp(locale, "adminCheckinHistory"),
                checkin.summary,
                checkin.rows,
                false,
              )}
            </>
          ) : (
            <p className="subpage__muted">{tp(locale, "loading")}</p>
          )}
        </div>
      </details>

      {/* Cache backfill */}
      <details className="panel panel--collapsible">
        <summary>{tp(locale, "adminBackfillTitle")}</summary>
        <div className="panel__body">
          <div className="action-cards">
            <div className="action-card">
              <h3>{tp(locale, "adminIncrementalTitle")}</h3>
              <p>{tp(locale, "adminIncrementalDesc")}</p>
              <button
                type="button"
                className="btn btn--primary btn--small"
                disabled={backfillRunning}
                onClick={() => void handleBackfill(false)}
              >
                {tp(locale, "adminIncrementalRun")}
              </button>
            </div>
            <div className="action-card">
              <h3>{tp(locale, "adminForceTitle")}</h3>
              <p>{tp(locale, "adminForceDesc")}</p>
              <button
                type="button"
                className="btn btn--danger btn--small"
                disabled={backfillRunning}
                onClick={() => void handleBackfill(true)}
              >
                {tp(locale, "adminForceRun")}
              </button>
            </div>
          </div>
          {backfillMsg ? (
            <div className={`notice notice--${backfillMsg.tone}`}>
              {backfillMsg.text}
            </div>
          ) : null}
        </div>
      </details>

      {/* Today Insights config */}
      <details className="panel panel--collapsible">
        <summary>{tp(locale, "adminTodayInsightsTitle")}</summary>
        <div className="panel__body">
          <label className="field__checkbox">
            <input
              type="checkbox"
              checked={b0Enabled}
              disabled={!insightsCfg || insightsSaving}
              onChange={(event) => setB0Enabled(event.target.checked)}
            />
            {tp(locale, "adminTodayInsightsB0")}
          </label>
          <div className="btn-row">
            <span className="field__label">{tp(locale, "adminTodayInsightsB1")}:</span>
            <span className="pill">{b1StatusText}</span>
          </div>
          <label className="field__checkbox">
            <input
              type="checkbox"
              checked={b1Preview}
              disabled={!insightsCfg || insightsSaving}
              onChange={(event) => setB1Preview(event.target.checked)}
            />
            {tp(locale, "adminTodayInsightsB1Preview")}
          </label>
          <div className="btn-row">
            <button
              type="button"
              className="btn btn--primary btn--small"
              disabled={!insightsCfg || insightsSaving}
              onClick={() => void handleSaveInsights()}
            >
              {insightsSaving ? tp(locale, "saving") : tp(locale, "save")}
            </button>
          </div>
          {insightsMsg ? (
            <div className={`notice notice--${insightsMsg.tone}`}>
              {insightsMsg.text}
            </div>
          ) : null}
        </div>
      </details>

      {/* Quality maintenance */}
      <details className="panel panel--collapsible">
        <summary>{tp(locale, "adminQualityMaintenanceTitle")}</summary>
        <div className="panel__body">
          <div className="action-cards">
            <div className="action-card">
              <h3>{tp(locale, "adminQualityFix")}</h3>
              <p>{tp(locale, "adminQualityFixDesc")}</p>
              <button
                type="button"
                className="btn btn--primary btn--small"
                disabled={qualityBusy}
                onClick={() => void handleQualityFix()}
              >
                {tp(locale, "adminQualityFix")}
              </button>
            </div>
            <div className="action-card">
              <h3>{tp(locale, "adminQualityDigest")}</h3>
              <p>{tp(locale, "adminQualityDigestDesc")}</p>
              <button
                type="button"
                className="btn btn--small"
                disabled={qualityBusy}
                onClick={() => void handleQualityDigest()}
              >
                {tp(locale, "adminQualityDigest")}
              </button>
            </div>
          </div>
          {qualityMsg ? (
            <div className={`notice notice--${qualityMsg.tone}`}>
              {qualityMsg.text}
            </div>
          ) : null}
        </div>
      </details>
    </>
  );
}
