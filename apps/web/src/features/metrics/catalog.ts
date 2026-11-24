import type {
  LatestMetricValue,
  MetricAgg,
  MetricCatalogItem,
  MetricPoint,
  MetricSourceSeries,
} from "../../api/operations";
import type { Locale } from "../../i18n";
import { tp, type PageMessageKey } from "../../i18n/pages";

export interface MetricCategoryDefinition {
  key: string;
  labelKey: PageMessageKey;
  color: string;
  metrics: readonly string[];
}

export const METRIC_CATEGORIES: readonly MetricCategoryDefinition[] = [
  {
    key: "heart",
    labelKey: "cat_heart",
    color: "#e11d48",
    metrics: [
      "heart_rate",
      "resting_heart_rate",
      "walking_heart_rate_average",
      "heart_rate_variability",
      "blood_oxygen_saturation",
      "respiratory_rate",
      "blood_pressure_systolic",
      "blood_pressure_diastolic",
      "heart_rate_recovery",
      "wrist_temperature",
      "breathing_disturbances",
    ],
  },
  {
    key: "activity",
    labelKey: "cat_activity",
    color: "#059669",
    metrics: [
      "step_count",
      "walking_running_distance",
      "active_energy",
      "basal_energy_burned",
      "apple_exercise_time",
      "apple_stand_time",
      "apple_stand_hour",
      "physical_effort",
      "flights_climbed",
      "stair_ascent_speed",
      "stair_descent_speed",
      "distance_cycling",
      "distance_swimming",
      "swimming_stroke_count",
      "mindful_minutes",
    ],
  },
  {
    key: "mobility",
    labelKey: "cat_fitness",
    color: "#f59e0b",
    metrics: [
      "vo2_max",
      "six_minute_walking_test_distance",
      "walking_speed",
      "walking_step_length",
      "walking_double_support_percentage",
      "walking_asymmetry_percentage",
      "walking_steadiness",
    ],
  },
  {
    key: "sleep",
    labelKey: "cat_sleep",
    color: "#7c3aed",
    metrics: [
      "sleep_total",
      "night_sleep_total",
      "nap_total",
      "sleep_deep",
      "sleep_rem",
      "sleep_core",
      "sleep_unspecified",
      "sleep_awake",
    ],
  },
  {
    key: "body",
    labelKey: "cat_body",
    color: "#10b981",
    metrics: [
      "body_mass",
      "body_mass_index",
      "body_fat_percentage",
      "lean_body_mass",
      "height",
    ],
  },
  {
    key: "env",
    labelKey: "cat_env",
    color: "#06b6d4",
    metrics: [
      "environmental_audio_exposure",
      "headphone_audio_exposure",
      "time_in_daylight",
      "environmental_sound_reduction",
    ],
  },
  {
    key: "nutrition",
    labelKey: "cat_nutrition",
    color: "#f97316",
    metrics: [
      "dietary_energy",
      "dietary_protein",
      "dietary_carbs",
      "dietary_fat",
      "dietary_fat_saturated",
      "dietary_fat_monounsaturated",
      "dietary_fat_polyunsaturated",
      "dietary_water",
      "dietary_fiber",
      "dietary_sugar",
      "dietary_sodium",
      "dietary_caffeine",
      "dietary_calcium",
      "dietary_iron",
      "dietary_cholesterol",
      "dietary_potassium",
      "dietary_magnesium",
      "dietary_phosphorus",
      "dietary_zinc",
      "dietary_copper",
      "dietary_manganese",
      "dietary_selenium",
      "dietary_iodine",
      "dietary_molybdenum",
      "dietary_folate",
      "dietary_biotin",
      "dietary_vitamin_a",
      "dietary_vitamin_c",
      "dietary_vitamin_d",
      "dietary_vitamin_e",
      "dietary_vitamin_k",
      "dietary_vitamin_b6",
      "dietary_vitamin_b12",
      "dietary_niacin",
      "dietary_riboflavin",
      "dietary_thiamin",
      "dietary_pantothenic_acid",
      "alcoholic_beverages",
    ],
  },
];

export const READINESS_MAX_DAYS = 365;

export const SLEEP_PHASE_METRICS = [
  "sleep_deep",
  "sleep_core",
  "sleep_rem",
  "sleep_unspecified",
  "sleep_awake",
] as const;

export type SleepPhaseMetric = (typeof SLEEP_PHASE_METRICS)[number];

export interface SleepPhaseRow {
  date: string;
  sleep_deep: number;
  sleep_core: number;
  sleep_rem: number;
  sleep_unspecified: number;
  sleep_awake: number;
  sleep_total?: number;
}

const SLEEP_METRIC_SET = new Set<string>([
  "sleep_total",
  ...SLEEP_PHASE_METRICS,
]);

