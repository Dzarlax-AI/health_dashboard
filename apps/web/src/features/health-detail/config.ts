export type HealthSectionKey = "activity" | "cardio" | "recovery";

export interface HealthSectionConfig {
  key: HealthSectionKey;
  primaryMetric?: string;
  titleKey: "healthDetailActivity" | "healthDetailCardio" | "healthDetailRecovery";
  context: "summary" | "recovery-ai";
}

export const healthSectionConfigs: Record<HealthSectionKey, HealthSectionConfig> = {
  activity: {
    key: "activity",
    primaryMetric: "step_count",
    titleKey: "healthDetailActivity",
    context: "summary",
  },
  cardio: {
    key: "cardio",
    primaryMetric: "vo2_max",
    titleKey: "healthDetailCardio",
    context: "summary",
  },
  recovery: {
    key: "recovery",
    titleKey: "healthDetailRecovery",
    context: "recovery-ai",
  },
};

export function resolveHealthSection(pathname: string): HealthSectionConfig | undefined {
  const key = pathname.slice(1);
  return Object.hasOwn(healthSectionConfigs, key)
    ? healthSectionConfigs[key as HealthSectionKey]
    : undefined;
}
