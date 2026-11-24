import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { ClientApiError, getSession, type SessionResponse } from "../../api/client";
import {
  listMetrics,
  queryMetricData,
  queryMetricRange,
  queryReadinessHistory,
  type MetricAgg,
  type MetricBucket,
  type MetricPoint,
  type MetricSourceSeries,
  type UnifiedMetricRangeResponse,
} from "../../api/operations";
import {
  clearSessionRecoveryAttempt,
  recoverSessionOnUnauthorized,
} from "../../auth/sessionRecovery";
import { AppHeader } from "../../components/AppHeader";
import { StatusPanel } from "../../components/StatusPanel";
import { resolveLocale, translate } from "../../i18n";
import { tp } from "../../i18n/pages";
import {
  buildMetricCsv,
  computeSeriesStats,
  daysBetweenInclusive,
  formatCompactValue,
  isBarChartMetric,
  isSleepChartMetric,
  shiftDateIso,
  SLEEP_PHASE_METRICS,
  todayIso,
} from "./catalog";
import { MetricChart, type SleepPhaseRow } from "./MetricChart";

type PresetId = "1" | "7" | "30" | "90" | "365" | "all" | "custom";

const PRESETS: readonly { id: Exclude<PresetId, "custom">; label?: string; days?: number }[] = [
  { id: "1", label: "1d", days: 1 },
  { id: "7", label: "7d", days: 7 },
  { id: "30", label: "30d", days: 30 },
  { id: "90", label: "90d", days: 90 },
  { id: "365", label: "1y", days: 365 },
  { id: "all" },
];

type ChartState =
  | { status: "loading" }
  | { status: "unauthenticated" }
  | { status: "error"; message: string }
  | {
      status: "ready";
      mode: "line" | "bar" | "readiness" | "by_source" | "sleep";
      bucket: string;
      agg: string;
      points: MetricPoint[];
      comparePoints: MetricPoint[];
      sources: MetricSourceSeries[];
      sleepRows: SleepPhaseRow[];
    };