// Mirrors backend storage.UsesCurrentMetricSum (dbt SumMetrics U hour_sum live
// quantities) so /api/metrics/data?view=quantity only offers valid aggregations.
const QUANTITY_SUM_METRIC_SET = new Set<string>([
  "active_energy",
  "apple_exercise_time",
  "apple_stand_hour",
  "apple_stand_time",
  "basal_energy_burned",
  "distance_cycling",
  "distance_swimming",
  "flights_climbed",
  "nap_total",
  "night_sleep_total",
  "sleep_awake",
  "sleep_core",
  "sleep_deep",
  "sleep_rem",
  "sleep_total",
  "sleep_unspecified",
  "step_count",
  "swimming_stroke_count",
  "time_in_daylight",
  "walking_running_distance",
]);

export function isSleepChartMetric(metric: string): boolean {
  return SLEEP_METRIC_SET.has(metric);
}

export function usesSumMetricAggregation(metric: string): boolean {
  return QUANTITY_SUM_METRIC_SET.has(metric);
}

export function supportedMetricAggregations(metric: string): readonly MetricAgg[] {
  return usesSumMetricAggregation(metric)
    ? ["", "SUM", "MAX", "MIN"]
    : ["", "AVG", "MAX", "MIN"];
}

export function isBarChartMetric(metric: string, agg: string): boolean {
  if (agg === "SUM") return true;
  return QUANTITY_SUM_METRIC_SET.has(metric);
}

export function buildSleepPhaseRows(
  phasePoints: Partial<Record<SleepPhaseMetric, readonly MetricPoint[] | null | undefined>>,
  totalPoints?: readonly MetricPoint[] | null,
): SleepPhaseRow[] {
  const rowMap = new Map<string, SleepPhaseRow>();
  for (const phase of SLEEP_PHASE_METRICS) {
    for (const p of phasePoints[phase] ?? []) {
      const key = p.date.slice(0, 10);
      let row = rowMap.get(key);
      if (!row) {
        row = {
          date: key,
          sleep_deep: 0,
          sleep_core: 0,
          sleep_rem: 0,
          sleep_unspecified: 0,
          sleep_awake: 0,
        };
        rowMap.set(key, row);
      }
      row[phase] = Number(p.qty.toFixed(2));
    }
  }
  for (const p of totalPoints ?? []) {
    const key = p.date.slice(0, 10);
    const totalQty = Number(p.qty.toFixed(2));
    let row = rowMap.get(key);
    if (!row) {
      row = {
        date: key,
        sleep_deep: 0,
        sleep_core: 0,
        sleep_rem: 0,
        sleep_unspecified: 0,
        sleep_awake: 0,
        sleep_total: totalQty,
      };
      rowMap.set(key, row);
    } else {
      row.sleep_total = totalQty;
    }
  }
  return Array.from(rowMap.values()).sort((a, b) =>
    a.date.localeCompare(b.date),
  );
}

export function formatCompactValue(value: number): string {
  if (!Number.isFinite(value)) return "—";
  if (Math.abs(value) >= 10000) {
    return `${(value / 1000).toFixed(1)}k`;
  }
  if (Math.abs(value) >= 100) {
    return value.toFixed(0);
  }
  if (Math.abs(value) < 10) {
    return value.toFixed(1);
  }
  return value.toFixed(0);
}

export interface GroupedMetricCard {
  name: string;
  displayName: string;
  value?: string;
  unit: string;
}

export interface GroupedMetricCategory {
  key: string;
  label: string;
  color: string;
  items: GroupedMetricCard[];
}

export function groupMetricsCatalog(
  catalog: MetricCatalogItem[],
  latest: LatestMetricValue[],
  query: string,
  locale: Locale,
): GroupedMetricCategory[] {
  const byName = new Map<string, MetricCatalogItem>();
  for (const item of catalog) {
    byName.set(item.name, item);
  }
  const latestMap = new Map<string, LatestMetricValue>();
  for (const entry of latest) {
    latestMap.set(entry.metric, entry);
  }
  const normalizedQuery = query.trim().toLowerCase();
  const seen = new Set<string>();
  const result: GroupedMetricCategory[] = [];

  const matches = (item: MetricCatalogItem): boolean => {
    if (!normalizedQuery) return true;
    return (
      item.display_name.toLowerCase().includes(normalizedQuery) ||
      item.name.toLowerCase().includes(normalizedQuery)
    );
  };

  const toCard = (item: MetricCatalogItem): GroupedMetricCard => {
    const lv = latestMap.get(item.name);
    return {
      name: item.name,
      displayName: item.display_name || item.name,
      value: lv ? formatCompactValue(lv.value) : undefined,
      unit: lv?.unit || item.units || "",
    };
  };

  for (const def of METRIC_CATEGORIES) {
    const items: GroupedMetricCard[] = [];
    for (const metricName of def.metrics) {
      const item = byName.get(metricName);
      if (!item) continue;
      seen.add(metricName);
      if (matches(item)) {
        items.push(toCard(item));
      }
    }
    if (items.length > 0) {
      result.push({
        key: def.key,
        label: tp(locale, def.labelKey),
        color: def.color,
        items,
      });
    }
  }

  const remaining = catalog
    .filter((item) => !seen.has(item.name) && matches(item))
    .sort((a, b) => (a.display_name || a.name).localeCompare(b.display_name || b.name));

  if (remaining.length > 0) {
    result.push({
      key: "other",
      label: tp(locale, "cat_other"),
      color: "#64748b",
      items: remaining.map(toCard),
    });
  }

  return result;
}

