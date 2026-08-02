import type { DashboardLoaders } from "./loader";
import { loadDashboardResources } from "./loader";

const briefing = {
  date: "2026-08-02",
} as Awaited<ReturnType<DashboardLoaders["briefing"]>>;

function loaders(): DashboardLoaders {
  return {
    briefing: vi.fn<DashboardLoaders["briefing"]>().mockResolvedValue(briefing),
    dashboard: vi.fn<DashboardLoaders["dashboard"]>().mockResolvedValue({
      cards: [],
      date: "",
      last_updated: "",
    }),
    ai: vi.fn<DashboardLoaders["ai"]>().mockResolvedValue({
      blocks: {},
      date: "",
      disabled: false,
      generating: false,
      insight: "",
      lang: "en",
      recommendation: "",
      recovery: "",
      sections: [],
      sleep: "",
      yesterday: "",
    }),
    readinessHistory: vi.fn<DashboardLoaders["readinessHistory"]>().mockResolvedValue({
      points: [],
    }),
    energyHistory: vi.fn<DashboardLoaders["energyHistory"]>().mockResolvedValue({
      granularity: "day",
      points: [],
    }),
    session: vi.fn<DashboardLoaders["session"]>().mockResolvedValue({ is_admin: false }),
  };
}

describe("dashboard resource loader", () => {
  it("keeps the primary briefing usable when optional resources fail", async () => {
    const api = loaders();
    vi.mocked(api.energyHistory).mockRejectedValue(new Error("history unavailable"));
    vi.mocked(api.session).mockRejectedValue(new Error("session unavailable"));

    const result = await loadDashboardResources("en", undefined, api);

    expect(result.briefing).toBe(briefing);
    expect(result.energyHistory).toBeUndefined();
    expect(result.session).toBeUndefined();
    expect(result.missing).toEqual(["energyHistory", "session"]);
  });

  it("rejects when the canonical health briefing cannot load", async () => {
    const api = loaders();
    vi.mocked(api.briefing).mockRejectedValue(new Error("briefing unavailable"));

    await expect(loadDashboardResources("en", undefined, api)).rejects.toThrow(
      "briefing unavailable",
    );
  });
});
