import type {
  AIBriefingResponse,
  EnergyHistoryResponse,
  HealthBriefingResponse,
  ReadinessHistoryResponse,
} from "../../api/client";
import type { Locale } from "../../i18n";
import type { DashboardResources } from "./loader";

export const fixtureNames = [
  "normal",
  "partial",
  "stale",
  "loading",
  "unavailable",
  "error",
] as const;

export type FixtureName = (typeof fixtureNames)[number];

export function resolveFixture(value: string | null): FixtureName {
  return fixtureNames.includes(value as FixtureName)
    ? (value as FixtureName)
    : "normal";
}

function briefing(locale: Locale, fixture: FixtureName): HealthBriefingResponse {
  const partial = fixture === "partial";
  const stale = fixture === "stale";
  const unavailable = fixture === "unavailable";
  const russian = locale === "ru";

  return {
    date: unavailable ? "" : "2026-08-02",
    greeting: russian ? "Доброе утро" : "Good morning",
    overall: "fair",
    readiness_score: 62,
    readiness_band: "fair",
    readiness_label: russian ? "Умеренно" : "Fair",
    readiness_tip: russian
      ? "Держи нагрузку под контролем и оставь запас."
      : "Keep the effort controlled and leave some reserve.",
    readiness_raw_score: 68,
    readiness_display_score: 65,
    readiness_confidence: partial ? "provisional" : stale ? "low" : "final",
    readiness_today: 65,
    readiness_today_band: "fair",
    readiness_today_label: russian ? "Умеренно" : "Fair",
    readiness_serving: {
      status: unavailable
        ? "missing"
        : partial
          ? "data_accruing"
          : stale
            ? "stale"
            : "fresh",
      confidence: partial ? "provisional" : stale || unavailable ? "low" : "final",
      reason: partial
        ? russian
          ? "Данные восстановления ещё поступают."
          : "Recovery data is still arriving."
        : stale
          ? russian
            ? "Показан последний устойчивый паттерн."
            : "The last stable pattern is shown."
          : "",
    },
    recovery_pct: 64,
    correlation: [],
    highlights: [],
    insights: [],
    metric_cards: [
      {
        metric: "heart_rate_variability",
        name: russian ? "ВСР" : "HRV",
        trend_30d_pct: 8,
        trend_7d_pct: 5,
        trend_label: russian ? "выше обычного" : "above usual",
        trend_pct: 5,
        trend_status: "good",
        unit: "ms",
        value: "60.9",
      },
      {
        metric: "resting_heart_rate",
        name: russian ? "Пульс в покое" : "Resting heart rate",
        trend_30d_pct: -2,
        trend_7d_pct: -1,
        trend_label: russian ? "стабильно" : "stable",
        trend_pct: -1,
        trend_status: "good",
        unit: "bpm",
        value: "58",
      },
    ],
    sections: [
      {
        details: null,
        icon: "☾",
        key: "sleep",
        status: "good",
        summary: russian ? "9 ч 40 мин сна, хороший запас." : "9h 40m with a useful reserve.",
        title: russian ? "Сон" : "Sleep",
      },
      {
        details: null,
        icon: "♡",
        key: "recovery",
        status: "fair",
        summary: russian ? "ВСР выше обычного." : "HRV is above your usual range.",
        title: russian ? "Восстановление" : "Recovery",
      },
      {
        details: null,
        icon: "↗",
        key: "activity",
        status: "fair",
        summary: russian ? "Сегодня нагрузка пока низкая." : "Today’s load is still low.",
        title: russian ? "Активность" : "Activity",
      },
    ],
    sleep: {
      awake_avg: 0.3,
      deep_avg: 1.4,
      efficiency: 95,
      nights: 1,
      rem_avg: 2.4,
      total_avg: 9.67,
    },
    sleep_quality: {
      confidence: partial ? "provisional" : "final",
      duration_pct: 96,
      score_pct: 94,
      structure_pct: 92,
    },
    energy_bank: {
      action_verdict: "moderate",
      capacity: 94,
      current: 81,
      drain_so_far: 13,
      hrv_z_raw: 0.7,
      strain: 12,
      stress: 10,
      verdict_label: russian ? "Заряжен" : "Charged",
      verdict_reason: russian
        ? "Запас полный — сегодня можно бросить себе вызов."
        : "Your reserve is full enough for a challenge.",
    },
    today_guidance: {
      action: "moderate",
      confidence: partial ? "provisional" : "final",
      label: russian ? "Размеренный день" : "Measured day",
      reason: russian
        ? "Хорошее восстановление поддерживает умеренную нагрузку."
        : "Solid recovery supports a controlled effort.",
      summary: russian
        ? "Сегодня можно двигаться увереннее."
        : "You can move with more confidence today.",
    },
  };
}

const readinessHistory: ReadinessHistoryResponse = {
  points: [58, 61, 60, 66, 63, 65, 65].map((score, index) => ({
    date: ["2026-07-27", "2026-07-28", "2026-07-29", "2026-07-30", "2026-07-31", "2026-08-01", "2026-08-02"][index],
    score,
  })),
};

const energyHistory: EnergyHistoryResponse = {
  granularity: "day",
  points: [42, 55, 47, 68, 62, 75, 81].map((current_eod, index) => ({
    capacity: 94,
    current_eod,
    date: ["2026-07-27", "2026-07-28", "2026-07-29", "2026-07-30", "2026-07-31", "2026-08-01", "2026-08-02"][index],
    drain: 20,
    verdict: "moderate",
  })),
};

function ai(locale: Locale): AIBriefingResponse {
  const russian = locale === "ru";
  return {
    blocks: {},
    date: "2026-08-02",
    disabled: false,
    generating: false,
    insight: "",
    lang: locale,
    recommendation: "",
    recovery: "",
    sections: [
      {
        body: russian
          ? "Длинный сон заметно восстановил запас. ВСР тоже выше твоего обычного уровня."
          : "A long night restored your reserve, while HRV sits above your usual level.",
        header: russian ? "Заслуженный отскок" : "A deserved rebound",
        key: "RECOVERY",
      },
    ],
    sleep: "",
    yesterday: "",
  };
}

export function fixtureResources(
  locale: Locale,
  fixture: FixtureName,
): DashboardResources {
  return {
    briefing: briefing(locale, fixture),
    ai: ai(locale),
    readinessHistory,
    energyHistory,
    session: { is_admin: true },
    missing: fixture === "partial" ? ["dashboard"] : [],
  };
}
