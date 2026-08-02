import { render, screen } from "@testing-library/react";

import { ReadinessRing } from "./ReadinessRing";

describe("ReadinessRing", () => {
  it("uses one fixed SVG coordinate system and accessible text", () => {
    const { container } = render(
      <ReadinessRing value={65} label="Readiness" status="Fair" />,
    );

    const figure = screen.getByLabelText("Readiness, 65%, Fair");
    const svg = container.querySelector("[data-gauge-svg]");
    const progress = container.querySelector(".score-gauge__progress");

    expect(figure).toHaveAttribute("data-score", "65");
    expect(svg).toHaveAttribute("viewBox", "0 0 120 120");
    expect(svg).toHaveAttribute("preserveAspectRatio", "xMidYMid meet");
    expect(progress).toHaveAttribute("cx", "60");
    expect(progress).toHaveAttribute("cy", "60");
    expect(progress).toHaveAttribute("r", "50");
    expect(progress).toHaveAttribute("stroke-dasharray", "65 100");
  });

  it.each([
    [-12, "0"],
    [48.6, "49"],
    [125, "100"],
  ])("clamps score %s to %s", (value, expected) => {
    render(<ReadinessRing value={value} label="Readiness" />);
    expect(screen.getByLabelText(new RegExp(`Readiness, ${expected}%`))).toHaveAttribute(
      "data-score",
      expected,
    );
  });
});
