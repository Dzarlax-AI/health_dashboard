import type { AIBriefingResponse, TodayInsightsResponse } from "../../api/client";

export const maxAIPollAttempts = 10;

export function shouldPollAI(
  ai: AIBriefingResponse | undefined,
  attempts: number,
): boolean {
  if (!ai || ai.disabled || (attempts >= maxAIPollAttempts && !ai.previous)) {
    return false;
  }
  const hasSections = (ai.sections?.length ?? 0) > 0;
  const hasBlocks = Object.values(ai.blocks).some((body) => body.trim() !== "");
  const cacheIsCold = !ai.insight.trim() && !hasSections && !hasBlocks;
  return ai.generating || cacheIsCold || !!ai.previous;
}

// Today Insights always returns factual content. A disabled response is not a
// permanent client state: rollout flags can change while the dashboard stays
// open, so visible tabs keep revalidating it without spending the bounded
// provider-generation retry budget. Never replace visible facts with legacy
// AI text while that optional narrative is pending.
export function shouldPollTodayInsights(
  todayInsights: TodayInsightsResponse | undefined,
  attempts: number,
): boolean {
  // Retain the call signature; attempts now control cadence, not visibility.
  void attempts;
  if (!todayInsights) {
    return false;
  }
  const { generation } = todayInsights;
  if (generation.state === "disabled") {
    return true;
  }
  return (
    generation.state === "cold" ||
    generation.state === "generating" ||
    generation.state === "failed" ||
    (generation.slots?.some((slot) => ["cold", "generating", "failed"].includes(slot.state) || !slot.fresh_for_snapshot) ?? false) ||
    !generation.fresh_for_snapshot
  );
}

export function isTodayInsightsGenerationPending(
  todayInsights: TodayInsightsResponse | undefined,
): boolean {
  const state = todayInsights?.generation.state;
  return state === "cold" || state === "generating" || state === "failed";
}

// todayInsightsPollDelayMs waits out a server-issued retry window instead of
// polling an upstream provider failure every minute. A failed state without a
// window is retried at the ordinary cadence because it is immediately eligible.
export function todayInsightsPollDelayMs(
  todayInsights: TodayInsightsResponse | undefined,
  attempts: number,
): number | undefined {
  if (!shouldPollTodayInsights(todayInsights, attempts)) {
    return undefined;
  }
  if (todayInsights?.generation.state === "disabled") return 60_000;
  const retryAfter = Math.max(todayInsights?.generation.retry_after_seconds ?? 0,
    ...(todayInsights?.generation.slots?.map((slot) => slot.retry_after_seconds ?? 0) ?? []));
  if (attempts >= maxAIPollAttempts) return Math.max(300_000, retryAfter * 1_000);
  if (todayInsights?.generation.state === "failed" && retryAfter > 0) {
    return retryAfter * 1_000;
  }
  return 60_000;
}
