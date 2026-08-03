import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { HealthSectionIcon } from "./HealthSectionIcon";

describe("HealthSectionIcon", () => {
  it("renders a real icon without exposing the API icon key", () => {
    const { container } = render(<HealthSectionIcon name="battery" />);

    expect(container.querySelector("svg")).toBeInTheDocument();
    expect(container).not.toHaveTextContent("battery");
  });

  it("renders a safe fallback for an unknown icon key", () => {
    const { container } = render(<HealthSectionIcon name="future-section" />);

    expect(container.querySelector("svg")).toBeInTheDocument();
    expect(container).not.toHaveTextContent("future-section");
  });
});
