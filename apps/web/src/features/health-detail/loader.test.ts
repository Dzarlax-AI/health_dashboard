import { describe, expect, it, vi } from "vitest";

import type {
  AIBriefingResponse,
  HealthBriefingResponse,
  MetricDataResponse,
  MetricRangeResponse,
  ReadinessHistoryResponse,
  SectionResponse,
  SessionResponse,
} from "../../api/client";
import { healthSectionConfigs } from "./config";
import { loadHealthDetailResources, type HealthDetailLoaders } from "./loader";

function loaders(section: SectionResponse): HealthDetailLoaders {
  return {
    briefing: vi.fn(async () => ({ date: "2026-08-06" }) as HealthBriefingResponse),
    section: vi.fn(async () => section),
    ai: vi.fn(async () => ({
      blocks: { RECOVERY: "Saved recovery insight" },
    }) as unknown as AIBriefingResponse),
    session: vi.fn(async () => ({ is_admin: true }) as SessionResponse),
    range: vi.fn(async () => ({
      min: "2026-01-01",
      max: "2026-08-06",
    }) as MetricRangeResponse),
    readiness: vi.fn(async () => ({ points: [] }) as ReadinessHistoryResponse),
    metric: vi.fn(async (metric, _from, _to, _signal, options) => ({
      metric,
      agg: options?.agg ?? "AVG",
      bucket: "day",
      points: [],
    }) as MetricDataResponse),
  };
}

describe("health detail resource loader", () => {
  it("preserves SUM aggregation for activity metrics", async () => {
    const testLoaders = loaders({
      key: "activity",
      title: "Activity",
      summary: "",
      details: [],
      explains: [],
      charts: [
        { metric: "step_count", agg: "SUM", label: "Steps", type: "bar" },
        { metric: "active_energy", agg: "SUM", label: "Energy", type: "bar" },
      ],
    });

    await loadHealthDetailResources(healthSectionConfigs.activity, "en", undefined, testLoaders);

    expect(testLoaders.metric).toHaveBeenCalledWith(
      "step_count",
      "2026-01-01",
      "2026-08-06",
      undefined,
      { agg: "SUM", bucket: "day" },
    );
    expect(testLoaders.metric).toHaveBeenCalledWith(
      "active_energy",
      "2026-01-01",
      "2026-08-06",
      undefined,
      { agg: "SUM", bucket: "day" },
    );
    expect(testLoaders.ai).not.toHaveBeenCalled();
  });

  it("loads recovery AI and readiness without blocking on a failed metric", async () => {
    const testLoaders = loaders({
      key: "recovery",
      title: "Recovery",
      summary: "",
      details: [],
      explains: [],
      charts: [
        { metric: "heart_rate_variability", agg: "AVG", label: "HRV" },
        { label: "Readiness", virtual: true },
      ],
    });
    testLoaders.metric = vi.fn(async () => {
      throw new Error("metric unavailable");
    });

    const result = await loadHealthDetailResources(
      healthSectionConfigs.recovery,
      "en",
      undefined,
      testLoaders,
    );

    expect(testLoaders.ai).toHaveBeenCalledWith("en", undefined);
    expect(testLoaders.readiness).toHaveBeenCalledWith(365, undefined);
    expect(result.ai?.blocks.RECOVERY).toBe("Saved recovery insight");
    expect(result.missing).toContain("heart_rate_variability");
  });

  it("rejects an aborted load instead of publishing partial resources", async () => {
    const controller = new AbortController();
    const testLoaders = loaders({
      key: "cardio",
      title: "Cardio",
      summary: "",
      details: [],
      explains: [],
      charts: [{ metric: "vo2_max", agg: "AVG", label: "VO2" }],
    });
    testLoaders.range = vi.fn(async () => {
      controller.abort(new DOMException("Superseded", "AbortError"));
      return { min: "2026-01-01", max: "2026-08-06" };
    });

    await expect(
      loadHealthDetailResources(
        healthSectionConfigs.cardio,
        "en",
        controller.signal,
        testLoaders,
      ),
    ).rejects.toMatchObject({ name: "AbortError" });
  });
});
