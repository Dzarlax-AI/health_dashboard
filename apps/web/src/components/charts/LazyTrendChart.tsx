import {
  Component,
  lazy,
  Suspense,
  type ErrorInfo,
  type ReactNode,
} from "react";

import type { TrendChartProps } from "./TrendChart";

const TrendChart = lazy(async () => {
  const module = await import("./TrendChart");
  return { default: module.TrendChart };
});

interface ChartErrorBoundaryProps {
  children: ReactNode;
  fallback: ReactNode;
}

interface ChartErrorBoundaryState {
  failed: boolean;
}

export class ChartErrorBoundary extends Component<
  ChartErrorBoundaryProps,
  ChartErrorBoundaryState
> {
  state: ChartErrorBoundaryState = { failed: false };

  static getDerivedStateFromError(): ChartErrorBoundaryState {
    return { failed: true };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("Trend chart failed to render", error, info.componentStack);
  }

  render() {
    return this.state.failed ? this.props.fallback : this.props.children;
  }
}

export function LazyTrendChart(props: TrendChartProps) {
  if (props.data.length < 2) {
    return null;
  }
  return (
    <ChartErrorBoundary
      fallback={
        <div
          className="trend-chart trend-chart--error"
          role="img"
          aria-label={props.ariaLabel}
        />
      }
    >
      <Suspense fallback={<div className="trend-chart trend-chart--loading" />}>
        <TrendChart {...props} />
      </Suspense>
    </ChartErrorBoundary>
  );
}
