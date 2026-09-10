import type { AIBriefingResponse, TodayInsightsResponse } from "../../api/client";

export const maxAIPollAttempts = 10;

export function shouldPollAI(
  ai: AIBriefingResponse | undefined,
  attempts: number,
): boolean {
  if (!ai || ai.disabled || attempts >= maxAIPollAttempts) {
    return false;
  }
  const hasSections = (ai.sections?.length ?? 0) > 0;
  const hasBlocks = Object.values(ai.blocks).some((body) => body.trim() !== "");
  const cacheIsCold = !ai.insight.trim() && !hasSections && !hasBlocks;
  return ai.generating || cacheIsCold;
}

// Today Insights always returns factual content. Poll only while its optional
// provider acknowledgement is pending or the saved bundle no longer matches
// that factual snapshot; never replace the visible facts with legacy AI text.
export function shouldPollTodayInsights(
  todayInsights: TodayInsightsResponse | undefined,
  attempts: number,
): boolean {
  if (!todayInsights || attempts >= maxAIPollAttempts) {
    return false;
  }
  const { generation } = todayInsights;
  return (
    generation.state === "cold" ||
    generation.state === "generating" ||
    generation.state === "failed" ||
    !generation.fresh_for_snapshot
  );
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
  const retryAfter = todayInsights?.generation.retry_after_seconds ?? 0;
  if (todayInsights?.generation.state === "failed" && retryAfter > 0) {
    return retryAfter * 1_000;
  }
  return 60_000;
}
