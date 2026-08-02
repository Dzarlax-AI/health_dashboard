import { render, screen } from "@testing-library/react";

import { App } from "./App";

function renderFixture(fixture: string, locale = "en") {
  window.history.replaceState({}, "", `/?lang=${locale}&fixture=${fixture}`);
  return render(<App />);
}

describe("foundation fixtures", () => {
  afterEach(() => {
    window.history.replaceState({}, "", "/");
    document.documentElement.lang = "en";
  });

  it.each(["normal", "partial", "stale"])(
    "renders a useful %s readiness state",
    (fixture) => {
      renderFixture(fixture, "ru");
      expect(document.querySelector("[data-readiness-ring]")).toBeInTheDocument();
      expect(document.querySelector(`[data-resource-state="${fixture}"]`)).toBeInTheDocument();
    },
  );

  it.each(["loading", "unavailable", "error"])(
    "does not fabricate a gauge for %s",
    (fixture) => {
      renderFixture(fixture);
      expect(document.querySelector("[data-readiness-ring]")).not.toBeInTheDocument();
      expect(document.querySelector(`[data-resource-state="${fixture}"]`)).toBeInTheDocument();
      expect(screen.getByRole("heading", { level: 1 })).toBeInTheDocument();
    },
  );

  it("applies Serbian content and document language", () => {
    renderFixture("normal", "sr");

    expect(screen.getByText("Spremnost")).toBeInTheDocument();
    expect(document.querySelector("[data-locale]")).toHaveAttribute("data-locale", "sr");
    expect(document.documentElement).toHaveAttribute("lang", "sr");
    expect(screen.getByRole("navigation", { name: "Jezik" })).toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "Stanja komponenti" })).toBeInTheDocument();
    expect(
      screen.getByRole("region", { name: "Dodatne zdravstvene ocene" }),
    ).toBeInTheDocument();
  });

  it("falls back to the English locale for unsupported input", () => {
    renderFixture("normal", "schema=tenant_b");
    expect(screen.getByText("Readiness")).toBeInTheDocument();
    expect(document.querySelector("[data-locale]")).toHaveAttribute("data-locale", "en");
    expect(document.documentElement).toHaveAttribute("lang", "en");
  });
});
