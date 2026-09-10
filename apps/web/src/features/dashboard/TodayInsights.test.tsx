import { render, screen } from "@testing-library/react";

import type { TodayInsightsResponse } from "../../api/client";
import { TodayInsights } from "./TodayInsights";

const response: TodayInsightsResponse = {
  changes: [],
  date: "2026-09-10",
  decision_id: "decision",
  domains: [
    {
      band: "good",
      data_state: "fresh",
      destination: { id: "sleep", kind: "sleep" },
      insight: { evidence_ids: ["sleep"], fallback: true, meaning: "Sleep is complete.", observation: "You slept well.", state: "insight", title: "Sleep" },
      key: "sleep",
      summary: "Sleep is stable",
    },
    {
      band: "fair",
      data_state: "partial",
      destination: { id: "recovery", kind: "section" },
      insight: { evidence_ids: ["recovery"], fallback: true, meaning: "Recovery is usable.", observation: "HRV is near usual.", state: "insight", title: "Recovery" },
      key: "recovery",
      summary: "Recovery is steady",
    },
    {
      band: "good",
      data_state: "fresh",
      destination: { id: "activity", kind: "section" },
      insight: { evidence_ids: ["energy"], fallback: true, meaning: "Reserve is available.", observation: "Energy is available.", state: "insight", title: "Energy" },
      key: "energy",
      summary: "Energy is available",
    },
  ],
  evidence: [],
  generation: { fresh_for_snapshot: true, state: "disabled" },
  has_more: false,
  primary: {
    evidence_ids: ["recovery"],
    fallback: true,
    meaning: "Keep the effort controlled.",
    next_step: { id: "moderate", text: "Measured day" },
    observation: "Recovery supports a measured day.",
    state: "insight",
    title: "Today",
  },
  snapshot_version: "v1",
};

describe("TodayInsights", () => {
  it("renders one focal insight and the three server-declared domain routes", () => {
    render(<TodayInsights locale="en" todayInsights={response} />);

    expect(screen.getByRole("link", { name: /Sleep/ })).toHaveAttribute("href", "/sleep?lang=en");
    expect(screen.getByRole("link", { name: /Recovery/ })).toHaveAttribute("href", "/recovery?lang=en");
    expect(screen.getByRole("link", { name: /Energy/ })).toHaveAttribute("href", "/activity?lang=en");
  });

  it("does not render when the optional endpoint is unavailable", () => {
    const { container } = render(<TodayInsights locale="en" />);
    expect(container).toBeEmptyDOMElement();
  });

  it("does not repeat a factual card line when an older response duplicates it", () => {
    render(<TodayInsights locale="en" todayInsights={response} />);

    expect(screen.getAllByText("Energy is available")).toHaveLength(1);
  });

  it("does not repeat a factual line from another domain as an observation", () => {
    const crossDomainDuplicate: TodayInsightsResponse = {
      ...response,
      domains: response.domains?.map((domain) =>
        domain.key === "sleep"
          ? { ...domain, insight: { ...domain.insight, observation: "Energy is available" } }
          : domain,
      ) ?? null,
    };

    render(<TodayInsights locale="en" todayInsights={crossDomainDuplicate} />);

    expect(screen.getAllByText("Energy is available")).toHaveLength(1);
  });
});
