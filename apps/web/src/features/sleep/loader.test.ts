import { describe, expect, it, vi } from "vitest";

import type {
  AIBriefingResponse,
  DerivedMetricsResponse,
  HealthBriefingResponse,
  MetricDataResponse,
  MetricRangeResponse,
  SectionResponse,
  SessionResponse,
} from "../../api/client";
import { loadSleepResources, sleepMetrics, type SleepLoaders } from "./loader";

function metricResponse(metric = "sleep_total"): MetricDataResponse {
  return {
    metric,
    agg: "AVG",
    bucket: "day",
    points: [],
  };
}

function loaders(overrides: Partial<SleepLoaders> = {}): SleepLoaders {
  return {
    briefing: vi.fn(async () => ({ date: "2026-08-05" }) as HealthBriefingResponse),
    section: vi.fn(async () => ({}) as SectionResponse),
    ai: vi.fn(async () => ({}) as AIBriefingResponse),
    range: vi.fn(async () => ({ min: "2020-01-01", max: "2026-08-05" }) as MetricRangeResponse),
    wake: vi.fn(async () => ({}) as DerivedMetricsResponse),
    session: vi.fn(async () => ({}) as SessionResponse),
    metric: vi.fn(async (metric) => metricResponse(metric)),
    ...overrides,
  };
}

describe("sleep resource loader", () => {
  it("keeps useful resources and skips date-dependent requests when briefing date is empty", async () => {
    const testLoaders = loaders({
      briefing: vi.fn(async () => ({ date: "" }) as HealthBriefingResponse),
    });

    const result = await loadSleepResources("en", undefined, testLoaders);

    expect(result.briefing.date).toBe("");
    expect(result.metrics).toEqual({});
    expect(testLoaders.wake).not.toHaveBeenCalled();
    expect(testLoaders.metric).not.toHaveBeenCalled();
  });

  it("loads the complete available sleep range for the All period", async () => {
    const testLoaders = loaders();

    await loadSleepResources("en", undefined, testLoaders);

    expect(testLoaders.range).toHaveBeenCalledWith("sleep_total", undefined);
    expect(testLoaders.wake).toHaveBeenCalledWith(
      "wake_time",
      "2020-01-01",
      "2026-08-05",
      undefined,
    );
    expect(testLoaders.metric).toHaveBeenCalledTimes(sleepMetrics.length);
    for (const metric of sleepMetrics) {
      expect(testLoaders.metric).toHaveBeenCalledWith(
        metric,
        "2020-01-01",
        "2026-08-05",
        undefined,
      );
    }
  });

  it("retries sleep_total once without discarding the other phase metrics", async () => {
    let totalAttempts = 0;
    const testLoaders = loaders({
      metric: vi.fn(async (metric) => {
        if (metric === "sleep_total" && totalAttempts++ === 0) {
          throw new Error("temporary failure");
        }
        return metricResponse(metric);
      }),
    });

    const result = await loadSleepResources("en", undefined, testLoaders);

    expect(totalAttempts).toBe(2);
    expect(result.metrics.sleep_total?.metric).toBe("sleep_total");
    expect(result.missing).not.toContain("sleep_total");
  });

  it("rejects an aborted load instead of publishing partial settled results", async () => {
    const controller = new AbortController();
    const testLoaders = loaders({
      metric: vi.fn(async () => {
        controller.abort(new DOMException("Superseded", "AbortError"));
        return metricResponse();
      }),
    });

    await expect(loadSleepResources("en", controller.signal, testLoaders)).rejects.toMatchObject({
      name: "AbortError",
    });
  });
});
