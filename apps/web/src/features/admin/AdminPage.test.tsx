import { fireEvent, render, screen, waitFor } from "@testing-library/react";

import * as client from "../../api/client";
import * as ops from "../../api/operations";
import { AdminPage } from "./AdminPage";

describe("AdminPage", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    window.history.replaceState({}, "", "/");
  });

  it("blocks non-admin users cleanly", async () => {
    window.history.replaceState({}, "", "/admin?lang=en");
    vi.spyOn(client, "getSession").mockResolvedValue({
      is_admin: false,
    });

    render(<AdminPage />);
    expect(
      await screen.findByText("Administrator access required"),
    ).toBeInTheDocument();
  });

  it("renders multi-tenant admin tabs, reveals API keys, and switches profile scopes", async () => {
    window.history.replaceState({}, "", "/admin?lang=en");
    vi.spyOn(client, "getSession").mockResolvedValue({
      is_admin: true,
    });
    vi.spyOn(ops, "listUsers").mockResolvedValue({
      users: [
        {
          username: "alex",
          schema_name: "health_alex",
          api_key_masked: "sk-****1234",
          email: "alex@example.com",
          is_admin: true,
        },
      ],
    });
    vi.spyOn(ops, "getUserAPIKey").mockResolvedValue({
      username: "alex",
      api_key: "sk-secret-full-key",
    });
    const statusSpy = vi.spyOn(ops, "getCacheStatus").mockResolvedValue({
      raw_points: { rows: 1000, metrics: 12, oldest: "2026-01-01", newest: "2026-11-24" },
      minute_cache: { rows: 500, metrics: 10, oldest: "2026-01-01", newest: "2026-11-24" },
      hourly_cache: { rows: 200, metrics: 10, oldest: "2026-01-01", newest: "2026-11-24" },
      daily_scores: { rows: 90, oldest: "2026-08-25", newest: "2026-11-24" },
      score_version: 5,
      telegram_enabled: true,
      redesign_storage: { healthy: true },
    });
    vi.spyOn(ops, "getCheckinCoverage").mockResolvedValue({
      days: 14,
      from: "2026-11-11",
      to: "2026-11-24",
      source: "telegram",
      sla_active: false,
      summary: {
        total_days: 14,
        prompted: 12,
        answered: 10,
        late_answered: 0,
        expired: 2,
        missing: 2,
        pending: 0,
        prompted_coverage_percent: 86,
        answered_coverage_percent: 71,
        answer_counts: { great: 4, ok: 6 },
      },
      sla_summary: {
        total_days: 0,
        prompted: 0,
        answered: 0,
        late_answered: 0,
        expired: 0,
        missing: 0,
        pending: 0,
        prompted_coverage_percent: 0,
        answered_coverage_percent: 0,
        answer_counts: {},
      },
      rows: [],
      sla_rows: [],
    });
    vi.spyOn(ops, "getTodayInsightsConfig").mockResolvedValue({
      schema: "health_alex",
      b0_enabled: true,
      b1_enabled: false,
      b1_preview_enabled: true,
      b1_narrative_mode: "preview",
      b1_quality_gate_approved: true,
      b1_quality_gate_matches_active_ai: true,
    });

    render(<AdminPage />);

    expect(await screen.findByText("sk-****1234")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Reveal" }));
    expect(await screen.findByText("sk-secret-full-key")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("tab", { name: "alex" }));
    await waitFor(() => {
      expect(statusSpy).toHaveBeenCalledWith("health_alex", expect.any(AbortSignal));
    });
    expect(await screen.findByText("metric_points")).toBeInTheDocument();
  });
});
