import type { AIBriefingResponse, TodayInsightsResponse } from "../../api/client";
import { maxAIPollAttempts, shouldPollAI, shouldPollTodayInsights, todayInsightsPollDelayMs } from "./aiPolling";

function briefing(
  overrides: Partial<AIBriefingResponse> = {},
): AIBriefingResponse {
  return {
    blocks: {},
    date: "2026-08-02",
    disabled: false,
    fresh_for_decision: false,
    generating: false,
    insight: "",
    lang: "en",
    recommendation: "",
    recovery: "",
    sections: [],
    sleep: "",
    summary: "",
    yesterday: "",
    ...overrides,
  };
}

describe("AI polling policy", () => {
  it("polls a cold cache even before generation is reported", () => {
    expect(shouldPollAI(briefing(), 0)).toBe(true);
  });

  it("polls while generation is active", () => {
    expect(shouldPollAI(briefing({ generating: true, insight: "cached" }), 0)).toBe(true);
  });

  it("stops for a populated or disabled cache", () => {
    expect(shouldPollAI(briefing({ insight: "Ready" }), 0)).toBe(false);
    expect(shouldPollAI(briefing({ disabled: true }), 0)).toBe(false);
  });

  it("caps cold-cache polling attempts", () => {
    expect(shouldPollAI(briefing(), maxAIPollAttempts)).toBe(false);
  });
});

function todayInsights(
  state: TodayInsightsResponse["generation"]["state"],
  freshForSnapshot = true,
): TodayInsightsResponse {
  return {
    changes: [],
    date: "2026-08-02",
    decision_id: "fixture",
    domains: [],
    evidence: [],
    generation: { fresh_for_snapshot: freshForSnapshot, state },
    has_more: false,
    primary: { answer_kind: "factual_context", evidence_ids: [], fallback: true, meaning: "", observation: "", state: "insight", title: "Today" },
    snapshot_version: "fixture",
  };
}

describe("Today Insights polling policy", () => {
  it("polls only while the factual snapshot needs a provider acknowledgement", () => {
    expect(shouldPollTodayInsights(todayInsights("cold"), 0)).toBe(true);
    expect(shouldPollTodayInsights(todayInsights("generating"), 0)).toBe(true);
    expect(shouldPollTodayInsights(todayInsights("ready", false), 0)).toBe(true);
  });

  it("stops for ready, disabled, unavailable, and capped states", () => {
    expect(shouldPollTodayInsights(todayInsights("ready"), 0)).toBe(false);
    expect(shouldPollTodayInsights(todayInsights("disabled"), 0)).toBe(false);
    expect(shouldPollTodayInsights(undefined, 0)).toBe(false);
    expect(shouldPollTodayInsights(todayInsights("cold"), maxAIPollAttempts)).toBe(false);
  });

  it("waits for the server backoff before retrying a failed acknowledgement", () => {
    const failed = todayInsights("failed");
    failed.generation.retry_after_seconds = 300;

    expect(shouldPollTodayInsights(failed, 0)).toBe(true);
    expect(todayInsightsPollDelayMs(failed, 0)).toBe(300_000);
  });
});
