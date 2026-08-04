import {
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";

import { chartTheme } from "./chartTheme";

export interface TrendPoint {
  label: string;
  value: number;
}

export interface TrendChartProps {
  ariaLabel: string;
  data: TrendPoint[];
  tone: "readiness" | "energy";
}

export function TrendChart({ ariaLabel, data, tone }: TrendChartProps) {
  if (data.length < 2) {
    return null;
  }

  return (
    <div className="trend-chart" role="img" aria-label={ariaLabel}>
      <ResponsiveContainer width="100%" height="100%">
        <LineChart data={data} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
          <CartesianGrid stroke={chartTheme.grid} vertical={false} />
          <XAxis
            dataKey="label"
            axisLine={false}
            tickLine={false}
            minTickGap={28}
            tick={{ fill: chartTheme.text, fontSize: 11 }}
          />
          <YAxis
            domain={tone === "readiness" ? [0, 100] : ["auto", "auto"]}
            axisLine={false}
            tickLine={false}
            width={34}
            tick={{ fill: chartTheme.text, fontSize: 11 }}
          />
          <Tooltip
            cursor={{ stroke: chartTheme.grid }}
            contentStyle={{
              background: chartTheme.tooltipBackground,
              border: `1px solid ${chartTheme.tooltipBorder}`,
              borderRadius: 12,
              boxShadow: chartTheme.tooltipShadow,
            }}
          />
          <Line
            type="monotone"
            dataKey="value"
            stroke={chartTheme[tone]}
            strokeWidth={3}
            dot={false}
            activeDot={{ r: 5 }}
            isAnimationActive={false}
          />
        </LineChart>
      </ResponsiveContainer>
    </div>
  );
}
