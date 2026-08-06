import {
  getAIBriefing,
  getHealthBriefing,
  getMetricData,
  getMetricRange,
  getReadinessHistory,
  getSection,
  getSession,
  type AIBriefingResponse,
  type HealthBriefingResponse,
  type MetricDataOptions,
  type MetricDataResponse,
  type ReadinessHistoryResponse,
  type SectionResponse,
  type SessionResponse,
} from "../../api/client";
import type { Locale } from "../../i18n";
import type { HealthSectionConfig } from "./config";

export interface HealthDetailResources {
  briefing: HealthBriefingResponse;
  section?: SectionResponse;
  ai?: AIBriefingResponse;
  session?: SessionResponse;
  readiness?: ReadinessHistoryResponse;
  metrics: Record<string, MetricDataResponse>;
  from?: string;
  to?: string;
  missing: string[];
}

export interface HealthDetailLoaders {
  briefing: typeof getHealthBriefing;
  section: typeof getSection;
  ai: typeof getAIBriefing;
  session: typeof getSession;
  range: typeof getMetricRange;
  readiness: typeof getReadinessHistory;
  metric: typeof getMetricData;
}

const defaultLoaders: HealthDetailLoaders = {
  briefing: getHealthBriefing,
  section: getSection,
  ai: getAIBriefing,
  session: getSession,
  range: getMetricRange,
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
): Promise<HealthDetailResources> {
  const briefing = await loaders.briefing(locale, signal);
  const to = dateOnly(briefing.date);
  const [sectionResult, sessionResult, aiResult] = await Promise.allSettled([
    loaders.section(config.key, locale, signal),
    loaders.session(signal),
    config.context === "recovery-ai"
      ? loaders.ai(locale, signal)
      : Promise.resolve(undefined),
  ]);
  throwIfAborted(signal);

  const section = fulfilled(sectionResult);
  const charts = section?.charts ?? [];
  const metricCharts = charts.filter(
    (chart): chart is typeof chart & { metric: string } =>
      Boolean(chart.metric) && !chart.virtual,
  );
  const rangeResults = await Promise.allSettled(
    metricCharts.map((chart) => loaders.range(chart.metric, signal)),
  );
  throwIfAborted(signal);

  const earliest = rangeResults
    .map(fulfilled)
    .map((range) => dateOnly(range?.min ?? ""))
    .filter((date): date is string => Boolean(date))
    .sort()[0];
  const from = to ? earliest ?? dateOffset(to, -89) : undefined;

  const metricResults =
    from && to
      ? await Promise.allSettled(
          metricCharts.map((chart) => {
            const options: MetricDataOptions = {
              agg: (chart.agg || "AVG") as MetricDataOptions["agg"],
              bucket: "day",
            };
            return loaders.metric(chart.metric, from, to, signal, options);
          }),
        )
      : [];
  const readinessResult =
    from && to && charts.some((chart) => chart.virtual)
      ? await Promise.allSettled([loaders.readiness(365, signal)])
      : [];
  throwIfAborted(signal);

  const metrics: Record<string, MetricDataResponse> = {};
  metricCharts.forEach((chart, index) => {
    const response = fulfilled(metricResults[index]);
    if (response) metrics[chart.metric] = response;
  });

  return {
    briefing,
    section,
    ai: fulfilled(aiResult),
    session: fulfilled(sessionResult),
    readiness: fulfilled(readinessResult[0]),
    metrics,
    from,
    to,
    missing: [
      sectionResult.status === "rejected" ? "section" : undefined,
      sessionResult.status === "rejected" ? "session" : undefined,
      aiResult.status === "rejected" ? "ai" : undefined,
      ...rangeResults.map((result, index) =>
        result.status === "rejected" ? `${metricCharts[index].metric}:range` : undefined,
      ),
      ...metricResults.map((result, index) =>
        result.status === "rejected" ? metricCharts[index].metric : undefined,
      ),
      readinessResult[0]?.status === "rejected" ? "readiness" : undefined,
    ].filter((value): value is string => Boolean(value)),
  };
}
