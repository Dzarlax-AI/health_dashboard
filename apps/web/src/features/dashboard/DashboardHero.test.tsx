import { render, screen } from "@testing-library/react";

import type { TodayInsightsResponse } from "../../api/client";
import { DashboardHero } from "./DashboardHero";
import type { DashboardViewModel } from "./model";

const model: DashboardViewModel = {
  alerts: [],
  date: "not-a-date",
  degradedResources: [],
  detail: "Measured effort is useful today.",
  metricCards: [],
  sections: [],
  state: "ready",
  title: "Move with confidence",
  readiness: {
    label: "Fair",
    status: "Current",
    tone: "readiness",
    value: 65,
  },
};

describe("DashboardHero", () => {
  it("falls back to Today when the API date is invalid", () => {
    render(<DashboardHero locale="en" model={model} />);

    expect(screen.getByText("Today")).toBeInTheDocument();
  });

  it("uses the server-owned primary insight instead of duplicating TodayGuidance", () => {
    const todayInsights: TodayInsightsResponse = {
      changes: [], date: "2026-08-02", decision_id: "fixture", domains: [], evidence: [],
      generation: { fresh_for_snapshot: true, narrative_mode: "disabled", state: "disabled" }, has_more: false,
      primary: { answer_kind: "factual_context", evidence_ids: [], fallback: true, meaning: "Keep the effort controlled.", next_step: { id: "moderate", text: "Measured day" }, observation: "Recovery supports a measured day.", state: "insight", title: "Today" }, snapshot_version: "fixture",
    };
    render(<DashboardHero locale="en" model={{ ...model, todayInsights }} />);

    expect(screen.getByRole("heading", { name: "Recovery supports a measured day." })).toBeInTheDocument();
    expect(screen.getByText("Measured day")).toBeInTheDocument();
    expect(screen.queryByText(model.title)).not.toBeInTheDocument();
  });

  it("shows an independent AI opinion alongside the server action", () => {
    const todayInsights: TodayInsightsResponse = {
      changes: [], date: "2026-08-02", decision_id: "fixture", domains: [], evidence: [],
      generation: { fresh_for_snapshot: true, narrative_mode: "disabled", state: "ready" }, has_more: false,
      primary: {
        answer_kind: "factual_context", evidence_ids: ["recovery"], fallback: true,
        meaning: "Keep the effort controlled.",
        next_step: { id: "moderate", text: "Measured day" }, observation: "Recovery supports a measured day.", state: "insight", title: "Today",
      }, ai_insight: { text: "The context supports a measured day, but ordinary plans can still fit.", stance: "qualify", alternative_action: "Take an easy walk.", fact_ids: ["recovery"], evidence_ids: ["recovery"] }, snapshot_version: "fixture",
    };
    render(<DashboardHero locale="en" model={{ ...model, todayInsights }} />);

    expect(screen.getByText("The context supports a measured day, but ordinary plans can still fit.")).toBeInTheDocument();
    expect(screen.getByText("Keep the effort controlled.")).toBeInTheDocument();
    expect(screen.getByText("Measured day")).toBeInTheDocument();
    expect(screen.getByText(/Take an easy walk/)).toBeInTheDocument();
    expect(screen.getByText("Server Insight")).toBeInTheDocument();
    expect(screen.getByText("AI Insight")).toBeInTheDocument();
  });
});
