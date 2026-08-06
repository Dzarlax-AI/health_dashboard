import {
  Bar,
  BarChart,
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
  color?: string;
  kind?: "line" | "bar";
  unit?: string;
}

export function TrendChart({
  ariaLabel,
  data,
  tone,
  color,
  kind = "line",
  unit = "",
}: TrendChartProps) {
  if (data.length < 2) {
    return null;
  }

  const stroke = color ?? chartTheme[tone];
  const common = {
    data,
    margin: { top: 8, right: 8, bottom: 0, left: 0 },
  };
  const axes = (
    <>
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
        width={42}
        tick={{ fill: chartTheme.text, fontSize: 11 }}
      />
      <Tooltip
        cursor={{ stroke: chartTheme.grid }}
        formatter={(value) => [`${String(value)}${unit ? ` ${unit}` : ""}`, ariaLabel]}
        contentStyle={{
          background: chartTheme.tooltipBackground,
          border: `1px solid ${chartTheme.tooltipBorder}`,
          borderRadius: 12,
          boxShadow: chartTheme.tooltipShadow,
        }}
      />
    </>
  );

  return (
    <div className="trend-chart" role="img" aria-label={ariaLabel}>
      <ResponsiveContainer width="100%" height="100%">
        {kind === "bar" ? (
          <BarChart {...common}>
            {axes}
            <Bar
              dataKey="value"
              fill={stroke}
              radius={[5, 5, 1, 1]}
              isAnimationActive={false}
            />
          </BarChart>
        ) : (
          <LineChart {...common}>
            {axes}
            <Line
              type="monotone"
              dataKey="value"
              stroke={stroke}
              strokeWidth={3}
              dot={false}
              activeDot={{ r: 5 }}
              isAnimationActive={false}
            />
          </LineChart>
        )}
      </ResponsiveContainer>
    </div>
  );
}
