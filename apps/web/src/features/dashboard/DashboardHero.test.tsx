import { render, screen } from "@testing-library/react";

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
});
