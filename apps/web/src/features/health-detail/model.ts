import type { HealthBriefingResponse, MetricDataResponse } from "../../api/client";
import type { Locale } from "../../i18n";
import type { HealthSectionConfig } from "./config";
import type { HealthDetailResources } from "./loader";

export type HistoryRange = 7 | 30 | 90 | "all";

export interface HealthTrend {
  key: string;
  label: string;
  unit: string;
  color?: string;
  kind: "line" | "bar";
  points: { date: string; value: number }[];
}

export interface HealthDetailModel {
  date: string;
  titleKey: HealthSectionConfig["titleKey"];
  status: "good" | "fair" | "low" | "unknown";
  statusLabel: string;
  heroValue: string;
  heroProgress?: number;
  heroComparison?: number;
  context: string;
  contextIsAI: boolean;
  details: NonNullable<HealthDetailResources["section"]>["details"];
  explains: NonNullable<HealthDetailResources["section"]>["explains"];
  trends: HealthTrend[];
  partial: boolean;
}

function localeNumber(locale: Locale, value: number, digits = 0): string {
  return new Intl.NumberFormat(locale, { maximumFractionDigits: digits }).format(value);
}

function briefingSection(
  briefing: HealthBriefingResponse,
  key: HealthSectionConfig["key"],
) {
  return briefing.sections?.find((section) => section.key === key);
}

function latestPoint(response?: MetricDataResponse) {
  return response?.points?.reduce<(NonNullable<MetricDataResponse["points"]>[number]) | undefined>(
    (latest, point) => (!latest || point.date > latest.date ? point : latest),
    undefined,
  );
}

function activityComparison(response?: MetricDataResponse): number | undefined {
  const points = [...(response?.points ?? [])].sort((a, b) => b.date.localeCompare(a.date));
  if (points.length < 8) return undefined;
  const current = points[0].qty;
  const baseline = points.slice(1, 31).reduce((sum, point) => sum + point.qty, 0) /
    Math.min(points.length - 1, 30);
  if (baseline <= 0) return undefined;
  return Math.max(0, Math.round((current / baseline) * 100));
}

function recoveryInsight(resources: HealthDetailResources): string {
  return resources.ai?.blocks?.RECOVERY || resources.ai?.recovery || "";
}

function trendPoints(response?: MetricDataResponse) {
  return (response?.points ?? [])
    .map((point) => ({ date: point.date.slice(0, 10), value: point.qty }))
    .filter((point) => point.date && Number.isFinite(point.value))
    .sort((a, b) => a.date.localeCompare(b.date));
}

export function buildHealthDetailModel(
  resources: HealthDetailResources,
  config: HealthSectionConfig,
  locale: Locale,
): HealthDetailModel {
  const section = briefingSection(resources.briefing, config.key);
  const primary = config.primaryMetric
    ? latestPoint(resources.metrics[config.primaryMetric])
    : undefined;
  const recoveryScore =
    resources.briefing.readiness_today ||
    resources.briefing.readiness_display_score ||
    resources.briefing.readiness_score;
  const activityRatio = activityComparison(resources.metrics.step_count);
  let heroValue = "—";
  if (config.key === "activity" && primary) {
    heroValue = localeNumber(locale, primary.qty);
  } else if (config.key === "cardio" && primary) {
    heroValue = localeNumber(locale, primary.qty, 1);
  } else if (config.key === "recovery" && recoveryScore > 0) {
    heroValue = `${localeNumber(locale, recoveryScore)}%`;
  }

  const ai = config.context === "recovery-ai" ? recoveryInsight(resources) : "";
  const chartTrends = (resources.section?.charts ?? []).flatMap<HealthTrend>((chart) => {
    if (chart.virtual) {
      const points = (resources.readiness?.points ?? []).map((point) => ({
        date: point.date.slice(0, 10),
        value: point.score,
      }));
      return points.length
        ? [{
            key: "readiness",
            label: chart.label,
            unit: chart.unit || "%",
            color: chart.color_dark || chart.color,
            kind: "line",
            points,
          }]
        : [];
    }
    if (!chart.metric) return [];
    const points = trendPoints(resources.metrics[chart.metric]);
    return points.length
      ? [{
          key: chart.metric,
          label: chart.label,
          unit: chart.unit || "",
          color: chart.color_dark || chart.color,
          kind: chart.type === "bar" ? "bar" : "line",
          points,
        }]
      : [];
  });

  return {
    date: resources.briefing.date,
    titleKey: config.titleKey,
    status: section?.status ?? "unknown",
    statusLabel: section?.status_label ?? "",
    heroValue,
    heroProgress:
      config.key === "activity"
        ? activityRatio === undefined ? undefined : Math.min(100, activityRatio)
        : config.key === "recovery" && recoveryScore > 0
          ? Math.max(0, Math.min(100, recoveryScore))
          : undefined,
    heroComparison: config.key === "activity" ? activityRatio : undefined,
    context: ai || section?.summary || resources.section?.summary || "",
    contextIsAI: Boolean(ai),
    details: resources.section?.details ?? section?.details ?? [],
    explains: resources.section?.explains ?? [],
    trends: chartTrends,
    partial: resources.missing.length > 0,
  };
}

export function pointsForRange(
  points: HealthTrend["points"],
  range: HistoryRange,
): HealthTrend["points"] {
  return range === "all" ? points : points.slice(-range);
}
