import { render, screen } from "@testing-library/react";
import type { ReactNode } from "react";

vi.mock("recharts", async () => {
  const actual = await vi.importActual<typeof import("recharts")>("recharts");
  return {
    ...actual,
    ResponsiveContainer: ({ children }: { children: ReactNode }) => (
      <div style={{ width: 640, height: 320 }}>
        <actual.ResponsiveContainer width={640} height={320}>
          {children}
        </actual.ResponsiveContainer>
      </div>
    ),
  };
});

import { buildSleepPhaseRows } from "./catalog";
import { MetricChart } from "./MetricChart";

describe("MetricChart sleep_total rendering", () => {
  it("plots stored sleep_total as a dedicated line/dot series above mismatched stages and preserves total-only nights without relabeling as unspecified", () => {
    const sleepRows = buildSleepPhaseRows(
      {
        sleep_deep: [{ date: "2026-11-22", qty: 1.5 }],
        sleep_core: [{ date: "2026-11-22", qty: 4.0 }],
        sleep_rem: [{ date: "2026-11-22", qty: 1.5 }],
      },
      [
        // Night with only stored sleep_total (no stage metrics sent)
        { date: "2026-11-21", qty: 7.4 },
        // Night where stored sleep_total (8.2h) differs from summed stages (7.0h)
        { date: "2026-11-22", qty: 8.2 },
      ],
    );

    const { container } = render(
      <MetricChart
        locale="en"
        label="Sleep total"
        unit="hr"
        bucket="day"
        mode="sleep"
        points={[
          { date: "2026-11-21", qty: 7.4 },
          { date: "2026-11-22", qty: 8.2 },
        ]}
        sleepRows={sleepRows}
      />,
    );

    expect(screen.getByRole("img", { name: "Sleep total" })).toBeInTheDocument();
    // Legend shows both the stored Sleep total series and stage breakdown items
    expect(screen.getByText("Sleep total")).toBeInTheDocument();
    expect(screen.getByText("Deep")).toBeInTheDocument();
    expect(screen.getByText("Core")).toBeInTheDocument();
    expect(screen.getByText("REM")).toBeInTheDocument();

    // Dedicated sleep_total Line series renders dots for BOTH nights (7.4h total-only & 8.2h mismatched)
    const dots = Array.from(container.querySelectorAll(".recharts-line-dot"));
    expect(dots).toHaveLength(2);
    const totalOnlyCy = Number(dots[0].getAttribute("cy"));
    const mismatchedCy = Number(dots[1].getAttribute("cy"));
    expect(Number.isFinite(totalOnlyCy)).toBe(true);
    expect(Number.isFinite(mismatchedCy)).toBe(true);
    // 8.2h sits higher (smaller SVG y) than 7.4h
    expect(mismatchedCy).toBeLessThan(totalOnlyCy);

    // No sleep_unspecified bar should have been fabricated for the total-only night
    const unspecifiedBars = Array.from(
      container.querySelectorAll('.recharts-bar-rectangle path[fill="#94a3b8"]'),
    ).filter((el) => Number(el.getAttribute("height") ?? "0") > 0);
    expect(unspecifiedBars).toHaveLength(0);

    // For the mismatched night, the stacked stage bars (sum 7.0h) sit strictly below the 8.2h sleep_total dot
    const nonZeroStageBarTops = Array.from(
      container.querySelectorAll(".recharts-bar-rectangle path"),
    )
      .filter((el) => Number(el.getAttribute("height") ?? "0") > 0)
      .map((el) => Number(el.getAttribute("y")));
    expect(nonZeroStageBarTops.length).toBeGreaterThanOrEqual(3);
    const highestStageBarY = Math.min(...nonZeroStageBarTops);
    expect(mismatchedCy).toBeLessThan(highestStageBarY);
  });

  it("omits the sleep_total overlay line when rendering a phase-only sleep chart", () => {
    const sleepRows = buildSleepPhaseRows({
      sleep_deep: [{ date: "2026-11-22", qty: 1.5 }],
      sleep_core: [{ date: "2026-11-22", qty: 4.0 }],
    });

    const { container } = render(
      <MetricChart
        locale="en"
        label="Deep sleep"
        unit="hr"
        bucket="day"
        mode="sleep"
        points={[{ date: "2026-11-22", qty: 1.5 }]}
        sleepRows={sleepRows}
      />,
    );

    expect(container.querySelectorAll(".recharts-line")).toHaveLength(0);
    expect(container.querySelectorAll(".recharts-bar").length).toBeGreaterThan(0);
  });
});
