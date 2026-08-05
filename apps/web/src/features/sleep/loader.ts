import {
  getAIBriefing,
  getDerivedMetrics,
  getHealthBriefing,
  getMetricData,
  getMetricRange,
  getSection,
  getSession,
  type AIBriefingResponse,
  type DerivedMetricsResponse,
  type HealthBriefingResponse,
  type MetricDataResponse,
  type MetricRangeResponse,
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
  range?: MetricRangeResponse;
  session?: SessionResponse;
  metrics: Partial<Record<SleepMetric, MetricDataResponse>>;
  missing: string[];
}

export interface SleepLoaders {
  briefing: typeof getHealthBriefing;
  section: typeof getSection;
  ai: typeof getAIBriefing;
  range: typeof getMetricRange;
  wake: typeof getDerivedMetrics;
  session: typeof getSession;
  metric: typeof getMetricData;
}

const defaultLoaders: SleepLoaders = {
  briefing: getHealthBriefing,
  section: getSection,
  ai: getAIBriefing,
  range: getMetricRange,
  wake: getDerivedMetrics,
  session: getSession,
  metric: getMetricData,
};

function normalizedDate(value: string): string | undefined {
  const candidate = value.slice(0, 10);
  if (!/^\d{4}-\d{2}-\d{2}$/.test(candidate)) return undefined;
  const parsed = new Date(`${candidate}T12:00:00`);
  return Number.isNaN(parsed.getTime()) || parsed.toISOString().slice(0, 10) !== candidate
    ? undefined
    : candidate;
}

function dateOffset(date: string, days: number): string {
  const value = new Date(`${date}T12:00:00`);
  value.setDate(value.getDate() + days);
  return value.toISOString().slice(0, 10);
}

function throwIfAborted(signal?: AbortSignal): void {
  if (signal?.aborted) {
    throw signal.reason ?? new DOMException("The request was aborted", "AbortError");
  }
}

function fulfilled<T>(result: PromiseSettledResult<T>): T | undefined {
  return result.status === "fulfilled" ? result.value : undefined;
}

export async function loadSleepResources(
  locale: Locale,
  signal?: AbortSignal,
  loaders: SleepLoaders = defaultLoaders,
): Promise<SleepResources> {
  const rawBriefing = await loaders.briefing(locale, signal);
  const briefingDate = normalizedDate(rawBriefing.date);
  const briefing = briefingDate ? { ...rawBriefing, date: briefingDate } : rawBriefing;
  const [section, ai, session, range] = await Promise.allSettled([
    loaders.section("sleep", locale, signal),
    loaders.ai(locale, signal),
    loaders.session(signal),
    loaders.range("sleep_total", signal),
  ]);
  throwIfAborted(signal);

  const fixedMissing = [
    section.status === "rejected" ? "section" : undefined,
    ai.status === "rejected" ? "ai" : undefined,
    session.status === "rejected" ? "session" : undefined,
    range.status === "rejected" ? "range" : undefined,
  ].filter((name): name is string => Boolean(name));

  if (!briefingDate) {
    return {
      briefing,
      section: fulfilled(section),
      ai: fulfilled(ai),
      session: fulfilled(session),
      range: fulfilled(range),
      metrics: {},
      missing: fixedMissing,
    };
  }

  const earliest = range.status === "fulfilled" ? normalizedDate(range.value.min) : undefined;
  const from = earliest && earliest <= briefingDate ? earliest : dateOffset(briefingDate, -89);
  const [wake] = await Promise.allSettled([
    loaders.wake("wake_time", from, briefingDate, signal),
  ]);
  const metricResults = await Promise.allSettled(
    sleepMetrics.map((metric) => loaders.metric(metric, from, briefingDate, signal)),
  );
  throwIfAborted(signal);

  const metrics: Partial<Record<SleepMetric, MetricDataResponse>> = {};
  sleepMetrics.forEach((metric, index) => {
    const value = fulfilled(metricResults[index]);
    if (value) {
      metrics[metric] = value;
    }
  });

  return {
    briefing,
    section: fulfilled(section),
    ai: fulfilled(ai),
    wake: fulfilled(wake),
    session: fulfilled(session),
    range: fulfilled(range),
    metrics,
    missing: [
      ...fixedMissing,
      wake.status === "rejected" ? "wake" : undefined,
      ...metricResults.map((result, index) =>
        result.status === "rejected" ? sleepMetrics[index] : undefined,
      ),
    ].filter((name): name is string => Boolean(name)),
  };
}
