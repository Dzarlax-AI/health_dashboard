import { render, screen } from "@testing-library/react";

import { ClientApiError } from "./api/client";
import { App } from "./App";
import { fixtureResources } from "./features/dashboard/fixtures";
import { loadDashboardResources } from "./features/dashboard/loader";

vi.mock("./features/dashboard/loader", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("./features/dashboard/loader")>();
  return {
    ...actual,
    loadDashboardResources: vi.fn(),
  };
});

const mockedLoadDashboardResources = vi.mocked(loadDashboardResources);

function renderFixture(fixture: string, locale = "en") {
  vi.stubEnv("VITE_ENABLE_FIXTURES", "true");
  window.history.replaceState({}, "", `/?lang=${locale}&fixture=${fixture}`);
  return render(<App />);
}

describe("foundation fixtures", () => {
  afterEach(() => {
    mockedLoadDashboardResources.mockReset();
    window.history.replaceState({}, "", "/");
    document.documentElement.lang = "en";
    vi.unstubAllEnvs();
  });

  it.each(["normal", "partial", "stale"])(
    "renders a useful %s readiness state",
    (fixture) => {
      renderFixture(fixture, "ru");
      expect(document.querySelector("[data-readiness-ring]")).toBeInTheDocument();
      const state = fixture === "normal" ? "ready" : fixture;
      expect(document.querySelector(`[data-resource-state="${state}"]`)).toBeInTheDocument();
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

  it("ignores fixture query input in an ordinary production build", async () => {
    vi.stubEnv("VITE_ENABLE_FIXTURES", "false");
    window.history.replaceState({}, "", "/?lang=en&fixture=normal");
    mockedLoadDashboardResources.mockResolvedValue(fixtureResources("en", "normal"));
    const view = render(<App />);

    expect(screen.queryByRole("navigation", { name: "Component states" })).not.toBeInTheDocument();
    expect(screen.getByText("Refreshing today")).toBeInTheDocument();
    expect(await screen.findByText("You can move with more confidence today.")).toBeInTheDocument();
    view.unmount();
  });

  it("encodes the complete post-login destination", async () => {
    vi.stubEnv("VITE_ENABLE_FIXTURES", "false");
    window.history.replaceState({}, "", "/?lang=en");
    mockedLoadDashboardResources.mockRejectedValue(
      new ClientApiError(401, "authentication required"),
    );

    render(<App />);

    expect(await screen.findByRole("link", { name: "Sign in" })).toHaveAttribute(
      "href",
      "/login?next=%2F%3Flang%3Den",
    );
  });

  it("falls back to the English locale for unsupported input", () => {
    renderFixture("normal", "schema=tenant_b");
    expect(screen.getByText("Readiness")).toBeInTheDocument();
    expect(document.querySelector("[data-locale]")).toHaveAttribute("data-locale", "en");
    expect(document.documentElement).toHaveAttribute("lang", "en");
  });

  it.each([
    ["/activity", "Activity"],
    ["/cardio", "Heart & breathing"],
    ["/recovery", "Recovery"],
  ])("routes %s to its React detail page", (path, title) => {
    vi.stubEnv("VITE_ENABLE_FIXTURES", "true");
    window.history.replaceState({}, "", `${path}?lang=en&fixture=normal`);
    render(<App />);

    expect(screen.getByRole("heading", { level: 1, name: title })).toBeInTheDocument();
  });
});
