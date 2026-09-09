import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { sleepFixtureResources } from "./fixtures";
import { SleepReady } from "./SleepPage";

describe("SleepPage history periods", () => {
  it("shows the complete fixture history when All is selected", () => {
    const { container } = render(
      <SleepReady resources={sleepFixtureResources("en")} locale="en" />,
    );

    expect(container.querySelectorAll(".sleep-history__night")).toHaveLength(30);
    expect(container.querySelector(".sleep-history__night.is-selected")).toHaveAttribute("aria-pressed", "true");

    fireEvent.click(screen.getByRole("button", { name: "All" }));

    expect(container.querySelectorAll(".sleep-history__night")).toHaveLength(90);
  });
});
