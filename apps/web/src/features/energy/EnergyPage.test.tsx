import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { getEnergyHistory, getSession, getTodayInsights } from "../../api/client";
import { fixtureResources } from "../dashboard/fixtures";
import { EnergyPage } from "./EnergyPage";

vi.mock("../../api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../api/client")>();
  return { ...actual, getTodayInsights: vi.fn(), getEnergyHistory: vi.fn(), getSession: vi.fn() };
});

describe("EnergyPage", () => {
  afterEach(() => {
    vi.mocked(getTodayInsights).mockReset();
    vi.mocked(getEnergyHistory).mockReset();
    vi.mocked(getSession).mockReset();
    window.history.replaceState({}, "", "/");
  });

  it("shows the server insight without waiting for optional history and session", async () => {
    window.history.replaceState({}, "", "/energy?lang=ru");
    vi.mocked(getTodayInsights).mockResolvedValue(fixtureResources("ru", "normal").todayInsights!);
    vi.mocked(getEnergyHistory).mockImplementation(() => new Promise(() => undefined));
    vi.mocked(getSession).mockImplementation(() => new Promise(() => undefined));

    render(<EnergyPage />);

    expect(await screen.findByText("Запас доступен.")).toBeInTheDocument();
    expect(screen.getByText("Инсайт сервера")).toBeInTheDocument();
  });
});
