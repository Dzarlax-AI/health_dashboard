import type { AIBriefingResponse } from "../../api/client";
import type { SleepMetric, SleepResources } from "./loader";

export interface SleepDay {
  date: string;
  total: number;
  deep: number;
  rem: number;
  core: number;
  unspecified: number;
  awake: number;
  wake?: string;
}

const metricToKey: Record<SleepMetric, keyof Omit<SleepDay, "date" | "wake">> = {
  sleep_total: "total",
  sleep_deep: "deep",
  sleep_rem: "rem",
  sleep_core: "core",
  sleep_unspecified: "unspecified",
  sleep_awake: "awake",
};

export function buildSleepDays(resources: SleepResources): SleepDay[] {
  const days = new Map<string, SleepDay>();
  for (const [metric, response] of Object.entries(resources.metrics) as [
    SleepMetric,
    NonNullable<SleepResources["metrics"][SleepMetric]>,
  ][]) {
    for (const point of response.points ?? []) {
      const date = point.date.slice(0, 10);
      const day = days.get(date) ?? {
        date,
        total: 0,
        deep: 0,
        rem: 0,
        core: 0,
        unspecified: 0,
        awake: 0,
      };
      day[metricToKey[metric]] = point.qty;
      days.set(date, day);
    }
  }
  for (const point of resources.wake?.values ?? []) {
    const day = days.get(point.metric_date);
    if (day && point.value_timestamp) {
      day.wake = point.value_timestamp;
    }
  }
  for (const day of days.values()) {
    if (day.total <= 0) {
      day.total = day.deep + day.rem + day.core + day.unspecified;
    }
  }
  return [...days.values()]
    .filter((day) => day.total > 0 || day.deep + day.rem + day.core + day.unspecified > 0)
    .sort((a, b) => b.date.localeCompare(a.date));
}

export function sleepInsight(ai?: AIBriefingResponse): string {
  return ai?.blocks?.SLEEP || ai?.sleep || ai?.blocks?.SYNTHESIS || ai?.summary || "";
}
