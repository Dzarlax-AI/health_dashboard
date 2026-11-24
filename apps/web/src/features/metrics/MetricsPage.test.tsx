import { fireEvent, render, screen } from "@testing-library/react";

import * as client from "../../api/client";
import * as ops from "../../api/operations";
import { MetricsPage } from "./MetricsPage";

describe("MetricsPage", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    window.history.replaceState({}, "", "/");
  });

  it("groups metrics by category, displays latest values, and filters by search", async () => {
    window.history.replaceState({}, "", "/metrics?lang=en");
    vi.spyOn(ops, "listMetrics").mockResolvedValue([
      {
        name: "step_count",
        display_name: "Steps",
        units: "count",
        count: 100,
        min: "2026-01-01",
        max: "2026-11-24",
      },
      {
        name: "heart_rate",
        display_name: "Heart Rate",
        units: "bpm",
        count: 500,
        min: "2026-01-01",
        max: "2026-11-24",
      },
      {
        name: "custom_sensor_metric",
        display_name: "Custom Sensor",
        units: "u",
        count: 10,
        min: "2026-11-01",
        max: "2026-11-24",
      },
    ]);
    vi.spyOn(ops, "getLatestMetricValues").mockResolvedValue([
      { metric: "step_count", value: 12450, unit: "count", date: "2026-11-24" },
      { metric: "heart_rate", value: 62, unit: "bpm", date: "2026-11-24" },
    ]);
    vi.spyOn(client, "getSession").mockResolvedValue({
      is_admin: true,
    });

    render(<MetricsPage />);

    expect(await screen.findByText("Steps")).toBeInTheDocument();
    expect(screen.getByText("12.4k")).toBeInTheDocument();
    expect(screen.getByText("Heart Rate")).toBeInTheDocument();
    expect(screen.getByText("Custom Sensor")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Steps/i })).toHaveAttribute(
      "href",
      "/metrics/step_count?lang=en",
    );

    const search = screen.getByRole("searchbox");
    fireEvent.change(search, { target: { value: "heart" } });
    expect(screen.getByText("Heart Rate")).toBeInTheDocument();
    expect(screen.queryByText("Steps")).not.toBeInTheDocument();
  });
});
