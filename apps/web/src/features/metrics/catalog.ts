import type {
  LatestMetricValue,
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

export const SLEEP_PHASE_METRICS = [
  "sleep_deep",
  "sleep_core",
  "sleep_rem",
  "sleep_unspecified",
  "sleep_awake",
] as const;

const SLEEP_METRIC_SET = new Set<string>([
  "sleep_total",
  ...SLEEP_PHASE_METRICS,
]);

const SUM_METRIC_SET = new Set<string>([
  "step_count",
  "active_energy",
  "basal_energy_burned",
  "walking_running_distance",
  "apple_exercise_time",
  "apple_stand_time",
  "apple_stand_hour",
  "flights_climbed",
  "distance_cycling",
  "distance_swimming",
  "swimming_stroke_count",
  "mindful_minutes",
  "time_in_daylight",
  "dietary_energy",
  "dietary_water",
]);

export function isSleepChartMetric(metric: string): boolean {
  return SLEEP_METRIC_SET.has(metric);
}

export function isBarChartMetric(metric: string, agg: string): boolean {
  if (agg === "SUM") return true;
  return SUM_METRIC_SET.has(metric);
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

export function shiftDateIso(dateStr: string, deltaDays: number): string {
  const parsed = new Date(`${dateStr}T12:00:00`);
  if (Number.isNaN(parsed.getTime())) return dateStr;
  parsed.setDate(parsed.getDate() + deltaDays);
  return parsed.toISOString().slice(0, 10);
}

export function daysBetweenInclusive(from: string, to: string): number {
  const start = new Date(`${from}T12:00:00`);
  const end = new Date(`${to}T12:00:00`);
  if (Number.isNaN(start.getTime()) || Number.isNaN(end.getTime())) return 1;
  return Math.max(1, Math.round((end.getTime() - start.getTime()) / 86400000) + 1);
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
