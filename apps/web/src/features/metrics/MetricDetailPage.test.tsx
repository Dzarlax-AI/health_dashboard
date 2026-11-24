import { fireEvent, render, screen, waitFor } from "@testing-library/react";

import * as client from "../../api/client";
import * as ops from "../../api/operations";
import {
  alignComparisonSeries,
  buildMetricCsv,
  computeSeriesStats,
  shiftDateIso,
} from "./catalog";
import { MetricDetailPage } from "./MetricDetailPage";

describe("MetricDetailPage", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    window.history.replaceState({}, "", "/");
  });

  it("sends explicit earliest from date on All preset and supports by_source", async () => {
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
      expect.objectContaining({ view: "quantity", bySource: false }),
      expect.any(AbortSignal),
    );

    fireEvent.click(screen.getByRole("button", { name: "All" }));
    await waitFor(() => {
      expect(querySpy).toHaveBeenCalledWith(
        "step_count",
        expect.objectContaining({ from: "2024-01-01" }),
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

  it("loads /metrics/readiness using getReadinessHistory points[].score", async () => {
    window.history.replaceState({}, "", "/metrics/readiness?lang=en");
    vi.spyOn(client, "getSession").mockResolvedValue({ is_admin: false });
    const readinessSpy = vi.spyOn(client, "getReadinessHistory").mockResolvedValue({
      points: [
        { date: "2026-11-22", score: 74, band: "fair" },
        { date: "2026-11-23", score: 86, band: "optimal" },
      ],
    });

    render(<MetricDetailPage metricName="readiness" />);

    expect(await screen.findByRole("heading", { level: 1, name: "Readiness" })).toBeInTheDocument();
    await waitFor(() => {
      expect(readinessSpy).toHaveBeenCalled();
    });
    expect(await screen.findByText("80")).toBeInTheDocument();
  });

  it("keeps sleep_deep statistics specific to sleep_deep and surfaces phase errors", async () => {
    window.history.replaceState({}, "", "/metrics/sleep_deep?lang=en");
    vi.spyOn(client, "getSession").mockResolvedValue({ is_admin: false });
    vi.spyOn(ops, "listMetrics").mockResolvedValue([]);
    vi.spyOn(ops, "queryMetricRange").mockResolvedValue({
      min: "2026-11-01",
      max: "2026-11-24",
    });
    vi.spyOn(ops, "queryMetricData").mockImplementation(async (metric) => ({
      metric,
      bucket: "day",
      agg: "SUM",
      points: [{ date: "2026-11-23", qty: metric === "sleep_deep" ? 1.5 : 3.0 }],
    }));

    const { unmount } = render(<MetricDetailPage metricName="sleep_deep" />);
    expect(await screen.findAllByText("1.5")).not.toHaveLength(0);
    unmount();

    vi.spyOn(ops, "queryMetricData").mockRejectedValue(
      new client.ClientApiError(500, "phase load failed"),
    );
    render(<MetricDetailPage metricName="sleep_deep" />);
    expect(await screen.findByText("phase load failed")).toBeInTheDocument();
  });

  it("aligns comparison points by shifted bucket offset rather than array index", () => {
    const aligned = alignComparisonSeries(
      [
        { date: "2026-11-11", qty: 10 },
        { date: "2026-11-12", qty: 20 },
        { date: "2026-11-13", qty: 30 },
      ],
      [
        { date: "2026-11-08", qty: 100 },
        // missing 2026-11-09
        { date: "2026-11-10", qty: 300 },
      ],
      3,
      "day",
    );
    expect(aligned.map((row) => ({ d: row.rawDate, v: row.value, c: row.compare }))).toEqual([
      { d: "2026-11-11", v: 10, c: 100 },
      { d: "2026-11-12", v: 20, c: undefined },
      { d: "2026-11-13", v: 30, c: 300 },
    ]);
  });

  it("computes UTC date shifts, series stats, and CSV output cleanly", () => {
    expect(shiftDateIso("2026-11-24", 0)).toBe("2026-11-24");
    expect(shiftDateIso("2026-11-24", -1)).toBe("2026-11-23");

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