export function todayIso(now = new Date()): string {
  const year = now.getFullYear();
  const month = String(now.getMonth() + 1).padStart(2, "0");
  const day = String(now.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

function parseIsoDateUtc(dateStr: string): Date | null {
  const match = /^(\d{4})-(\d{2})-(\d{2})/.exec(dateStr);
  if (!match) return null;
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  const dt = new Date(Date.UTC(year, month - 1, day));
  if (
    dt.getUTCFullYear() !== year ||
    dt.getUTCMonth() !== month - 1 ||
    dt.getUTCDate() !== day
  ) {
    return null;
  }
  return dt;
}

export function shiftDateIso(dateStr: string, deltaDays: number): string {
  const parsed = parseIsoDateUtc(dateStr);
  if (!parsed) return dateStr;
  parsed.setUTCDate(parsed.getUTCDate() + deltaDays);
  return parsed.toISOString().slice(0, 10);
}

export function daysBetweenInclusive(from: string, to: string): number {
  const start = parseIsoDateUtc(from);
  const end = parseIsoDateUtc(to);
  if (!start || !end) return 1;
  return Math.max(1, Math.round((end.getTime() - start.getTime()) / 86400000) + 1);
}

export function shiftBucketDate(dateStr: string, deltaDays: number): string {
  if (/^\d{4}-\d{2}-\d{2}$/.test(dateStr)) {
    return shiftDateIso(dateStr, deltaDays);
  }
  const parsed = new Date(dateStr);
  if (!Number.isNaN(parsed.getTime())) {
    const shifted = new Date(parsed.getTime() + deltaDays * 86400000);
    return shifted.toISOString();
  }
  return dateStr;
}

export function normalizeBucketKey(dateStr: string, bucket: string): string {
  if (bucket === "day" || /^\d{4}-\d{2}-\d{2}$/.test(dateStr)) {
    return dateStr.slice(0, 10);
  }
  const parsed = new Date(dateStr);
  return Number.isNaN(parsed.getTime()) ? dateStr : parsed.toISOString();
}

export interface AlignedComparisonRow {
  rawDate: string;
  value?: number;
  compare?: number;
}

export function alignComparisonSeries(
  points: MetricPoint[],
  comparePoints: MetricPoint[],
  compareShiftDays: number,
  bucket: string,
): AlignedComparisonRow[] {
  const rows = new Map<string, AlignedComparisonRow>();
  for (const p of points) {
    const key = normalizeBucketKey(p.date, bucket);
    rows.set(key, { rawDate: key, value: p.qty });
  }
  for (const p of comparePoints) {
    const shifted = shiftBucketDate(p.date, compareShiftDays);
    const key = normalizeBucketKey(shifted, bucket);
    const existing = rows.get(key);
    if (existing) {
      existing.compare = p.qty;
    } else {
      rows.set(key, { rawDate: key, compare: p.qty });
    }
  }
  return Array.from(rows.values()).sort((a, b) =>
    a.rawDate.localeCompare(b.rawDate),
  );
}

export interface SeriesStats {
  count: number;
  avg: number;
  min: number;
  max: number;
  sum: number;
}

export function computeSeriesStats(points: MetricPoint[]): SeriesStats | null {
  const valid = points.filter((p) => Number.isFinite(p.qty));
  if (valid.length === 0) return null;
  let sum = 0;
  let min = Infinity;
  let max = -Infinity;
  for (const p of valid) {
    sum += p.qty;
    const low = p.min !== undefined && Number.isFinite(p.min) ? p.min : p.qty;
    const high = p.max !== undefined && Number.isFinite(p.max) ? p.max : p.qty;
    if (low < min) min = low;
    if (high > max) max = high;
  }
  return {
    count: valid.length,
    avg: sum / valid.length,
    min,
    max,
    sum,
  };
}

function escapeCsvCell(value: string | number | undefined): string {
  const str = value === undefined ? "" : String(value);
  if (/[",\n]/.test(str)) {
    return `"${str.replaceAll('"', '""')}"`;
  }
  return str;
}

export function buildMetricCsv(
  metric: string,
  points: MetricPoint[],
  bySource?: MetricSourceSeries[] | null,
): string {
  if (bySource && bySource.length > 0) {
    const lines = ["metric,source,date,value,min,max"];
    for (const group of bySource) {
      for (const p of group.points ?? []) {
        lines.push(
          [
            escapeCsvCell(metric),
            escapeCsvCell(group.source),
            escapeCsvCell(p.date),
            escapeCsvCell(p.qty),
            escapeCsvCell(p.min),
            escapeCsvCell(p.max),
          ].join(","),
        );
      }
    }
    return lines.join("\n");
  }
  const lines = ["metric,date,value,min,max"];
  for (const p of points) {
    lines.push(
      [
        escapeCsvCell(metric),
        escapeCsvCell(p.date),
        escapeCsvCell(p.qty),
        escapeCsvCell(p.min),
        escapeCsvCell(p.max),
      ].join(","),
    );
  }
  return lines.join("\n");
}
