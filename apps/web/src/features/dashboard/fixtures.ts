import type {
  AIBriefingResponse,
  EnergyHistoryDayResponse,
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

const historyDates = [
  "2026-07-27",
  "2026-07-28",
  "2026-07-29",
  "2026-07-30",
  "2026-07-31",
  "2026-08-01",
  "2026-08-02",
] as const;

function localized(
  locale: Locale,
  copy: Record<Locale, string>,
): string {
  return copy[locale];
}

export function resolveFixture(value: string | null): FixtureName {
  return fixtureNames.includes(value as FixtureName)
    ? (value as FixtureName)
    : "normal";
}

function briefing(locale: Locale, fixture: FixtureName): HealthBriefingResponse {
  const partial = fixture === "partial";
  const stale = fixture === "stale";
  const unavailable = fixture === "unavailable";

  return {
    date: unavailable ? "" : "2026-08-02",
    greeting: localized(locale, {
      en: "Good morning",
      ru: "Доброе утро",
      sr: "Dobro jutro",
    }),
    overall: "fair",
    readiness_score: 62,
    readiness_band: "fair",
    readiness_label: localized(locale, { en: "Fair", ru: "Умеренно", sr: "Umereno" }),
    readiness_tip: localized(locale, {
      en: "Keep the effort controlled and leave some reserve.",
      ru: "Держи нагрузку под контролем и оставь запас.",
      sr: "Drži opterećenje pod kontrolom i ostavi malo rezerve.",
    }),
    readiness_raw_score: 68,
    readiness_display_score: 65,
    readiness_confidence: partial ? "provisional" : stale ? "low" : "final",
    readiness_today: 65,
    readiness_today_band: "fair",
    readiness_today_label: localized(locale, { en: "Fair", ru: "Умеренно", sr: "Umereno" }),
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
        ? localized(locale, {
            en: "Recovery data is still arriving.",
            ru: "Данные восстановления ещё поступают.",
            sr: "Podaci o oporavku još pristižu.",
          })
        : stale
          ? localized(locale, {
              en: "The last stable pattern is shown.",
              ru: "Показан последний устойчивый паттерн.",
              sr: "Prikazan je poslednji stabilan obrazac.",
            })
          : "",
    },
    recovery_pct: 64,
    correlation: [],
    highlights: [],
    insights: [],
    metric_cards: [
      {
        metric: "heart_rate_variability",
        name: localized(locale, { en: "HRV", ru: "ВСР", sr: "HRV" }),
        trend_30d_pct: 8,
        trend_7d_pct: 5,
        trend_label: localized(locale, {
          en: "above usual",
          ru: "выше обычного",
          sr: "iznad uobičajenog",
        }),
        trend_pct: 5,
        trend_status: "good",
        unit: "ms",
        value: "60.9",
      },
      {
        metric: "resting_heart_rate",
        name: localized(locale, {
          en: "Resting heart rate",
          ru: "Пульс в покое",
          sr: "Puls u mirovanju",
        }),
        trend_30d_pct: -2,
        trend_7d_pct: -1,
        trend_label: localized(locale, { en: "stable", ru: "стабильно", sr: "stabilno" }),
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
        summary: localized(locale, {
          en: "9h 40m with a useful reserve.",
          ru: "9 ч 40 мин сна, хороший запас.",
          sr: "9 č 40 min sna, uz dobru rezervu.",
        }),
        title: localized(locale, { en: "Sleep", ru: "Сон", sr: "San" }),
      },
      {
        details: null,
        icon: "♡",
        key: "recovery",
        status: "fair",
        summary: localized(locale, {
          en: "HRV is above your usual range.",
          ru: "ВСР выше обычного.",
          sr: "HRV je iznad tvog uobičajenog raspona.",
        }),
        title: localized(locale, {
          en: "Recovery",
          ru: "Восстановление",
          sr: "Oporavak",
        }),
      },
      {
        details: null,
        icon: "↗",
        key: "activity",
        status: "fair",
        summary: localized(locale, {
          en: "Today’s load is still low.",
          ru: "Сегодня нагрузка пока низкая.",
          sr: "Današnje opterećenje je još nisko.",
        }),
        title: localized(locale, { en: "Activity", ru: "Активность", sr: "Aktivnost" }),
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
      verdict_label: localized(locale, { en: "Charged", ru: "Заряжен", sr: "Napunjeno" }),
      verdict_reason: localized(locale, {
        en: "Your reserve is full enough for a challenge.",
        ru: "Запас полный — сегодня можно бросить себе вызов.",
        sr: "Rezerva je dovoljno puna za današnji izazov.",
      }),
    },
    today_guidance: {
      action: "moderate",
      confidence: partial ? "provisional" : "final",
      label: localized(locale, {
        en: "Measured day",
        ru: "Размеренный день",
        sr: "Odmeren dan",
      }),
      reason: localized(locale, {
        en: "Solid recovery supports a controlled effort.",
        ru: "Хорошее восстановление поддерживает умеренную нагрузку.",
        sr: "Dobar oporavak podržava kontrolisano opterećenje.",
      }),
      summary: localized(locale, {
        en: "You can move with more confidence today.",
        ru: "Сегодня можно двигаться увереннее.",
        sr: "Danas možeš da se krećeš sa više samopouzdanja.",
      }),
    },
  };
}

const readinessHistory: ReadinessHistoryResponse = {
  points: [58, 61, 60, 66, 63, 65, 65].map((score, index) => ({
    date: historyDates[index],
    score,
  })),
};

const energyHistory: EnergyHistoryDayResponse = {
  granularity: "day",
  points: [42, 55, 47, 68, 62, 75, 81].map((current_eod, index) => ({
    capacity: 94,
    current_eod,
    date: historyDates[index],
    drain: 20,
    verdict: "moderate",
  })),
};

function ai(locale: Locale): AIBriefingResponse {
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
        body: localized(locale, {
          en: "Sleep duration was strong, but the latest signals are still settling.",
          ru: "Продолжительность сна была хорошей, но последние сигналы ещё стабилизируются.",
          sr: "Trajanje sna je bilo dobro, ali se najnoviji signali još stabilizuju.",
        }),
        header: localized(locale, {
          en: "Sleep",
          ru: "Сон",
          sr: "San",
        }),
        key: "SLEEP",
      },
      {
        body: localized(locale, {
          en: "Yesterday stayed light enough to leave room for recovery.",
          ru: "Вчерашняя нагрузка оставила достаточно пространства для восстановления.",
          sr: "Jučerašnje opterećenje ostavilo je dovoljno prostora za oporavak.",
        }),
        header: localized(locale, {
          en: "Yesterday",
          ru: "Вчера",
          sr: "Juče",
        }),
        key: "YESTERDAY",
      },
      {
        body: localized(locale, {
          en: "A long night restored your reserve, while HRV sits above your usual level.",
          ru: "Длинный сон заметно восстановил запас. ВСР тоже выше твоего обычного уровня.",
          sr: "Dug san je obnovio rezervu, dok je HRV iznad tvog uobičajenog nivoa.",
        }),
        header: localized(locale, {
          en: "A deserved rebound",
          ru: "Заслуженный отскок",
          sr: "Zaslužen oporavak",
        }),
        key: "RECOVERY",
      },
      {
        body: localized(locale, {
          en: "Keep the day flexible and choose moderate activity while the remaining signals settle.",
          ru: "Оставь день гибким и выбери умеренную активность, пока остальные сигналы стабилизируются.",
          sr: "Ostavi dan fleksibilnim i izaberi umerenu aktivnost dok se ostali signali stabilizuju.",
        }),
        header: localized(locale, {
          en: "Plan for today",
          ru: "План на сегодня",
          sr: "Plan za danas",
        }),
        key: "RECOMMENDATION",
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
