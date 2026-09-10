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
    todayInsights: vi.fn<DashboardLoaders["todayInsights"]>().mockResolvedValue({
      changes: [],
      date: "2026-08-02",
      decision_id: "fixture",
      domains: [],
      evidence: [],
      generation: { fresh_for_snapshot: true, state: "disabled" },
      has_more: false,
      primary: { evidence_ids: [], fallback: true, meaning: "", observation: "", state: "no_material_change", title: "Today" },
      snapshot_version: "fixture",
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
