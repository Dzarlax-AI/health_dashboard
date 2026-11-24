import {
  getAIBriefing,
  getHealthBriefing,
  getMetricData,
  getReadinessHistory,
  getSection,
  getSession,
  getTodayInsights,
  type AIBriefingResponse,
  type HealthBriefingResponse,
  type MetricDataOptions,
  type MetricPlotSeries,
  type ReadinessHistoryResponse,
  type SectionResponse,
  type SessionResponse,
  type TodayInsightsResponse,
} from "../../api/client";
import type { Locale } from "../../i18n";
import type { HealthSectionConfig } from "./config";

export interface HealthDetailResources {
  briefing: HealthBriefingResponse;
  section?: SectionResponse;
  ai?: AIBriefingResponse;
  todayInsights?: TodayInsightsResponse;
  session?: SessionResponse;
  readiness?: ReadinessHistoryResponse;
  metrics: Record<string, MetricPlotSeries>;
  from?: string;
  to?: string;
  missing: string[];
}

export interface HealthDetailLoaders {
  briefing: typeof getHealthBriefing;
  section: typeof getSection;
  ai: typeof getAIBriefing;
  todayInsights?: typeof getTodayInsights;
  session: typeof getSession;
  readiness: typeof getReadinessHistory;
  metric: typeof getMetricData;
}

const defaultLoaders: HealthDetailLoaders = {
  briefing: getHealthBriefing,
  section: getSection,
  ai: getAIBriefing,
  todayInsights: getTodayInsights,
  session: getSession,
  readiness: getReadinessHistory,
  metric: getMetricData,
};

function dateOnly(value: string): string | undefined {
  const candidate = value.slice(0, 10);
  return /^\d{4}-\d{2}-\d{2}$/.test(candidate) ? candidate : undefined;
}

function dateOffset(date: string, days: number): string {
  const value = new Date(`${date}T12:00:00`);
  value.setDate(value.getDate() + days);
  const month = String(value.getMonth() + 1).padStart(2, "0");
  const day = String(value.getDate()).padStart(2, "0");
  return `${value.getFullYear()}-${month}-${day}`;
}

function fulfilled<T>(result?: PromiseSettledResult<T>): T | undefined {
  return result?.status === "fulfilled" ? result.value : undefined;
}

function throwIfAborted(signal?: AbortSignal): void {
  if (signal?.aborted) {
    throw signal.reason ?? new DOMException("The request was aborted", "AbortError");
  }
}

export async function loadHealthDetailResources(
  config: HealthSectionConfig,
  locale: Locale,
  signal?: AbortSignal,
  loaders: HealthDetailLoaders = defaultLoaders,
  onCharts?: (resources: HealthDetailResources) => void,
): Promise<HealthDetailResources> {
  const optional = Promise.allSettled([
    loaders.session(signal),
    config.context === "recovery-ai" ? loaders.ai(locale, signal) : Promise.resolve(undefined),
    config.key === "recovery" ? loaders.todayInsights?.(locale, signal) : undefined,
  ] as const);
  const [briefing, [sectionResult]] = await Promise.all([
    loaders.briefing(locale, signal),
    Promise.allSettled([loaders.section(config.key, locale, signal)]),
  ]);
  throwIfAborted(signal);
  const to = dateOnly(briefing.date);
  const section = fulfilled(sectionResult);
  const charts = section?.charts ?? [];
  const metricCharts = charts.filter(
    (chart): chart is typeof chart & { metric: string } => Boolean(chart.metric) && !chart.virtual,
  );
  const readiness = to && charts.some((chart) => chart.virtual)
    ? Promise.allSettled([loaders.readiness(365, signal)])
    : Promise.resolve([]);
  const metricResults = to ? await Promise.allSettled(metricCharts.map((chart) =>
    loaders.metric(chart.metric, undefined, to, signal, {
      agg: chart.agg as MetricDataOptions["agg"], bucket: "day",
    }),
  )) : [];
  throwIfAborted(signal);

  const metrics: Record<string, MetricPlotSeries> = {};
  metricCharts.forEach((chart, index) => {
    const response = fulfilled(metricResults[index]);
    if (response) metrics[chart.metric] = response;
  });
  const earliest = Object.values(metrics)
    .map((series) => dateOnly(series.points?.[0]?.date ?? ""))
    .filter((date): date is string => Boolean(date))
    .sort()[0];
  const base: HealthDetailResources = {
    briefing, section, metrics, to,
    from: to ? earliest ?? dateOffset(to, -89) : undefined,
    missing: [
      sectionResult.status === "rejected" ? "section" : undefined,
      ...metricResults.map((result, index) => result.status === "rejected" ? metricCharts[index].metric : undefined),
    ].filter((name): name is string => Boolean(name)),
  };
  onCharts?.(base);

  const [[session, ai, insights], [history]] = await Promise.all([optional, readiness]);
  throwIfAborted(signal);
  return {
    ...base,
    session: fulfilled(session), ai: fulfilled(ai), todayInsights: fulfilled(insights), readiness: fulfilled(history),
    missing: [...base.missing, ...[
      session.status === "rejected" ? "session" : undefined,
      ai.status === "rejected" ? "ai" : undefined,
      insights.status === "rejected" ? "todayInsights" : undefined,
      history?.status === "rejected" ? "readiness" : undefined,
    ].filter((name): name is string => Boolean(name))],
  };
}
