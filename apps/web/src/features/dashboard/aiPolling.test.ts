import type { AIBriefingResponse } from "../../api/client";
import { maxAIPollAttempts, shouldPollAI } from "./aiPolling";

function briefing(
  overrides: Partial<AIBriefingResponse> = {},
): AIBriefingResponse {
  return {
    blocks: {},
    date: "2026-08-02",
    disabled: false,
    generating: false,
    insight: "",
    lang: "en",
    recommendation: "",
    recovery: "",
    sections: [],
    sleep: "",
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
