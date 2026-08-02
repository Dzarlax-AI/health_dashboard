import { lazy, Suspense } from "react";

import type { TrendPoint } from "./TrendChart";

const TrendChart = lazy(async () => {
  const module = await import("./TrendChart");
  return { default: module.TrendChart };
});

interface LazyTrendChartProps {
  ariaLabel: string;
  data: TrendPoint[];
  tone: "readiness" | "energy";
}

export function LazyTrendChart(props: LazyTrendChartProps) {
  if (props.data.length < 2) {
    return null;
  }
  return (
    <Suspense fallback={<div className="trend-chart trend-chart--loading" />}>
      <TrendChart {...props} />
    </Suspense>
  );
}
