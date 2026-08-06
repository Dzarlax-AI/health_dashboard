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

export type SleepPhaseCoverage = "complete" | "partial" | "coarse";

export interface SleepComposition {
  deep: number;
  rem: number;
  core: number;
  unspecified: number;
  awake: number;
  asleep: number;
  inBed: number;
  coverage: SleepPhaseCoverage;
}

const COVERAGE_TOLERANCE_HOURS = 0.08;

export function sleepComposition(day: SleepDay): SleepComposition {
  const total = Math.max(day.total, 0);
  const awake = Math.max(day.awake, 0);
  const deep = Math.max(day.deep, 0);
  const rem = Math.max(day.rem, 0);
  const core = Math.max(day.core, 0);
  const unspecified = Math.max(day.unspecified, 0);

  // Some sources put their entire coarse "asleep" value into sleep_core
  // without emitting any actual stages. Treating that as 100% Core is a
  // stronger claim than the source supports, so present it as unclassified.
  if (deep <= COVERAGE_TOLERANCE_HOURS && rem <= COVERAGE_TOLERANCE_HOURS && core > 0 && unspecified <= COVERAGE_TOLERANCE_HOURS) {
    const asleep = total > 0 ? total : core;
    return {
      deep: 0,
      rem: 0,
      core: 0,
      unspecified: asleep,
      awake,
      asleep,
      inBed: asleep + awake,
      coverage: "coarse",
    };
  }

  const known = deep + rem + core + unspecified;
  const target = total > 0 ? total : known;
  const gap = Math.max(target - known, 0);
  const resolvedUnspecified = unspecified + gap;
  const asleep = deep + rem + core + resolvedUnspecified;
  const coverage =
    gap > COVERAGE_TOLERANCE_HOURS || Math.abs(asleep - target) > COVERAGE_TOLERANCE_HOURS
      ? "partial"
      : "complete";

  return {
    deep,
    rem,
    core,
    unspecified: resolvedUnspecified,
    awake,
    asleep,
    inBed: Math.max(target, asleep) + awake,
    coverage,
  };
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
