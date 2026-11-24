import { fireEvent, render, screen, waitFor } from "@testing-library/react";

import * as client from "../../api/client";
import * as ops from "../../api/operations";
import { buildMetricCsv, computeSeriesStats } from "./catalog";
import { MetricDetailPage } from "./MetricDetailPage";

describe("MetricDetailPage", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    window.history.replaceState({}, "", "/");
  });

  it("queries unified API v2 with range=all on All preset and supports by_source", async () => {
    window.history.replaceState({}, "", "/metrics/step_count?lang=en");
    vi.spyOn(client, "getSession").mockResolvedValue({ is_admin: false });
    vi.spyOn(ops, "listMetrics").mockResolvedValue([
      {
        name: "step_count",
        display_name: "Steps",
        units: "count",
        count: 100,
        min: "2024-01-01",
        max: "2026-11-24",
      },
    ]);
    vi.spyOn(ops, "queryMetricRange").mockResolvedValue({
      min: "2024-01-01",
      max: "2026-11-24",
    });
    const querySpy = vi.spyOn(ops, "queryMetricData").mockResolvedValue({
      metric: "step_count",
      bucket: "day",
      agg: "SUM",
      points: [
        { date: "2026-11-22", qty: 8000 },
        { date: "2026-11-23", qty: 10000 },
      ],
    });

    render(<MetricDetailPage metricName="step_count" />);

    expect(await screen.findByRole("heading", { level: 1, name: "Steps" })).toBeInTheDocument();
    expect(querySpy).toHaveBeenCalledWith(
      "step_count",
      expect.objectContaining({ view: "quantity", bySource: false, rangeAll: false }),
      expect.any(AbortSignal),
    );

    fireEvent.click(screen.getByRole("button", { name: "All" }));
    await waitFor(() => {
      expect(querySpy).toHaveBeenCalledWith(
        "step_count",
        expect.objectContaining({ rangeAll: true, from: undefined }),
        expect.any(AbortSignal),
      );
    });

    fireEvent.click(screen.getByRole("button", { name: "By source" }));
    await waitFor(() => {
      expect(querySpy).toHaveBeenCalledWith(
        "step_count",
        expect.objectContaining({ bySource: true, view: "full" }),
        expect.any(AbortSignal),
      );
    });
  });

  it("computes series stats and builds CSV output cleanly", () => {
    const stats = computeSeriesStats([
      { date: "2026-11-22", qty: 10, min: 8, max: 12 },
      { date: "2026-11-23", qty: 20, min: 15, max: 25 },
    ]);
    expect(stats).toEqual({ count: 2, avg: 15, min: 8, max: 25, sum: 30 });

    const csv = buildMetricCsv("heart_rate", [
      { date: "2026-11-22", qty: 62, min: 55, max: 70 },
    ]);
    expect(csv).toBe("metric,date,value,min,max\nheart_rate,2026-11-22,62,55,70");
  });
});
