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
});