export function MetricDetailPage({ metricName }: { metricName: string }) {
  const params = new URLSearchParams(window.location.search);
  const locale = resolveLocale(params.get("lang"));
  const today = useMemo(() => todayIso(), []);

  const [preset, setPreset] = useState<PresetId>("30");
  const [from, setFrom] = useState(() => shiftDateIso(today, -29));
  const [to, setTo] = useState(() => today);
  const [bucket, setBucket] = useState<MetricBucket>("");
  const [agg, setAgg] = useState<MetricAgg>("");
  const [bySource, setBySource] = useState(false);
  const [compare, setCompare] = useState(false);
  const [reloadKey, setReloadKey] = useState(0);

  const [session, setSession] = useState<SessionResponse | undefined>();
  const [displayName, setDisplayName] = useState(() =>
    metricName === "readiness" ? translate(locale, "readiness") : metricName,
  );
  const [unit, setUnit] = useState("");
  const [rangeBounds, setRangeBounds] = useState<UnifiedMetricRangeResponse | null>(null);
  const [chartState, setChartState] = useState<ChartState>({ status: "loading" });
  const [loadedRequestKey, setLoadedRequestKey] = useState("");
  const requestSeq = useRef(0);

  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);

  // Fetch metadata (display name, units, session, available min/max range)
  // independently so chart points render immediately without waiting on bounds.
  useEffect(() => {
    const controller = new AbortController();
    getSession(controller.signal)
      .then((data) => setSession(data))
      .catch(() => undefined);

    if (metricName !== "readiness") {
      listMetrics(locale, controller.signal)
        .then((catalog) => {
          const match = catalog.find((item) => item.name === metricName);
          if (match) {
            setDisplayName(match.display_name || match.name);
            setUnit(match.units || "");
          } else {
            setDisplayName(metricName);
          }
        })
        .catch(() => undefined);

      queryMetricRange(metricName, controller.signal)
        .then((bounds) => {
          if (!controller.signal.aborted) {
            setRangeBounds(bounds);
          }
        })
        .catch(() => undefined);
    }

    return () => controller.abort();
  }, [locale, metricName]);

  const applyPreset = useCallback(
    (id: Exclude<PresetId, "custom">) => {
      setPreset(id);
      const nowStr = todayIso();
      setTo(nowStr);
      if (id === "all") {
        if (rangeBounds?.min) {
          setFrom(rangeBounds.min.slice(0, 10));
        }
        return;
      }
      const days = Number(id);
      setFrom(shiftDateIso(nowStr, -(days - 1)));
    },
    [rangeBounds],
  );

  const shiftRange = useCallback((direction: -1 | 1) => {
    setPreset("custom");
    setFrom((prevFrom) => {
      const span = daysBetweenInclusive(prevFrom, to);
      const step = Math.max(1, Math.round(span * 0.5)) * direction;
      setTo((prevTo) => shiftDateIso(prevTo, step));
      return shiftDateIso(prevFrom, step);
    });
  }, [to]);

  // Keyboard shortcuts: Left/Right arrows shift window, 1-5 select presets.
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null;
      if (
        target &&
        (target.tagName === "INPUT" ||
          target.tagName === "SELECT" ||
          target.tagName === "TEXTAREA")
      ) {
        return;
      }
      if (event.key === "ArrowLeft") {
        shiftRange(-1);
      } else if (event.key === "ArrowRight") {
        shiftRange(1);
      } else if (event.key >= "1" && event.key <= "5") {
        const ids: Exclude<PresetId, "custom" | "all">[] = ["1", "7", "30", "90", "365"];
        applyPreset(ids[Number(event.key) - 1]);
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [applyPreset, shiftRange]);

  // Main series loader using Unified Health API v2 (no legacy /fragments/*).
  const queryFrom = preset === "all" ? undefined : from;
  const currentRequestKey = `${metricName}|${preset}|${queryFrom ?? ""}|${to}|${bucket}|${agg}|${bySource}|${compare}|${reloadKey}`;

  useEffect(() => {
    const seq = ++requestSeq.current;
    const controller = new AbortController();

    const isCurrent = () => seq === requestSeq.current && !controller.signal.aborted;

    const handleError = (error: unknown) => {
      if (!isCurrent()) return;
      if (
        recoverSessionOnUnauthorized(
          error,
          window.location,
          window.sessionStorage,
          (target) => window.location.assign(target),
        )
      ) {
        return;
      }
      if (error instanceof ClientApiError && error.status === 401) {
        setChartState({ status: "unauthenticated" });
        setLoadedRequestKey(currentRequestKey);
        return;
      }
      setChartState({
        status: "error",
        message:
          error instanceof Error ? error.message : tp(locale, "metricLoadError"),
      });
      setLoadedRequestKey(currentRequestKey);
    };

    const run = async () => {
      if (metricName === "readiness") {
        const res = await queryReadinessHistory(queryFrom ?? "2000-01-01", to, controller.signal);
        if (!isCurrent()) return;
        clearSessionRecoveryAttempt(window.sessionStorage);
        const points: MetricPoint[] = (res.history ?? [])
          .filter((entry) => entry.readiness !== null)
          .map((entry) => ({ date: entry.date, qty: entry.readiness as number }));
        if (preset === "all" && points.length > 0) {
          setFrom(points[0].date.slice(0, 10));
        }
        setChartState({
          status: "ready",
          mode: "readiness",
          bucket: "day",
          agg: "AVG",
          points,
          comparePoints: [],
          sources: [],
          sleepRows: [],
        });
        setLoadedRequestKey(currentRequestKey);
        return;
      }

      if (isSleepChartMetric(metricName) && !bucket && !agg && !bySource && !compare) {
        const responses = await Promise.all(
          SLEEP_PHASE_METRICS.map((phase) =>
            queryMetricData(
              phase,
              {
                from: queryFrom,
                to,
                bucket: "day",
                agg: "SUM",
                rangeAll: preset === "all",
                view: "quantity",
              },
              controller.signal,
            ).catch(() => null),
          ),
        );
        if (!isCurrent()) return;
        clearSessionRecoveryAttempt(window.sessionStorage);
        const rowMap = new Map<string, SleepPhaseRow>();
        SLEEP_PHASE_METRICS.forEach((phase, index) => {
          const resp = responses[index];
          for (const p of resp?.points ?? []) {
            const key = p.date.slice(0, 10);
            let row = rowMap.get(key);
            if (!row) {
              row = {
                date: key,
                sleep_deep: 0,
                sleep_core: 0,
                sleep_rem: 0,
                sleep_unspecified: 0,
                sleep_awake: 0,
              };
              rowMap.set(key, row);
            }
            row[phase] = Number(p.qty.toFixed(2));
          }
        });
        const sleepRows = Array.from(rowMap.values()).sort((a, b) =>
          a.date.localeCompare(b.date),
        );
        const totalPoints: MetricPoint[] = sleepRows.map((row) => ({
          date: row.date,
          qty: Number(
            (
              row.sleep_deep +
              row.sleep_core +
              row.sleep_rem +
              row.sleep_unspecified
            ).toFixed(2),
          ),
        }));
        if (preset === "all" && sleepRows.length > 0) {
          setFrom(sleepRows[0].date);
        }
        setChartState({
          status: "ready",
          mode: "sleep",
          bucket: "day",
          agg: "SUM",
          points: totalPoints,
          comparePoints: [],
          sources: [],
          sleepRows,
        });
        setLoadedRequestKey(currentRequestKey);
        return;
      }

      const mainPromise = queryMetricData(
        metricName,
        {
          from: queryFrom,
          to,
          bucket,
          agg,
          bySource,
          rangeAll: preset === "all",
          view: bySource ? "full" : "quantity",
        },
        controller.signal,
      );

      let comparePromise: Promise<MetricPoint[]> = Promise.resolve([]);
      if (compare && !bySource && queryFrom) {
        const span = daysBetweenInclusive(queryFrom, to);
        const prevTo = shiftDateIso(queryFrom, -1);
        const prevFrom = shiftDateIso(prevTo, -(span - 1));
        comparePromise = queryMetricData(
          metricName,
          {
            from: prevFrom,
            to: prevTo,
            bucket,
            agg,
            bySource: false,
            view: "quantity",
          },
          controller.signal,
        )
          .then((res) => res.points ?? [])
          .catch(() => []);
      }

      const [main, comparePoints] = await Promise.all([mainPromise, comparePromise]);
      if (!isCurrent()) return;
      clearSessionRecoveryAttempt(window.sessionStorage);

      const points = main.points ?? [];
      const sources = main.points_by_source ?? [];
      if (preset === "all" && points.length > 0) {
        const firstDate = points[0].date.slice(0, 10);
        if (/^\d{4}-\d{2}-\d{2}$/.test(firstDate)) {
          setFrom(firstDate);
        }
      }

      const mode = bySource
        ? "by_source"
        : isBarChartMetric(metricName, main.agg || agg)
          ? "bar"
          : "line";

      setChartState({
        status: "ready",
        mode,
        bucket: main.bucket || bucket || "day",
        agg: main.agg || agg,
        points,
        comparePoints,
        sources,
        sleepRows: [],
      });
      setLoadedRequestKey(currentRequestKey);
    };

    run().catch(handleError);
    return () => controller.abort();
  }, [agg, bucket, bySource, compare, currentRequestKey, locale, metricName, preset, queryFrom, reloadKey, to]);

  const effectiveChartState: ChartState =
    loadedRequestKey !== currentRequestKey ? { status: "loading" } : chartState;

  const stats = useMemo(() => {
    if (chartState.status !== "ready") return null;
    if (chartState.mode === "by_source") {
      const flat = chartState.sources.flatMap((s) => s.points ?? []);
      return computeSeriesStats(flat);
    }
    return computeSeriesStats(chartState.points);
  }, [chartState]);

  const handleDownloadCsv = () => {
    if (chartState.status !== "ready") return;
    const csv = buildMetricCsv(
      metricName,
      chartState.points,
      chartState.mode === "by_source" ? chartState.sources : null,
    );
    const blob = new Blob([csv], { type: "text/csv;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = `${metricName}-${from}-${to}.csv`;
    link.click();
    URL.revokeObjectURL(url);
  };

  return (
    <div className="app-shell" data-locale={locale}>
      <AppHeader locale={locale} isAdmin={Boolean(session?.is_admin)} />
      <main className="subpage">
        <header className="subpage__header">
          <div className="subpage__heading">
            <a className="subpage__back" href={`/metrics?lang=${locale}`}>
              ← {tp(locale, "backToMetrics")}
            </a>
            <h1>{displayName}</h1>
            {unit ? <span className="pill">{unit}</span> : null}
          </div>
          <span className="subpage__muted">
            {from} — {to}
          </span>
        </header>

        <section className="panel">
          <div className="metric-toolbar">
            <div className="metric-presets" role="group" aria-label="Date presets">
              {PRESETS.map((item) => (
                <button
                  key={item.id}
                  type="button"
                  className={preset === item.id ? "is-active" : ""}
                  aria-pressed={preset === item.id}
                  onClick={() => applyPreset(item.id)}
                >
                  {item.label ?? tp(locale, "presetAll")}
                </button>
              ))}
            </div>

            <div className="btn-row">
              <input
                type="date"
                className="field__input"
                value={from}
                aria-label="From date"
                onChange={(event) => {
                  setPreset("custom");
                  setFrom(event.target.value);
                }}
              />
              <span className="subpage__muted">—</span>
              <input
                type="date"
                className="field__input"
                value={to}
                aria-label="To date"
                onChange={(event) => {
                  setPreset("custom");
                  setTo(event.target.value);
                }}
              />
              <button
                type="button"
                className="btn btn--small"
                aria-label={tp(locale, "prevShift")}
                onClick={() => shiftRange(-1)}
              >
                ‹
              </button>
              <button
                type="button"
                className="btn btn--small"
                aria-label={tp(locale, "nextShift")}
                onClick={() => shiftRange(1)}
              >
                ›
              </button>
            </div>

            {metricName !== "readiness" ? (
              <div className="btn-row">
                <label className="field__label">
                  {tp(locale, "bucketLabel")}
                  <select
                    className="field__select"
                    value={bucket}
                    onChange={(event) =>
                      setBucket(event.target.value as MetricBucket)
                    }
                  >
                    <option value="">{tp(locale, "autoOption")}</option>
                    <option value="minute">{tp(locale, "bucketMinute")}</option>
                    <option value="hour">{tp(locale, "bucketHour")}</option>
                    <option value="day">{tp(locale, "bucketDay")}</option>
                  </select>
                </label>

                <label className="field__label">
                  {tp(locale, "aggLabel")}
                  <select
                    className="field__select"
                    value={agg}
                    onChange={(event) => setAgg(event.target.value as MetricAgg)}
                  >
                    <option value="">{tp(locale, "autoOption")}</option>
                    <option value="AVG">{tp(locale, "aggAvg")}</option>
                    <option value="SUM">{tp(locale, "aggSum")}</option>
                    <option value="MAX">{tp(locale, "aggMax")}</option>
                    <option value="MIN">{tp(locale, "aggMin")}</option>
                  </select>
                </label>

                <button
                  type="button"
                  className={`btn btn--small${bySource ? " is-active" : ""}`}
                  aria-pressed={bySource}
                  onClick={() => {
                    setBySource((prev) => !prev);
                    if (!bySource) setCompare(false);
                  }}
                >
                  {tp(locale, "bySource")}
                </button>

                <button
                  type="button"
                  className={`btn btn--small${compare ? " is-active" : ""}`}
                  aria-pressed={compare}
                  disabled={bySource || preset === "all"}
                  onClick={() => setCompare((prev) => !prev)}
                >
                  {tp(locale, "compare")}
                </button>

                <button
                  type="button"
                  className="btn btn--small"
                  disabled={chartState.status !== "ready"}
                  onClick={handleDownloadCsv}
                >
                  {tp(locale, "exportCsv")}
                </button>
              </div>
            ) : null}
          </div>

          {stats ? (
            <div className="metric-stats">
              <div className="metric-stat">
                <span>{tp(locale, "statAvg")}</span>
                <strong>
                  {formatCompactValue(stats.avg)}
                  {unit ? ` ${unit}` : ""}
                </strong>
              </div>
              <div className="metric-stat">
                <span>{tp(locale, "statMin")}</span>
                <strong>
                  {formatCompactValue(stats.min)}
                  {unit ? ` ${unit}` : ""}
                </strong>
              </div>
              <div className="metric-stat">
                <span>{tp(locale, "statMax")}</span>
                <strong>
                  {formatCompactValue(stats.max)}
                  {unit ? ` ${unit}` : ""}
                </strong>
              </div>
              {chartState.status === "ready" &&
              isBarChartMetric(metricName, chartState.agg) ? (
                <div className="metric-stat">
                  <span>{tp(locale, "statSum")}</span>
                  <strong>
                    {formatCompactValue(stats.sum)}
                    {unit ? ` ${unit}` : ""}
                  </strong>
                </div>
              ) : null}
              <div className="metric-stat">
                <span>{tp(locale, "statPoints")}</span>
                <strong>{stats.count}</strong>
              </div>
              {rangeBounds?.min && rangeBounds?.max ? (
                <div className="metric-stat">
                  <span>{tp(locale, "statRange")}</span>
                  <strong>
                    {rangeBounds.min.slice(0, 10)} → {rangeBounds.max.slice(0, 10)}
                  </strong>
                </div>
              ) : null}
            </div>
          ) : null}

          {effectiveChartState.status === "loading" ? (
            <StatusPanel
              state="loading"
              title={tp(locale, "loading")}
              detail={translate(locale, "loadingDetail")}
            />
          ) : effectiveChartState.status === "unauthenticated" ? (
            <div className="standalone-status">
              <StatusPanel
                state="error"
                title={translate(locale, "signInTitle")}
                detail={translate(locale, "signInDetail")}
              />
              <a
                className="primary-action"
                href={`/login?next=${encodeURIComponent(
                  `/metrics/${encodeURIComponent(metricName)}?lang=${locale}`,
                )}`}
              >
                {translate(locale, "signIn")}
              </a>
            </div>
          ) : effectiveChartState.status === "error" ? (
            <div className="standalone-status">
              <StatusPanel
                state="error"
                title={tp(locale, "metricLoadError")}
                detail={effectiveChartState.message}
              />
              <button
                type="button"
                className="primary-action"
                onClick={() => setReloadKey((n) => n + 1)}
              >
                {tp(locale, "retry")}
              </button>
            </div>
          ) : (
            <>
              {effectiveChartState.mode === "sleep" ? (
                <p className="subpage__muted">{tp(locale, "metricSleepHint")}</p>
              ) : null}
              <MetricChart
                locale={locale}
                label={displayName}
                unit={unit}
                bucket={effectiveChartState.bucket}
                mode={effectiveChartState.mode}
                points={effectiveChartState.points}
                comparePoints={effectiveChartState.comparePoints}
                sources={effectiveChartState.sources}
                sleepRows={effectiveChartState.sleepRows}
              />
            </>
          )}
        </section>
      </main>
    </div>
  );
}
