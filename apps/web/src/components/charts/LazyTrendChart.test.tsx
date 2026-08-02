import { render, screen } from "@testing-library/react";

import { ChartErrorBoundary } from "./LazyTrendChart";

function BrokenChart(): never {
  throw new Error("chunk failed");
}

describe("ChartErrorBoundary", () => {
  it("contains a chart failure inside the chart panel", () => {
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);

    render(
      <ChartErrorBoundary fallback={<div role="status">Chart unavailable</div>}>
        <BrokenChart />
      </ChartErrorBoundary>,
    );

    expect(screen.getByRole("status")).toHaveTextContent("Chart unavailable");
    consoleError.mockRestore();
  });
});
