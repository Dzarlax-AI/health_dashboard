import {
  Bar,
  BarChart,
  CartesianGrid,
  Legend,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";

import type { MetricPoint, MetricSourceSeries } from "../../api/operations";
import { chartTheme } from "../../components/charts/chartTheme";
import { translate, type Locale } from "../../i18n";
import { tp } from "../../i18n/pages";
import {
  alignComparisonSeries,
  SLEEP_PHASE_METRICS,
} from "./catalog";

const SOURCE_COLORS = [
  "#2563eb",
  "#16a34a",
  "#d97706",
  "#dc2626",
  "#7c3aed",
  "#0891b2",
  "#db2777",
];

const SLEEP_PHASE_COLORS: Record<(typeof SLEEP_PHASE_METRICS)[number], string> = {
  sleep_deep: "#4f46e5",
  sleep_core: "#3b82f6",
  sleep_rem: "#a855f7",
  sleep_unspecified: "#94a3b8",
  sleep_awake: "#f97316",
};

const SLEEP_PHASE_LABEL_KEYS = {
  sleep_deep: "sleepPhase_deep",
  sleep_core: "sleepPhase_core",
  sleep_rem: "sleepPhase_rem",
  sleep_unspecified: "sleepPhase_unspecified",
  sleep_awake: "sleepPhase_awake",
} as const;

function formatTick(dateStr: string, locale: Locale, bucket: string): string {
  if (bucket === "hour" || bucket === "minute") {
    const d = new Date(dateStr);
    if (!Number.isNaN(d.getTime())) {
      return new Intl.DateTimeFormat(locale, {
        month: "short",
        day: "numeric",
        hour: "2-digit",
        minute: "2-digit",
      }).format(d);
    }
  }
  const day = dateStr.slice(0, 10);
  const d = new Date(`${day}T12:00:00`);
  if (!Number.isNaN(d.getTime())) {
    return new Intl.DateTimeFormat(locale, {
      day: "numeric",
      month: "short",
    }).format(d);
  }
  return dateStr;
}

export interface SleepPhaseRow {
  date: string;
  sleep_deep: number;
  sleep_core: number;
  sleep_rem: number;
  sleep_unspecified: number;
  sleep_awake: number;
}

export interface MetricChartProps {
  locale: Locale;
  label: string;
  unit: string;
  bucket: string;
  mode: "line" | "bar" | "readiness" | "by_source" | "sleep";
  points: MetricPoint[];
  comparePoints?: MetricPoint[];
  compareShiftDays?: number;
  sources?: MetricSourceSeries[];
  sleepRows?: SleepPhaseRow[];
}

export function MetricChart({
  locale,
  label,
  unit,
  bucket,
  mode,
  points,
  comparePoints = [],
  compareShiftDays = 0,
  sources = [],
  sleepRows = [],
}: MetricChartProps) {
  const tooltipStyle = {
    background: chartTheme.tooltipBackground,
    border: `1px solid ${chartTheme.tooltipBorder}`,
    borderRadius: 12,
    boxShadow: chartTheme.tooltipShadow,
  };

  if (mode === "sleep") {
    if (sleepRows.length === 0) {
      return <p className="subpage__muted">{tp(locale, "metricNoPoints")}</p>;
    }
    const data = sleepRows.map((row) => ({
      ...row,
      tick: formatTick(row.date, locale, "day"),
    }));
    return (
      <div className="metric-chart-box" role="img" aria-label={label}>
        <ResponsiveContainer width="100%" height="100%">
          <BarChart data={data} margin={{ top: 10, right: 12, bottom: 0, left: 0 }}>
            <CartesianGrid stroke={chartTheme.grid} vertical={false} />
            <XAxis
              dataKey="tick"
              axisLine={false}
              tickLine={false}
              minTickGap={24}
              tick={{ fill: chartTheme.text, fontSize: 11 }}
            />
            <YAxis
              axisLine={false}
              tickLine={false}
              width={44}
              tick={{ fill: chartTheme.text, fontSize: 11 }}
            />
            <Tooltip contentStyle={tooltipStyle} />
            <Legend />
            {SLEEP_PHASE_METRICS.map((phase) => (
              <Bar
                key={phase}
                dataKey={phase}
                name={translate(locale, SLEEP_PHASE_LABEL_KEYS[phase])}
                stackId="sleep"
                fill={SLEEP_PHASE_COLORS[phase]}
                isAnimationActive={false}
              />
            ))}
          </BarChart>
        </ResponsiveContainer>
      </div>
    );
  }

  if (mode === "by_source") {
    const activeSources = sources.filter((s) => (s.points?.length ?? 0) > 0);
    if (activeSources.length === 0) {
      return <p className="subpage__muted">{tp(locale, "metricNoPoints")}</p>;
    }
    const byDate = new Map<string, Record<string, string | number>>();
    for (const s of activeSources) {
      for (const p of s.points ?? []) {
        let row = byDate.get(p.date);
        if (!row) {
          row = {
            rawDate: p.date,
            tick: formatTick(p.date, locale, bucket),
          };
          byDate.set(p.date, row);
        }
        row[s.source] = p.qty;
      }
    }
    const data = Array.from(byDate.values()).sort((a, b) =>
      String(a.rawDate).localeCompare(String(b.rawDate)),
    );
    return (
      <div className="metric-chart-box" role="img" aria-label={label}>
        <ResponsiveContainer width="100%" height="100%">
          <LineChart data={data} margin={{ top: 10, right: 12, bottom: 0, left: 0 }}>
            <CartesianGrid stroke={chartTheme.grid} vertical={false} />
            <XAxis
              dataKey="tick"
              axisLine={false}
              tickLine={false}
              minTickGap={24}
              tick={{ fill: chartTheme.text, fontSize: 11 }}
            />
            <YAxis
              axisLine={false}
              tickLine={false}
              width={48}
              tick={{ fill: chartTheme.text, fontSize: 11 }}
            />
            <Tooltip contentStyle={tooltipStyle} />
            <Legend />
            {activeSources.map((s, index) => (
              <Line
                key={s.source}
                type="monotone"
                dataKey={s.source}
                stroke={SOURCE_COLORS[index % SOURCE_COLORS.length]}
                strokeWidth={2.2}
                dot={false}
                connectNulls
                isAnimationActive={false}
              />
            ))}
          </LineChart>
        </ResponsiveContainer>
      </div>
    );
  }

  if (points.length === 0 && comparePoints.length === 0) {
    return <p className="subpage__muted">{tp(locale, "metricNoPoints")}</p>;
  }

  const data =
    comparePoints.length > 0 && compareShiftDays > 0
      ? alignComparisonSeries(points, comparePoints, compareShiftDays, bucket).map(
          (row) => ({
            ...row,
            tick: formatTick(row.rawDate, locale, bucket),
          }),
        )
      : points.map((p) => ({
          rawDate: p.date,
          tick: formatTick(p.date, locale, bucket),
          value: p.qty,
          compare: undefined,
        }));

  const primaryColor =
    mode === "readiness" ? chartTheme.readiness : chartTheme.energy;

  return (
    <div className="metric-chart-box" role="img" aria-label={label}>
      <ResponsiveContainer width="100%" height="100%">
        {mode === "bar" && comparePoints.length === 0 ? (
          <BarChart data={data} margin={{ top: 10, right: 12, bottom: 0, left: 0 }}>
            <CartesianGrid stroke={chartTheme.grid} vertical={false} />
            <XAxis
              dataKey="tick"
              axisLine={false}
              tickLine={false}
              minTickGap={24}
              tick={{ fill: chartTheme.text, fontSize: 11 }}
            />
            <YAxis
              axisLine={false}
              tickLine={false}
              width={48}
              tick={{ fill: chartTheme.text, fontSize: 11 }}
            />
            <Tooltip
              contentStyle={tooltipStyle}
              formatter={(value) => [
                `${String(value)}${unit ? ` ${unit}` : ""}`,
                label,
              ]}
            />
            <Bar
              dataKey="value"
              fill={primaryColor}
              radius={[4, 4, 0, 0]}
              isAnimationActive={false}
            />
          </BarChart>
        ) : (
          <LineChart data={data} margin={{ top: 10, right: 12, bottom: 0, left: 0 }}>
            <CartesianGrid stroke={chartTheme.grid} vertical={false} />
            <XAxis
              dataKey="tick"
              axisLine={false}
              tickLine={false}
              minTickGap={24}
              tick={{ fill: chartTheme.text, fontSize: 11 }}
            />
            <YAxis
              domain={mode === "readiness" ? [0, 100] : ["auto", "auto"]}
              axisLine={false}
              tickLine={false}
              width={48}
              tick={{ fill: chartTheme.text, fontSize: 11 }}
            />
            <Tooltip
              contentStyle={tooltipStyle}
              formatter={(value, name) => [
                `${String(value)}${unit ? ` ${unit}` : ""}`,
                name === "compare" ? tp(locale, "previousPeriod") : label,
              ]}
            />
            {comparePoints.length > 0 ? <Legend /> : null}
            <Line
              type="monotone"
              dataKey="value"
              name={label}
              stroke={primaryColor}
              strokeWidth={2.5}
              dot={points.length <= 2}
              isAnimationActive={false}
            />
            {comparePoints.length > 0 ? (
              <Line
                type="monotone"
                dataKey="compare"
                name={tp(locale, "previousPeriod")}
                stroke="#94a3b8"
                strokeDasharray="5 5"
                strokeWidth={2}
                dot={false}
                isAnimationActive={false}
              />
            ) : null}
          </LineChart>
        )}
      </ResponsiveContainer>
    </div>
  );
}
