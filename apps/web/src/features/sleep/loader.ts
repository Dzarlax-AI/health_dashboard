import {
  getAIBriefing,
  getDerivedMetrics,
  getHealthBriefing,
  getMetricData,
  getSection,
  getSession,
  type AIBriefingResponse,
  type DerivedMetricsResponse,
  type HealthBriefingResponse,
  type MetricDataResponse,
  type SectionResponse,
  type SessionResponse,
} from "../../api/client";
import type { Locale } from "../../i18n";

export const sleepMetrics = [
  "sleep_total",
  "sleep_deep",
  "sleep_rem",
  "sleep_core",
  "sleep_unspecified",
  "sleep_awake",
] as const;

export type SleepMetric = (typeof sleepMetrics)[number];

export interface SleepResources {
  briefing: HealthBriefingResponse;
  section?: SectionResponse;
  ai?: AIBriefingResponse;
  wake?: DerivedMetricsResponse;
  session?: SessionResponse;
  metrics: Partial<Record<SleepMetric, MetricDataResponse>>;
  missing: string[];
}

function dateOffset(date: string, days: number): string {
  const value = new Date(`${date}T12:00:00`);
  value.setDate(value.getDate() + days);
  return value.toISOString().slice(0, 10);
}

function fulfilled<T>(result: PromiseSettledResult<T>): T | undefined {
  return result.status === "fulfilled" ? result.value : undefined;
}

export async function loadSleepResources(
  locale: Locale,
  signal?: AbortSignal,
): Promise<SleepResources> {
  const briefing = await getHealthBriefing(locale, signal);
  const from = dateOffset(briefing.date, -89);
  const [section, ai, wake, session] = await Promise.allSettled([
    getSection("sleep", locale, signal),
    getAIBriefing(locale, signal),
    getDerivedMetrics("wake_time", from, briefing.date, signal),
    getSession(signal),
  ]);
  const metricResults = await Promise.allSettled(
    sleepMetrics.map((metric) => getMetricData(metric, from, briefing.date, signal)),
  );

  const metrics: Partial<Record<SleepMetric, MetricDataResponse>> = {};
  sleepMetrics.forEach((metric, index) => {
    const value = fulfilled(metricResults[index]);
    if (value) {
      metrics[metric] = value;
    }
  });

  const optional = [section, ai, wake, session];
  return {
    briefing,
    section: fulfilled(section),
    ai: fulfilled(ai),
    wake: fulfilled(wake),
    session: fulfilled(session),
    metrics,
    missing: [
      ...optional
        .map((result, index) =>
          result.status === "rejected" ? ["section", "ai", "wake", "session"][index] : undefined,
        ),
      ...metricResults.map((result, index) =>
        result.status === "rejected" ? sleepMetrics[index] : undefined,
      ),
    ].filter((name): name is string => Boolean(name)),
  };
}
