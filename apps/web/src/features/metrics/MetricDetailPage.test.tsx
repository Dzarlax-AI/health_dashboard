import { fireEvent, render, screen, waitFor } from "@testing-library/react";

import * as client from "../../api/client";
import * as ops from "../../api/operations";
import {
  alignComparisonSeries,
  buildMetricCsv,
  buildSleepPhaseRows,
  computeSeriesStats,
  shiftDateIso,
  supportedMetricAggregations,
  todayIso,
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
    expect(screen.queryByRole("button", { name: "All" })).not.toBeInTheDocument();
    expect(
      screen.getByText(/Readiness history is available for the last 365 days/i),
    ).toBeInTheDocument();

    const today = todayIso();
    const minDate = shiftDateIso(today, -364);
    const fromInput = screen.getByLabelText("From date") as HTMLInputElement;
    expect(fromInput.min).toBe(minDate);
    expect(fromInput.max).toBe(today);

    fireEvent.change(fromInput, { target: { value: "2020-01-01" } });
    expect(fromInput.value).toBe(minDate);
  });

  it("fetches stored sleep_total directly, preserves total-only nights, and uses stored totals for stats", async () => {
    window.history.replaceState({}, "", "/metrics/sleep_total?lang=en");
    vi.spyOn(client, "getSession").mockResolvedValue({ is_admin: false });
    vi.spyOn(ops, "listMetrics").mockResolvedValue([
      {
        name: "sleep_total",
        display_name: "Sleep total",
        units: "hr",
        count: 2,
        min: "2026-11-21",
        max: "2026-11-22",
      },
    ]);
    vi.spyOn(ops, "queryMetricRange").mockResolvedValue({
      min: "2026-11-21",
      max: "2026-11-22",
    });
    const querySpy = vi.spyOn(ops, "queryMetricData").mockImplementation(async (metric) => {
      if (metric === "sleep_total") {
        return {
          metric,
          bucket: "day",
          agg: "SUM",
          points: [
            // Night with ONLY stored sleep_total (no stage points)
            { date: "2026-11-21", qty: 7.4 },
            // Night where stored sleep_total (8.2) differs from sum of stages (7.0)
            { date: "2026-11-22", qty: 8.2 },
          ],
        };
      }
      if (metric === "sleep_deep") {
        return { metric, bucket: "day", agg: "SUM", points: [{ date: "2026-11-22", qty: 1.5 }] };
      }
      if (metric === "sleep_core") {
        return { metric, bucket: "day", agg: "SUM", points: [{ date: "2026-11-22", qty: 4.0 }] };
      }
      if (metric === "sleep_rem") {
        return { metric, bucket: "day", agg: "SUM", points: [{ date: "2026-11-22", qty: 1.5 }] };
      }
      return { metric, bucket: "day", agg: "SUM", points: [] };
    });

    render(<MetricDetailPage metricName="sleep_total" />);

    expect(await screen.findByRole("heading", { level: 1, name: "Sleep total" })).toBeInTheDocument();
    expect(querySpy).toHaveBeenCalledWith(
      "sleep_total",
      expect.objectContaining({ bucket: "day", agg: "SUM", view: "quantity" }),
      expect.any(AbortSignal),
    );
    // Stats must reflect stored sleep_total [7.4, 8.2] -> avg 7.8, min 7.4, max 8.2, sum 15.6 -> "16"
    expect(await screen.findByText("7.8 hr")).toBeInTheDocument();
    expect(screen.getByText("7.4 hr")).toBeInTheDocument();
    expect(screen.getByText("8.2 hr")).toBeInTheDocument();
    expect(screen.queryByText("No measurements recorded in this period.")).not.toBeInTheDocument();

    const rows = buildSleepPhaseRows(
      {
        sleep_deep: [{ date: "2026-11-22", qty: 1.5 }],
        sleep_core: [{ date: "2026-11-22", qty: 4.0 }],
        sleep_rem: [{ date: "2026-11-22", qty: 1.5 }],
      },
      [
        { date: "2026-11-21", qty: 7.4 },
        { date: "2026-11-22", qty: 8.2 },
      ],
    );
    expect(rows).toEqual([
      {
        date: "2026-11-21",
        sleep_deep: 0,
        sleep_core: 0,
        sleep_rem: 0,
        sleep_unspecified: 7.4,
        sleep_awake: 0,
      },
      {
        date: "2026-11-22",
        sleep_deep: 1.5,
        sleep_core: 4,
        sleep_rem: 1.5,
        sleep_unspecified: 0,
        sleep_awake: 0,
      },
    ]);
  });

  it("restricts aggregation selector to supported aggregations for SUM and AVG metrics", async () => {
    expect(supportedMetricAggregations("step_count")).toEqual(["", "SUM", "MAX", "MIN"]);
    expect(supportedMetricAggregations("sleep_total")).toEqual(["", "SUM", "MAX", "MIN"]);
    expect(supportedMetricAggregations("heart_rate")).toEqual(["", "AVG", "MAX", "MIN"]);
    expect(supportedMetricAggregations("dietary_energy")).toEqual(["", "AVG", "MAX", "MIN"]);

    vi.spyOn(client, "getSession").mockResolvedValue({ is_admin: false });
    vi.spyOn(ops, "listMetrics").mockResolvedValue([]);
    vi.spyOn(ops, "queryMetricRange").mockResolvedValue({
      min: "2026-11-01",
      max: "2026-11-24",
    });
    vi.spyOn(ops, "queryMetricData").mockResolvedValue({
      metric: "step_count",
      bucket: "day",
      agg: "SUM",
      points: [{ date: "2026-11-23", qty: 9000 }],
    });

    window.history.replaceState({}, "", "/metrics/step_count?lang=en");
    const { unmount } = render(<MetricDetailPage metricName="step_count" />);
    await screen.findByRole("heading", { level: 1, name: "step_count" });
    const stepAggSelect = screen.getByLabelText("Aggregation") as HTMLSelectElement;
    expect(Array.from(stepAggSelect.options).map((o) => o.value)).toEqual([
      "",
      "SUM",
      "MAX",
      "MIN",
    ]);
    unmount();

    window.history.replaceState({}, "", "/metrics/heart_rate?lang=en");
    render(<MetricDetailPage metricName="heart_rate" />);
    await screen.findByRole("heading", { level: 1, name: "heart_rate" });
    const hrAggSelect = screen.getByLabelText("Aggregation") as HTMLSelectElement;
    expect(Array.from(hrAggSelect.options).map((o) => o.value)).toEqual([
      "",
      "AVG",
      "MAX",
      "MIN",
    ]);
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
