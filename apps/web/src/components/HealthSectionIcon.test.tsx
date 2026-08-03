import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { HealthSectionIcon } from "./HealthSectionIcon";

describe("HealthSectionIcon", () => {
  it.each([
    ["activity", "lucide-activity"],
    ["cardio", "lucide-heart-pulse"],
    ["recovery", "lucide-battery-medium"],
    ["sleep", "lucide-moon"],
  ])("renders the intended icon for %s", (sectionKey, iconClass) => {
    const { container } = render(<HealthSectionIcon sectionKey={sectionKey} />);

    expect(container.querySelector("svg")).toHaveClass(iconClass);
    expect(container).not.toHaveTextContent(sectionKey);
  });

  it.each(["☾", "♡", "↗", "future-section"])(
    "renders a safe fallback for unknown key %s",
    (sectionKey) => {
      const { container } = render(<HealthSectionIcon sectionKey={sectionKey} />);

      expect(container.querySelector("svg")).toHaveClass(
        "lucide-circle-question-mark",
      );
      expect(container).not.toHaveTextContent(sectionKey);
    },
  );
});
