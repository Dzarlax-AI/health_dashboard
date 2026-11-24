import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import {
  ClientApiError,
  getReadinessHistory,
  getSession,
  type SessionResponse,
} from "../../api/client";
import {
  listMetrics,
  queryMetricData,
  queryMetricRange,
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
  buildSleepPhaseRows,
  computeSeriesStats,
  daysBetweenInclusive,
  formatCompactValue,
  isBarChartMetric,
  isSleepChartMetric,
  READINESS_MAX_DAYS,
  shiftDateIso,
  SLEEP_PHASE_METRICS,
  supportedMetricAggregations,
  todayIso,
  type SleepPhaseMetric,
  type SleepPhaseRow,
} from "./catalog";
import { MetricChart } from "./MetricChart";

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
      compareShiftDays: number;
      sources: MetricSourceSeries[];
      sleepRows: SleepPhaseRow[];
    };

export function MetricDetailPage({ metricName }: { metricName: string }) {
  const params = new URLSearchParams(window.location.search);
  const locale = resolveLocale(params.get("lang"));
  const today = useMemo(() => todayIso(), []);
  const isReadiness = metricName === "readiness";
  const readinessMinDate = useMemo(
    () => shiftDateIso(today, -(READINESS_MAX_DAYS - 1)),
    [today],
  );
  const visiblePresets = useMemo(
    () => (isReadiness ? PRESETS.filter((item) => item.id !== "all") : PRESETS),
    [isReadiness],
  );
  const supportedAggs = useMemo(
    () => supportedMetricAggregations(metricName),
    [metricName],
  );

  const [preset, setPreset] = useState<PresetId>("30");
  const [from, setFrom] = useState(() => shiftDateIso(today, -29));
  const [to, setTo] = useState(() => today);
  const [bucket, setBucket] = useState<MetricBucket>("");
  const [agg, setAgg] = useState<MetricAgg>("");
  const [bySource, setBySource] = useState(false);
  const [compare, setCompare] = useState(false);
  const [reloadKey, setReloadKey] = useState(0);

  const effectivePreset: PresetId = isReadiness && preset === "all" ? "365" : preset;
  const effectiveFrom = isReadiness
    ? from < readinessMinDate
      ? readinessMinDate
      : from > today
        ? today
        : from
    : from;
  const effectiveTo = isReadiness
    ? to > today
      ? today
      : to < effectiveFrom
        ? effectiveFrom
        : to
    : to;
  const effectiveAgg: MetricAgg = supportedAggs.includes(agg) ? agg : "";

  const [session, setSession] = useState<SessionResponse | undefined>();
  const [displayName, setDisplayName] = useState(() =>
    metricName === "readiness" ? translate(locale, "readiness") : metricName,
  );
  const [unit, setUnit] = useState("");
  const [rangeBounds, setRangeBounds] = useState<UnifiedMetricRangeResponse | null>(null);
  const rangeBoundsRef = useRef<UnifiedMetricRangeResponse | null>(null);
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
            rangeBoundsRef.current = bounds;
            setRangeBounds(bounds);
          }
        })
        .catch(() => undefined);
    }

    return () => controller.abort();
  }, [locale, metricName]);

  const applyPreset = useCallback(
    (id: Exclude<PresetId, "custom">) => {
      const targetId = isReadiness && id === "all" ? "365" : id;
      setPreset(targetId);
      const nowStr = todayIso();
      setTo(nowStr);
      if (targetId === "all") {
        if (rangeBounds?.min) {
          setFrom(rangeBounds.min.slice(0, 10));
        }
        return;
      }
      const days = Number(targetId);
      setFrom(shiftDateIso(nowStr, -(days - 1)));
    },
    [isReadiness, rangeBounds],
  );

  const shiftRange = useCallback(
    (direction: -1 | 1) => {
      setPreset("custom");
      const span = daysBetweenInclusive(effectiveFrom, effectiveTo);
      const step = Math.max(1, Math.round(span * 0.5)) * direction;
      let nextFrom = shiftDateIso(effectiveFrom, step);
      let nextTo = shiftDateIso(effectiveTo, step);
      if (isReadiness) {
        if (nextFrom < readinessMinDate) {
          nextFrom = readinessMinDate;
          const shiftedEnd = shiftDateIso(readinessMinDate, span - 1);
          nextTo = shiftedEnd > today ? today : shiftedEnd;
        }
        if (nextTo > today) {
          nextTo = today;
          const shiftedStart = shiftDateIso(today, -(span - 1));
          nextFrom = shiftedStart < readinessMinDate ? readinessMinDate : shiftedStart;
        }
      }
      setFrom(nextFrom);
      setTo(nextTo);
    },
    [effectiveFrom, effectiveTo, isReadiness, readinessMinDate, today],
  );

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
  const queryFrom = effectivePreset === "all" ? undefined : effectiveFrom;
  const currentRequestKey = `${metricName}|${effectivePreset}|${queryFrom ?? ""}|${effectiveTo}|${bucket}|${effectiveAgg}|${bySource}|${compare}|${reloadKey}`;

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
      if (isReadiness) {
        const requestedDays = Math.min(
          READINESS_MAX_DAYS,
          Math.max(daysBetweenInclusive(effectiveFrom, today), 1),
        );
        const res = await getReadinessHistory(requestedDays, controller.signal);
        if (!isCurrent()) return;
        clearSessionRecoveryAttempt(window.sessionStorage);
        const allPoints: MetricPoint[] = (res.points ?? []).map((entry) => ({
          date: entry.date,
          qty: entry.score,
        }));
        const points = allPoints.filter(
          (p) => p.date >= effectiveFrom && p.date <= effectiveTo,
        );
        setChartState({
          status: "ready",
          mode: "readiness",
          bucket: "day",
          agg: "AVG",
          points,
          comparePoints: [],
          compareShiftDays: 0,
          sources: [],
          sleepRows: [],
        });
        setLoadedRequestKey(currentRequestKey);
        return;
      }

      let resolvedFrom = queryFrom;
      if (effectivePreset === "all") {
        let earliest = rangeBoundsRef.current?.min?.slice(0, 10);
        if (!earliest) {
          const bounds = await queryMetricRange(metricName, controller.signal);
          if (!isCurrent()) return;
          rangeBoundsRef.current = bounds;
          setRangeBounds(bounds);
          earliest = bounds.min?.slice(0, 10);
        }
        if (earliest && /^\d{4}-\d{2}-\d{2}$/.test(earliest)) {
          resolvedFrom = earliest;
          setFrom(earliest);
        }
      }

      if (isSleepChartMetric(metricName) && !bucket && !effectiveAgg && !bySource && !compare) {
        const phasePromise = Promise.all(
          SLEEP_PHASE_METRICS.map((phase) =>
            queryMetricData(
              phase,
              {
                from: resolvedFrom,
                to: effectiveTo,
                bucket: "day",
                agg: "SUM",
                view: "quantity",
              },
              controller.signal,
            ),
          ),
        );
        const totalPromise =
          metricName === "sleep_total"
            ? queryMetricData(
                "sleep_total",
                {
                  from: resolvedFrom,
                  to: effectiveTo,
                  bucket: "day",
                  agg: "SUM",
                  view: "quantity",
                },
                controller.signal,
              )
            : Promise.resolve(null);

        const [responses, totalResponse] = await Promise.all([
          phasePromise,
          totalPromise,
        ]);
        if (!isCurrent()) return;
        clearSessionRecoveryAttempt(window.sessionStorage);

        const phasePoints: Partial<Record<SleepPhaseMetric, MetricPoint[]>> = {};
        SLEEP_PHASE_METRICS.forEach((phase, index) => {
          phasePoints[phase] = responses[index].points ?? [];
        });
        const totalPoints = totalResponse ? (totalResponse.points ?? []) : null;
        const sleepRows = buildSleepPhaseRows(phasePoints, totalPoints);
        const selectedPoints: MetricPoint[] =
          metricName === "sleep_total"
            ? (totalPoints ?? [])
            : (phasePoints[metricName as SleepPhaseMetric] ?? []);
        if (effectivePreset === "all" && !resolvedFrom && sleepRows.length > 0) {
          setFrom(sleepRows[0].date);
        }
        setChartState({
          status: "ready",
          mode: "sleep",
          bucket: "day",
          agg: "SUM",
          points: selectedPoints,
          comparePoints: [],
          compareShiftDays: 0,
          sources: [],
          sleepRows,
        });
        setLoadedRequestKey(currentRequestKey);
        return;
      }

      const mainPromise = queryMetricData(
        metricName,
        {
          from: resolvedFrom,
          to: effectiveTo,
          bucket,
          agg: effectiveAgg,
          bySource,
          view: bySource ? "full" : "quantity",
        },
        controller.signal,
      );

      let compareShiftDays = 0;
      let comparePromise: Promise<MetricPoint[]> = Promise.resolve([]);
      if (compare && !bySource && resolvedFrom && effectivePreset !== "all") {
        const span = daysBetweenInclusive(resolvedFrom, effectiveTo);
        compareShiftDays = span;
        const prevTo = shiftDateIso(resolvedFrom, -1);
        const prevFrom = shiftDateIso(prevTo, -(span - 1));
        comparePromise = queryMetricData(
          metricName,
          {
            from: prevFrom,
            to: prevTo,
            bucket,
            agg: effectiveAgg,
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
      if (effectivePreset === "all" && !resolvedFrom && points.length > 0) {
        const firstDate = points[0].date.slice(0, 10);
        if (/^\d{4}-\d{2}-\d{2}$/.test(firstDate)) {
          setFrom(firstDate);
        }
      }

      const mode = bySource
        ? "by_source"
        : isBarChartMetric(metricName, main.agg || effectiveAgg)
          ? "bar"
          : "line";

      setChartState({
        status: "ready",
        mode,
        bucket: main.bucket || bucket || "day",
        agg: main.agg || effectiveAgg,
        points,
        comparePoints,
        compareShiftDays,
        sources,
        sleepRows: [],
      });
      setLoadedRequestKey(currentRequestKey);
    };

    run().catch(handleError);
    return () => controller.abort();
  }, [
    bucket,
    bySource,
    compare,
    currentRequestKey,
    effectiveAgg,
    effectiveFrom,
    effectivePreset,
    effectiveTo,
    isReadiness,
    locale,
    metricName,
    queryFrom,
    reloadKey,
    today,
  ]);

  const effectiveChartState = useMemo<ChartState>(
    () =>
      loadedRequestKey !== currentRequestKey
        ? { status: "loading" }
        : chartState,
    [chartState, currentRequestKey, loadedRequestKey],
  );

  const stats = useMemo(() => {
    if (effectiveChartState.status !== "ready") return null;
    if (effectiveChartState.mode === "by_source") {
      const flat = effectiveChartState.sources.flatMap((s) => s.points ?? []);
      return computeSeriesStats(flat);
    }
    return computeSeriesStats(effectiveChartState.points);
  }, [effectiveChartState]);

  const handleDownloadCsv = () => {
    if (effectiveChartState.status !== "ready") return;
    const csv = buildMetricCsv(
      metricName,
      effectiveChartState.points,
      effectiveChartState.mode === "by_source" ? effectiveChartState.sources : null,
    );
    const blob = new Blob([csv], { type: "text/csv;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = `${metricName}-${effectiveFrom}-${effectiveTo}.csv`;
    link.click();
    URL.revokeObjectURL(url);
  };

  const aggOptionLabels: Record<MetricAgg, string> = {
    "": tp(locale, "autoOption"),
    AVG: tp(locale, "aggAvg"),
    SUM: tp(locale, "aggSum"),
    MAX: tp(locale, "aggMax"),
    MIN: tp(locale, "aggMin"),
  };

  const availableRangeLabel = isReadiness
    ? `${readinessMinDate} → ${today}`
    : rangeBounds?.min && rangeBounds?.max
      ? `${rangeBounds.min.slice(0, 10)} → ${rangeBounds.max.slice(0, 10)}`
      : null;

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
            {effectiveFrom} — {effectiveTo}
          </span>
        </header>

        <section className="panel">
          <div className="metric-toolbar">
            <div className="metric-presets" role="group" aria-label="Date presets">
              {visiblePresets.map((item) => (
                <button
                  key={item.id}
                  type="button"
                  className={effectivePreset === item.id ? "is-active" : ""}
                  aria-pressed={effectivePreset === item.id}
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
                value={effectiveFrom}
                min={isReadiness ? readinessMinDate : undefined}
                max={isReadiness ? today : undefined}
                aria-label="From date"
                onChange={(event) => {
                  const raw = event.target.value;
                  setPreset("custom");
                  if (!isReadiness || !raw) {
                    setFrom(raw);
                    return;
                  }
                  const clamped =
                    raw < readinessMinDate
                      ? readinessMinDate
                      : raw > today
                        ? today
                        : raw;
                  setFrom(clamped);
                  if (clamped > effectiveTo) {
                    setTo(clamped);
                  }
                }}
              />
              <span className="subpage__muted">—</span>
              <input
                type="date"
                className="field__input"
                value={effectiveTo}
                min={isReadiness ? readinessMinDate : undefined}
                max={isReadiness ? today : undefined}
                aria-label="To date"
                onChange={(event) => {
                  const raw = event.target.value;
                  setPreset("custom");
                  if (!isReadiness || !raw) {
                    setTo(raw);
                    return;
                  }
                  const clamped =
                    raw < readinessMinDate
                      ? readinessMinDate
                      : raw > today
                        ? today
                        : raw;
                  setTo(clamped);
                  if (clamped < effectiveFrom) {
                    setFrom(clamped);
                  }
                }}
              />
              <button
                type="button"
                className="btn btn--small"
                aria-label={tp(locale, "prevShift")}
                disabled={isReadiness && effectiveFrom <= readinessMinDate}
                onClick={() => shiftRange(-1)}
              >
                ‹
              </button>
              <button
                type="button"
                className="btn btn--small"
                aria-label={tp(locale, "nextShift")}
                disabled={isReadiness && effectiveTo >= today}
                onClick={() => shiftRange(1)}
              >
                ›
              </button>
            </div>

            {!isReadiness ? (
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
                    value={effectiveAgg}
                    onChange={(event) => setAgg(event.target.value as MetricAgg)}
                  >
                    {supportedAggs.map((option) => (
                      <option key={option || "auto"} value={option}>
                        {aggOptionLabels[option]}
                      </option>
                    ))}
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
                  disabled={bySource || effectivePreset === "all"}
                  onClick={() => setCompare((prev) => !prev)}
                >
                  {tp(locale, "compare")}
                </button>

                <button
                  type="button"
                  className="btn btn--small"
                  disabled={effectiveChartState.status !== "ready"}
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
              {effectiveChartState.status === "ready" &&
              isBarChartMetric(metricName, effectiveChartState.agg) ? (
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
              {availableRangeLabel ? (
                <div className="metric-stat">
                  <span>{tp(locale, "statRange")}</span>
                  <strong>{availableRangeLabel}</strong>
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
              ) : effectiveChartState.mode === "readiness" ? (
                <p className="subpage__muted">
                  {tp(locale, "metricReadinessHint", {
                    from: readinessMinDate,
                    to: today,
                  })}
                </p>
              ) : null}
              <MetricChart
                locale={locale}
                label={displayName}
                unit={unit}
                bucket={effectiveChartState.bucket}
                mode={effectiveChartState.mode}
                points={effectiveChartState.points}
                comparePoints={effectiveChartState.comparePoints}
                compareShiftDays={effectiveChartState.compareShiftDays}
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
