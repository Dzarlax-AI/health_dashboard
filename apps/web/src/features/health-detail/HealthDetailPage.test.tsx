import { act, fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { ClientApiError, getAIBriefing } from "../../api/client";
import { healthSectionConfigs } from "./config";
import { healthDetailFixtureResources } from "./fixtures";
import { HealthDetailPage, HealthDetailReady } from "./HealthDetailPage";
import { loadHealthDetailResources } from "./loader";

vi.mock("../../api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../api/client")>();
  return { ...actual, getAIBriefing: vi.fn() };
});

vi.mock("./loader", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./loader")>();
  return { ...actual, loadHealthDetailResources: vi.fn() };
});

vi.mock("../../components/charts/LazyTrendChart", () => ({
  LazyTrendChart: ({ ariaLabel, data }: { ariaLabel: string; data: unknown[] }) => (
    <div data-testid={`trend-${ariaLabel}`} data-points={data.length} />
  ),
}));

const mockedGetAIBriefing = vi.mocked(getAIBriefing);
const mockedLoadHealthDetailResources = vi.mocked(loadHealthDetailResources);

describe("HealthDetailPage", () => {
  afterEach(() => {
    mockedGetAIBriefing.mockReset();
    mockedLoadHealthDetailResources.mockReset();
    window.history.replaceState({}, "", "/");
    vi.unstubAllEnvs();
    vi.useRealTimers();
  });

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

  it("filters the production-localized VO2 detail from the KPI strip", () => {
    const config = healthSectionConfigs.cardio;
    const fixture = healthDetailFixtureResources(config, "ru", "normal");
    fixture.section!.details = [
      { label: "МПК (VO2 Max)", value: "34,4 ml/kg/min", trend: "stable" },
      { label: "Кислород крови", value: "96,5%", trend: "stable" },
    ];
    const { container } = render(
      <HealthDetailReady config={config} locale="ru" resources={fixture} />,
    );

    expect(container.querySelectorAll(".health-detail-kpis article")).toHaveLength(1);
    expect(screen.getByText("Кислород крови")).toBeInTheDocument();
  });

  it("renders the protected sign-in destination after a 401", async () => {
    vi.stubEnv("VITE_ENABLE_FIXTURES", "false");
    window.history.replaceState({}, "", "/activity?lang=en");
    mockedLoadHealthDetailResources.mockRejectedValue(
      new ClientApiError(401, "authentication required"),
    );

    render(<HealthDetailPage config={healthSectionConfigs.activity} />);

    expect(await screen.findByRole("link", { name: "Sign in" })).toHaveAttribute(
      "href",
      "/login?next=%2Factivity%3Flang%3Den",
    );
  });

  it("keeps the loading state visible while the request is pending", () => {
    vi.stubEnv("VITE_ENABLE_FIXTURES", "false");
    window.history.replaceState({}, "", "/activity?lang=en");
    mockedLoadHealthDetailResources.mockReturnValue(new Promise(() => undefined));

    render(<HealthDetailPage config={healthSectionConfigs.activity} />);

    expect(screen.getByText("Refreshing today")).toBeInTheDocument();
  });

  it("retries a failed load and renders the recovered page", async () => {
    vi.stubEnv("VITE_ENABLE_FIXTURES", "false");
    window.history.replaceState({}, "", "/cardio?lang=en");
    mockedLoadHealthDetailResources
      .mockRejectedValueOnce(new Error("temporary failure"))
      .mockResolvedValueOnce(
        healthDetailFixtureResources(healthSectionConfigs.cardio, "en", "normal"),
      );

    render(<HealthDetailPage config={healthSectionConfigs.cardio} />);
    fireEvent.click(await screen.findByRole("button", { name: "Try again" }));

    expect(await screen.findByRole("heading", { level: 1, name: "Heart & breathing" })).toBeInTheDocument();
    expect(mockedLoadHealthDetailResources).toHaveBeenCalledTimes(2);
  });

  it("polls a cold recovery AI cache and publishes the saved block", async () => {
    vi.useFakeTimers();
    vi.stubEnv("VITE_ENABLE_FIXTURES", "false");
    window.history.replaceState({}, "", "/recovery?lang=en");
    const initial = healthDetailFixtureResources(
      healthSectionConfigs.recovery,
      "en",
      "normal",
    );
    initial.ai = {
      ...initial.ai!,
      generating: true,
      blocks: {},
      recovery: "",
    };
    mockedLoadHealthDetailResources.mockResolvedValue(initial);
    mockedGetAIBriefing.mockResolvedValue({
      ...initial.ai,
      generating: false,
      blocks: { RECOVERY: "Fresh recovery insight" },
    } as NonNullable<typeof initial.ai>);

    render(<HealthDetailPage config={healthSectionConfigs.recovery} />);
    await act(async () => Promise.resolve());
    expect(screen.getByRole("heading", { level: 1, name: "Recovery" })).toBeInTheDocument();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });

    expect(screen.getByText("Fresh recovery insight")).toBeInTheDocument();
    expect(mockedGetAIBriefing).toHaveBeenCalledTimes(1);
  });
});
