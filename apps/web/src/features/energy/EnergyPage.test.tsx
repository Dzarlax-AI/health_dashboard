import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { getEnergyHistory, getHealthBriefing, getSession, getTodayInsights } from "../../api/client";
import { fixtureResources } from "../dashboard/fixtures";
import { EnergyPage } from "./EnergyPage";

vi.mock("../../components/charts/LazyTrendChart", () => ({ LazyTrendChart: () => <div /> }));

vi.mock("../../api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../api/client")>();
  return { ...actual, getTodayInsights: vi.fn(), getEnergyHistory: vi.fn(), getHealthBriefing: vi.fn(), getSession: vi.fn() };
});

afterEach(() => {
  vi.mocked(getTodayInsights).mockReset();
    vi.mocked(getEnergyHistory).mockReset();
    vi.mocked(getSession).mockReset();
    vi.mocked(getHealthBriefing).mockReset();
    window.sessionStorage.clear();
    window.history.replaceState({}, "", "/");
});

describe("EnergyPage", () => {
  it("shows the server insight without waiting for optional history and session", async () => {
    window.history.replaceState({}, "", "/energy?lang=ru");
    vi.mocked(getTodayInsights).mockResolvedValue(fixtureResources("ru", "normal").todayInsights!);
    vi.mocked(getEnergyHistory).mockImplementation(() => new Promise(() => undefined));
    vi.mocked(getSession).mockImplementation(() => new Promise(() => undefined));

    vi.mocked(getHealthBriefing).mockImplementation(() => new Promise(() => undefined));
    render(<EnergyPage />);

    expect(await screen.findByText("Запас доступен.")).toBeInTheDocument();
    expect(screen.getByText("Инсайт сервера")).toBeInTheDocument();
  });
});
const resources = fixtureResources("en", "normal");
it("shows reserve while history and insights are still loading", async () => {
  vi.mocked(getHealthBriefing).mockResolvedValue(resources.briefing);
  vi.mocked(getSession).mockResolvedValue({ is_admin: false });
  vi.mocked(getTodayInsights).mockReturnValue(new Promise(() => {}));
  vi.mocked(getEnergyHistory).mockReturnValue(new Promise(() => {}));
  render(<EnergyPage />);
  expect(await screen.findByText("81")).toBeVisible();
  expect(screen.getByText("Capacity")).toBeVisible();
});

it("keeps history and server insight when current reserve fails", async () => {
  vi.mocked(getHealthBriefing).mockRejectedValue(new Error("offline"));
  vi.mocked(getSession).mockResolvedValue({ is_admin: false });
  vi.mocked(getTodayInsights).mockResolvedValue(resources.todayInsights!);
  vi.mocked(getEnergyHistory).mockResolvedValue(resources.energyHistory!);
  render(<EnergyPage />);
  expect(await screen.findByText("Server Insight")).toBeVisible();
  expect(await screen.findByText("Daily values")).toBeVisible();
  expect(screen.getByTestId("energy-reserve")).toHaveTextContent("—");
});

it("keeps a negative reserve and a zero capacity without fabricating a positive score", async () => {
  vi.mocked(getHealthBriefing).mockResolvedValue({ ...resources.briefing,
    energy_bank: { ...resources.briefing.energy_bank!, current: -12, capacity: 0 } });
  vi.mocked(getSession).mockResolvedValue({ is_admin: false });
  vi.mocked(getTodayInsights).mockRejectedValue(new Error("unavailable"));
  vi.mocked(getEnergyHistory).mockResolvedValue(resources.energyHistory!);
  const { container } = render(<EnergyPage />);
  expect(await screen.findByText("-12")).toBeVisible();
  expect(container.querySelector(".health-detail-gauge")).toHaveStyle("--detail-progress: 0");
});

it("uses the absolute bank scale when the daily capacity is low", async () => {
  vi.mocked(getHealthBriefing).mockResolvedValue({ ...resources.briefing,
    energy_bank: { ...resources.briefing.energy_bank!, current: 10, capacity: 20 } });
  vi.mocked(getTodayInsights).mockResolvedValue(resources.todayInsights!);
  vi.mocked(getSession).mockResolvedValue({ is_admin: false });
  vi.mocked(getEnergyHistory).mockResolvedValue(resources.energyHistory!);
  const { container } = render(<EnergyPage />);
  await screen.findByTestId("energy-reserve");
  expect(await screen.findByTestId("energy-reserve")).toHaveTextContent("10");
  expect(container.querySelector(".health-detail-gauge")).toHaveStyle("--detail-progress: 10");
});

it("shows the briefing date and stale state instead of calling old data current", async () => {
  vi.mocked(getHealthBriefing).mockResolvedValue({ ...resources.briefing, date: "2026-09-01",
    readiness_serving: { ...resources.briefing.readiness_serving!, status: "stale" } });
  vi.mocked(getTodayInsights).mockResolvedValue(resources.todayInsights!);
  vi.mocked(getSession).mockResolvedValue({ is_admin: false });
  vi.mocked(getEnergyHistory).mockResolvedValue(resources.energyHistory!);
  render(<EnergyPage />);
  expect(await screen.findByText("2026-09-01")).toBeVisible();
  expect(screen.getByText("Last known data")).toBeVisible();
  expect(screen.queryByText("Current reserve")).not.toBeInTheDocument();
});

it("exposes a single history day without opening the disclosure", async () => {
  vi.mocked(getHealthBriefing).mockResolvedValue(resources.briefing);
  vi.mocked(getTodayInsights).mockResolvedValue(resources.todayInsights!);
  vi.mocked(getSession).mockResolvedValue({ is_admin: false });
  vi.mocked(getEnergyHistory).mockResolvedValue({ ...resources.energyHistory!, points: [resources.energyHistory!.points![0]] });
  const { container } = render(<EnergyPage />);
  await screen.findByText("Daily values");
  expect(container.querySelector(".energy-history-details")).toHaveAttribute("open");
  expect(container.querySelector(".energy-history-list__row")).toBeVisible();
});
