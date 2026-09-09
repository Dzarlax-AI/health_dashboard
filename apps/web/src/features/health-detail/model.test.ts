import { describe, expect, it } from "vitest";

import type { HealthDetailResources } from "./loader";
import { healthSectionConfigs } from "./config";
import { buildHealthDetailModel, pointsForRange } from "./model";

function resources(overrides: Partial<HealthDetailResources> = {}): HealthDetailResources {
  return {
    briefing: {
      date: "2026-08-06",
      readiness: 55,
      readiness_today: 55,
      sections: [
        {
          key: "recovery",
          title: "Recovery",
          icon: "battery",
          status: "fair",
          status_label: "Fair",
          summary: "Rule summary",
          details: [],
        },
      ],
    } as unknown as HealthDetailResources["briefing"],
    metrics: {},
    missing: [],
    ...overrides,
  };
}

describe("health detail model", () => {
  it("does not turn missing activity data into a zero", () => {
    const model = buildHealthDetailModel(
      resources({
        briefing: {
          date: "2026-08-06",
          sections: [
            {
              key: "activity",
              title: "Activity",
              icon: "activity",
              status: "low",
              summary: "Move more",
              details: [],
            },
          ],
        } as unknown as HealthDetailResources["briefing"],
      }),
      healthSectionConfigs.activity,
      "en",
    );

    expect(model.heroValue).toBe("—");
    expect(model.heroProgress).toBeUndefined();
  });

  it("ignores malformed activity values when building the baseline", () => {
    const points = Array.from({ length: 9 }, (_, index) => ({
      date: `2026-08-${String(index + 1).padStart(2, "0")}`,
      qty: index === 4 ? Number.NaN : 1000 + index * 10,
      min: 0,
      max: 0,
    }));
    const model = buildHealthDetailModel(
      resources({
        briefing: {
          date: "2026-08-09",
          sections: [],
        } as unknown as HealthDetailResources["briefing"],
        metrics: {
          step_count: {
            metric: "step_count",
            agg: "SUM",
            bucket: "day",
            points,
          },
        },
      }),
      healthSectionConfigs.activity,
      "en",
    );

    expect(model.heroValue).toBe("1,080");
    expect(model.heroComparison).toBeTypeOf("number");
    expect(Number.isFinite(model.heroComparison)).toBe(true);
  });

  it("preserves a valid zero readiness score", () => {
    const model = buildHealthDetailModel(
      resources({
        briefing: {
          date: "2026-08-06",
          readiness_today: 0,
          readiness_display_score: 72,
          readiness_score: 68,
          sections: [],
        } as unknown as HealthDetailResources["briefing"],
      }),
      healthSectionConfigs.recovery,
      "en",
    );

    expect(model.heroValue).toBe("0%");
    expect(model.heroProgress).toBe(0);
  });

  it("uses only the exact saved recovery AI block", () => {
    const model = buildHealthDetailModel(
      resources({
        ai: {
          blocks: {
            RECOVERY: "Recovery-specific text",
            YESTERDAY: "Activity and cardio text",
          },
        } as unknown as NonNullable<HealthDetailResources["ai"]>,
      }),
      healthSectionConfigs.recovery,
      "en",
    );

    expect(model.context).toBe("Recovery-specific text");
    expect(model.context).not.toContain("Activity and cardio");
    expect(model.contextIsAI).toBe(true);
  });

  it("slices finite history ranges without padding missing days", () => {
    const points = Array.from({ length: 12 }, (_, index) => ({
      date: `2026-07-${String(index + 1).padStart(2, "0")}`,
      value: index + 1,
    }));

    expect(pointsForRange(points, 7)).toEqual(points.slice(-7));
    expect(pointsForRange(points, "all")).toEqual(points);
  });

  it("filters malformed readiness history points", () => {
    const model = buildHealthDetailModel(
      resources({
        section: {
          key: "recovery",
          title: "Recovery",
          summary: "",
          details: [],
          explains: [],
          charts: [{ label: "Readiness", virtual: true }],
        },
        readiness: {
          points: [
            { date: "2026-08-05", score: 70 },
            { date: "", score: 71 },
            { date: "2026-08-06", score: Number.NaN },
          ],
        },
      }),
      healthSectionConfigs.recovery,
      "en",
    );

    expect(model.trends[0].points).toEqual([{ date: "2026-08-05", value: 70 }]);
  });
});
