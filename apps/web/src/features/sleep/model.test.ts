import { describe, expect, it } from "vitest";

import type { SleepResources } from "./loader";
import { buildSleepDays, sleepInsight } from "./model";

function resources(): SleepResources {
  return {
    briefing: { date: "2026-08-05" } as SleepResources["briefing"],
    metrics: {
      sleep_total: {
        metric: "sleep_total",
        bucket: "day",
        agg: "SUM",
        points: [{ date: "2026-08-05", qty: 7.5, min: 7.5, max: 7.5 }],
      },
      sleep_deep: {
        metric: "sleep_deep",
        bucket: "day",
        agg: "SUM",
        points: [{ date: "2026-08-05", qty: 1.1, min: 1.1, max: 1.1 }],
      },
      sleep_unspecified: {
        metric: "sleep_unspecified",
        bucket: "day",
        agg: "SUM",
        points: [{ date: "2026-08-05", qty: 0.4, min: 0.4, max: 0.4 }],
      },
    },
    wake: {
      metric: "wake_time",
      from: "2026-08-05",
      to: "2026-08-05",
      values: [{
        metric_name: "wake_time",
        metric_date: "2026-08-05",
        value_type: "timestamp",
        value_timestamp: "2026-08-05T08:03:00+02:00",
        unit: "timestamp",
        state: "final",
        formula_version: "1",
        calculated_at: "2026-08-05T08:05:00+02:00",
      }],
    },
    missing: [],
  };
}

describe("sleep model", () => {
  it("merges all metric series and derived wake time without parsing labels", () => {
    const [day] = buildSleepDays(resources());
    expect(day).toMatchObject({
      date: "2026-08-05",
      total: 7.5,
      deep: 1.1,
      unspecified: 0.4,
      wake: "2026-08-05T08:03:00+02:00",
    });
  });

  it("derives total sleep from asleep phases when sleep_total is unavailable", () => {
    const input = resources();
    delete input.metrics.sleep_total;
    input.metrics.sleep_rem = {
      metric: "sleep_rem",
      bucket: "day",
      agg: "SUM",
      points: [{ date: "2026-08-05", qty: 1.4, min: 1.4, max: 1.4 }],
    };
    input.metrics.sleep_core = {
      metric: "sleep_core",
      bucket: "day",
      agg: "SUM",
      points: [{ date: "2026-08-05", qty: 4.2, min: 4.2, max: 4.2 }],
    };
    input.metrics.sleep_awake = {
      metric: "sleep_awake",
      bucket: "day",
      agg: "SUM",
      points: [{ date: "2026-08-05", qty: 0.8, min: 0.8, max: 0.8 }],
    };

    const [day] = buildSleepDays(input);

    expect(day.total).toBeCloseTo(7.1);
  });

  it("prefers the scoped SLEEP block and falls back to synthesis", () => {
    expect(sleepInsight({ blocks: { SLEEP: "Scoped" } } as never)).toBe("Scoped");
    expect(sleepInsight({ blocks: { SYNTHESIS: "Overview" } } as never)).toBe("Overview");
  });
});
