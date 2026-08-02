import type { AIBriefingResponse } from "../../api/client";

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
