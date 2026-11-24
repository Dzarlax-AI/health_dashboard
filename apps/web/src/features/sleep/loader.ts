import {
  getAIBriefing,
  getDerivedMetrics,
  getHealthBriefing,
  getMetricData,
  getMetricRange,
  getSleepDurationBalance,
  getSleepDurationComparison,
  getSleepGoal,
  getSection,
  getSession,
  getTodayInsights,
  type AIBriefingResponse,
  type DerivedMetricsResponse,
  type HealthBriefingResponse,
  type MetricDataResponse,
  type MetricRangeResponse,
  type SectionResponse,
  type SessionResponse,
  type SleepDurationBalanceResponse,
  type SleepDurationComparison,
  type SleepGoalResponse,
  type TodayInsightsResponse,
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
  balance?: SleepDurationBalanceResponse;
  comparison?: SleepDurationComparison | null;
  goal?: SleepGoalResponse;
  todayInsights?: TodayInsightsResponse;
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
  balance: typeof getSleepDurationBalance;
  comparison?: typeof getSleepDurationComparison;
  goal: typeof getSleepGoal;
  todayInsights?: typeof getTodayInsights;
  metric: typeof getMetricData;
}

const defaultLoaders: SleepLoaders = {
  briefing: getHealthBriefing,
  section: getSection,
  ai: getAIBriefing,
  range: getMetricRange,
  wake: getDerivedMetrics,
  session: getSession,
  balance: getSleepDurationBalance,
  comparison: getSleepDurationComparison,
  goal: getSleepGoal,
  todayInsights: getTodayInsights,
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
  onCharts?: (resources: SleepResources) => void,
): Promise<SleepResources> {
  const rawBriefing = await loaders.briefing(locale, signal);
  const briefingDate = normalizedDate(rawBriefing.date);
  const briefing = briefingDate ? { ...rawBriefing, date: briefingDate } : rawBriefing;
  const embeddedComparison = briefing.sleep_duration_comparison;
  const supporting = Promise.allSettled([
    loaders.section("sleep", locale, signal),
    loaders.ai(locale, signal),
    loaders.session(signal),
    loaders.balance(signal, briefingDate),
    loaders.goal(signal, briefingDate),
    loaders.todayInsights?.(locale, signal) ?? Promise.resolve(undefined),
    embeddedComparison && embeddedComparison.wake_date === briefingDate
      ? embeddedComparison
      : loaders.comparison?.(signal, briefingDate),
  ] as const);
  const rangeAndWake = Promise.allSettled([loaders.range("sleep_total", signal)]).then(async ([range]) => {
    if (!briefingDate || signal?.aborted) return { range, wake: undefined };
    const earliest = normalizedDate(fulfilled(range)?.min ?? "");
    const from = earliest && earliest <= briefingDate ? earliest : dateOffset(briefingDate, -89);
    const [wake] = await Promise.allSettled([loaders.wake("wake_time", from, briefingDate, signal)]);
    return { range, wake };
  });
  const metricResults = briefingDate
    ? await Promise.allSettled(sleepMetrics.map((metric) => loaders.metric(metric, undefined, briefingDate, signal)))
    : [];
  throwIfAborted(signal);
  const metrics: SleepResources["metrics"] = {};
  metricResults.forEach((result, i) => {
    if (result.status === "fulfilled") metrics[sleepMetrics[i]] = result.value;
  });
  if (briefingDate && !metrics.sleep_total) {
    try {
      metrics.sleep_total = await loaders.metric("sleep_total", undefined, briefingDate, signal);
    } catch {
      throwIfAborted(signal);
    }
    throwIfAborted(signal);
  }
  const chartResources: SleepResources = {
    briefing, metrics,
    missing: metricResults.flatMap((result, i) =>
      result.status === "rejected" && !metrics[sleepMetrics[i]] ? [sleepMetrics[i]] : []),
  };
  if (briefingDate) onCharts?.(chartResources);

  const [[section, ai, session, balance, goal, insights, comparison], { range, wake }] = await Promise.all([supporting, rangeAndWake]);
  throwIfAborted(signal);
  return {
    ...chartResources, section: fulfilled(section), ai: fulfilled(ai), session: fulfilled(session),
    range: fulfilled(range), wake: wake && fulfilled(wake), balance: fulfilled(balance),
    goal: fulfilled(goal), comparison: fulfilled(comparison), todayInsights: fulfilled(insights),
    missing: [...chartResources.missing, ...[
      section.status === "rejected" ? "section" : undefined,
      ai.status === "rejected" ? "ai" : undefined,
      session.status === "rejected" ? "session" : undefined,
      range.status === "rejected" ? "range" : undefined,
      wake?.status === "rejected" ? "wake" : undefined,
      balance.status === "rejected" ? "balance" : undefined,
      goal.status === "rejected" ? "goal" : undefined,
      insights.status === "rejected" ? "todayInsights" : undefined,
    ].filter((name): name is string => Boolean(name))],
  };
}
