import type { HealthBriefingResponse } from "../../api/client";
import {
  buildDashboardViewModel,
  classifyBriefing,
  clampPercent,
} from "./model";

function briefing(
  overrides: Partial<HealthBriefingResponse> = {},
): HealthBriefingResponse {
  const base: HealthBriefingResponse = {
    date: "2026-08-02",
    greeting: "Good morning",
    overall: "fair",
    readiness_score: 61,
    readiness_band: "fair",
    readiness_label: "Fair",
    readiness_tip: "Keep the effort measured.",
    readiness_raw_score: 66,
    readiness_display_score: 65,
    recovery_pct: 63,
    readiness_today: 65,
    readiness_today_band: "fair",
    readiness_today_label: "Fair",
    readiness_serving: { status: "fresh", confidence: "final" },
    correlation: [],
    highlights: [],
    insights: [],
    metric_cards: [],
    sections: [],
    sleep: null,
  };
  return Object.assign(base, overrides);
}

describe("dashboard model", () => {
  it.each([
    [{ date: "" }, "unavailable"],
    [{ readiness_serving: { status: "missing", confidence: "low" } }, "unavailable"],
    [{ readiness_serving: { status: "stale", confidence: "low" } }, "stale"],
    [{ readiness_serving: { status: "low_coverage", confidence: "final" } }, "partial"],
    [{ readiness_serving: { status: "capped", confidence: "final" } }, "partial"],
    [
      { readiness_serving: { status: "data_accruing", confidence: "provisional" } },
      "partial",
    ],
    [{ readiness_serving: { status: "fresh", confidence: "final" } }, "ready"],
  ] satisfies [Partial<HealthBriefingResponse>, ReturnType<typeof classifyBriefing>][])(
    "classifies canonical readiness serving state",
    (overrides, expected) => {
      expect(classifyBriefing(briefing(overrides))).toBe(expected);
    },
  );

  it("uses readiness_today once and keeps raw EnergyBank semantics", () => {
    const model = buildDashboardViewModel(
      briefing({
        readiness_today: 65,
        readiness_score: 41,
        energy_bank: {
          action_verdict: "moderate",
          capacity: 96,
          current: -18,
          drain_so_far: 49,
          hrv_z_raw: -0.4,
          strain: 12,
          stress: 8,
          verdict_label: "Take it easy",
          verdict_reason: "The bank is below reserve.",
        },
      }),
    );

    expect(model.readiness?.value).toBe(65);
    expect(model.energy).toMatchObject({
      value: 0,
      detail: "-18 / 96",
      status: "The bank is below reserve.",
    });
  });

  it("does not invent sleep quality while the score is provisional", () => {
    const model = buildDashboardViewModel(
      briefing({
        sleep: {
          awake_avg: 0.4,
          deep_avg: 1.1,
          efficiency: 92,
          nights: 1,
          rem_avg: 1.8,
          total_avg: 7.6,
        },
        sleep_quality: {
          confidence: "provisional",
          duration_pct: 91,
          score_pct: 84,
        },
      }),
    );

    expect(model.sleep?.value).toBeUndefined();
    expect(model.sleep?.detail).toBe("7.6 h");
  });

  it("shows only substantive illness suspicion and preserves optional failures", () => {
    const model = buildDashboardViewModel(
      briefing({
        illness_suspicion: {
          confidence: "high",
          date: "2026-08-02",
          experimental: false,
          pattern: "Elevated overnight signals",
          reason: "Several recovery signals moved together.",
        },
      }),
      ["energyHistory"],
    );

    expect(model.illness).toEqual({
      confidence: "high",
      pattern: "Elevated overnight signals",
      reason: "Several recovery signals moved together.",
    });
    expect(model.degradedResources).toEqual(["energyHistory"]);
  });

  it("does not promote low-confidence illness suspicion", () => {
    const model = buildDashboardViewModel(
      briefing({
        illness_suspicion: {
          confidence: "low",
          date: "2026-08-02",
          experimental: false,
          reason: "Weak signal",
        },
      }),
    );

    expect(model.illness).toBeUndefined();
  });

  it("leaves the Energy Bank label empty for the localized card fallback", () => {
    const model = buildDashboardViewModel(
      briefing({
        energy_bank: {
          action_verdict: "moderate",
          capacity: 80,
          current: 40,
          drain_so_far: 20,
          hrv_z_raw: 0,
          strain: 10,
          stress: 10,
          verdict_label: "",
          verdict_reason: "Measured reserve.",
        },
      }),
    );

    expect(model.energy?.label).toBe("");
  });

  it.each([
    [-20, 0],
    [52.6, 53],
    [130, 100],
    [Number.NaN, 0],
  ])("clamps visual percent %s to %s", (value, expected) => {
    expect(clampPercent(value)).toBe(expected);
  });
});
