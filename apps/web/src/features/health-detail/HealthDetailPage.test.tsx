import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { healthSectionConfigs } from "./config";
import { healthDetailFixtureResources } from "./fixtures";
import { HealthDetailReady } from "./HealthDetailPage";

vi.mock("../../components/charts/LazyTrendChart", () => ({
  LazyTrendChart: ({ ariaLabel, data }: { ariaLabel: string; data: unknown[] }) => (
    <div data-testid={`trend-${ariaLabel}`} data-points={data.length} />
  ),
}));

describe("HealthDetailPage", () => {
  it.each([
    ["activity", "Активность"],
    ["cardio", "Сердце и дыхание"],
    ["recovery", "Восстановление"],
  ] as const)("renders the shared %s detail structure", (key, title) => {
    const config = healthSectionConfigs[key];
    const { container } = render(
      <HealthDetailReady
        config={config}
        locale="ru"
        resources={healthDetailFixtureResources(config, "ru", "normal")}
      />,
    );

    expect(screen.getByRole("heading", { level: 1, name: title })).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 2, name: "История" })).toBeInTheDocument();
    expect(container.querySelector(".health-detail-kpis")).toBeInTheDocument();
    expect(container.querySelectorAll(".health-detail-kpis article")).toHaveLength(2);
    expect(container.querySelector(".health-detail-explainers")).toBeInTheDocument();
  });

  it("switches every trend from 30 days to complete available history", () => {
    const config = healthSectionConfigs.cardio;
    render(
      <HealthDetailReady
        config={config}
        locale="en"
        resources={healthDetailFixtureResources(config, "en", "normal")}
      />,
    );

    expect(screen.getByTestId("trend-VO₂ max")).toHaveAttribute("data-points", "30");
    fireEvent.click(screen.getByRole("button", { name: "All" }));
    expect(screen.getByTestId("trend-VO₂ max")).toHaveAttribute("data-points", "90");
  });

  it("shows an explicit missing state instead of a zero", () => {
    const config = healthSectionConfigs.activity;
    render(
      <HealthDetailReady
        config={config}
        locale="en"
        resources={healthDetailFixtureResources(config, "en", "empty")}
      />,
    );

    expect(screen.getByText("—")).toBeInTheDocument();
    expect(screen.getByText("A personal baseline is still forming.")).toBeInTheDocument();
    expect(screen.getByText("There is not enough history for this signal yet.")).toBeInTheDocument();
  });
});
