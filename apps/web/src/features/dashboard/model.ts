import type { HealthBriefingResponse } from "../../api/client";

export type DashboardDataState = "ready" | "partial" | "stale" | "unavailable";
export type ScoreTone = "readiness" | "energy" | "sleep";

export interface DashboardScore {
  label: string;
  value?: number;
  status: string;
  detail?: string;
  tone: ScoreTone;
}

export interface DashboardViewModel {
  date: string;
  state: DashboardDataState;
  readiness?: DashboardScore;
  energy?: DashboardScore;
  sleep?: DashboardScore;
  title: string;
  detail: string;
  alerts: HealthBriefingResponse["alerts"];
  sections: HealthBriefingResponse["sections"];
  metricCards: HealthBriefingResponse["metric_cards"];
  checkinAnswer?: string;
}

export function clampPercent(value: number): number {
  if (!Number.isFinite(value)) {
    return 0;
  }
  return Math.min(100, Math.max(0, Math.round(value)));
}

export function classifyBriefing(
  briefing: HealthBriefingResponse,
): DashboardDataState {
  if (!briefing.date || !briefing.readiness_serving) {
    return "unavailable";
  }

  const { status, confidence } = briefing.readiness_serving;
  if (status === "missing") {
    return "unavailable";
  }
  if (status === "stale") {
    return "stale";
  }
  if (status !== "fresh" || confidence !== "final") {
    return "partial";
  }
  return "ready";
}

export function buildDashboardViewModel(
  briefing: HealthBriefingResponse,
): DashboardViewModel {
  const state = classifyBriefing(briefing);
  const guidance = briefing.today_guidance;
  const readiness =
    state === "unavailable"
      ? undefined
      : {
          label: briefing.readiness_today_label || briefing.readiness_label,
          value: clampPercent(briefing.readiness_today),
          status: briefing.readiness_serving?.reason || briefing.readiness_tip,
          tone: "readiness" as const,
        };

  const energy = briefing.energy_bank
    ? {
        label: briefing.energy_bank.verdict_label || "Energy",
        value: clampPercent(briefing.energy_bank.current),
        status: briefing.energy_bank.verdict_reason,
        detail: `${Math.round(briefing.energy_bank.current)} / ${Math.round(briefing.energy_bank.capacity)}`,
        tone: "energy" as const,
      }
    : undefined;

  const quality = briefing.sleep_quality;
  const sleep =
    quality?.score_pct !== undefined && quality.confidence !== "provisional"
      ? {
          label: "",
          value: clampPercent(quality.score_pct),
          status: quality.confidence,
          detail: briefing.sleep
            ? `${briefing.sleep.total_avg.toFixed(1)} h`
            : undefined,
          tone: "sleep" as const,
        }
      : briefing.sleep
        ? {
            label: "",
            status: quality?.confidence || "duration",
            detail: `${briefing.sleep.total_avg.toFixed(1)} h`,
            tone: "sleep" as const,
          }
        : undefined;

  return {
    date: briefing.date,
    state,
    readiness,
    energy,
    sleep,
    title: guidance?.summary || briefing.greeting,
    detail: guidance?.reason || briefing.readiness_tip,
    alerts: briefing.alerts ?? [],
    sections: briefing.sections ?? [],
    metricCards: briefing.metric_cards ?? [],
    checkinAnswer:
      briefing.subjective_checkin?.status === "answered"
        ? briefing.subjective_checkin.answer
        : undefined,
  };
}
