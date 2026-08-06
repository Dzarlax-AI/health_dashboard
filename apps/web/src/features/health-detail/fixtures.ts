import type { MetricDataResponse, SectionResponse } from "../../api/client";
import type { Locale } from "../../i18n";
import type { HealthSectionConfig } from "./config";
import type { HealthDetailResources } from "./loader";

export const healthDetailFixtureNames = ["normal", "partial", "empty"] as const;
export type HealthDetailFixtureName = (typeof healthDetailFixtureNames)[number];

export function resolveHealthDetailFixture(
  value: string | null,
): HealthDetailFixtureName | undefined {
  return healthDetailFixtureNames.includes(value as HealthDetailFixtureName)
    ? (value as HealthDetailFixtureName)
    : undefined;
}

function localized(locale: Locale, values: Record<Locale, string>): string {
  return values[locale];
}

function dateOffset(days: number): string {
  const value = new Date("2026-08-06T12:00:00");
  value.setDate(value.getDate() - days);
  return value.toISOString().slice(0, 10);
}

function points(metric: string, center: number, spread: number): MetricDataResponse {
  return {
    metric,
    bucket: "day",
    agg: metric === "step_count" || metric === "active_energy" || metric === "apple_exercise_time"
      ? "SUM"
      : "AVG",
    points: Array.from({ length: 90 }, (_, index) => {
      const qty = center + Math.sin(index * 0.72) * spread + ((index * 7) % 5) * spread * 0.12;
      return {
        date: dateOffset(89 - index),
        qty: Math.max(0, Number(qty.toFixed(1))),
        min: Math.max(0, Number((qty - spread * 0.2).toFixed(1))),
        max: Math.max(0, Number((qty + spread * 0.2).toFixed(1))),
      };
    }),
  };
}

function section(config: HealthSectionConfig, locale: Locale): SectionResponse {
  if (config.key === "activity") {
    return {
      key: config.key,
      title: localized(locale, { en: "Activity", ru: "Активность", sr: "Aktivnost" }),
      summary: localized(locale, {
        en: "Today is quieter than your recent baseline. A walk would close the gap without adding much strain.",
        ru: "Сегодня активности меньше твоего обычного уровня. Прогулка поможет закрыть разницу без лишней нагрузки.",
        sr: "Danas je aktivnost ispod tvog uobičajenog nivoa. Šetnja može da popuni razliku bez velikog opterećenja.",
      }),
      details: [
        { label: localized(locale, { en: "Steps", ru: "Шаги", sr: "Koraci" }), value: "6 842", trend: "stable", note: "" },
        { label: localized(locale, { en: "Active energy", ru: "Активная энергия", sr: "Aktivna energija" }), value: "438 kcal", trend: "positive", note: "" },
        { label: localized(locale, { en: "Exercise", ru: "Упражнения", sr: "Vežbanje" }), value: "34 min", trend: "positive", note: "" },
      ],
      charts: [
        { metric: "step_count", agg: "SUM", label: localized(locale, { en: "Steps", ru: "Шаги", sr: "Koraci" }), type: "bar", color: "#059669", color_dark: "#34d399" },
        { metric: "active_energy", agg: "SUM", label: localized(locale, { en: "Active energy", ru: "Активная энергия", sr: "Aktivna energija" }), unit: "kcal", type: "bar", color: "#d97706", color_dark: "#fbbf24" },
        { metric: "apple_exercise_time", agg: "SUM", label: localized(locale, { en: "Exercise", ru: "Упражнения", sr: "Vežbanje" }), unit: "min", type: "bar", color: "#2563eb", color_dark: "#60a5fa" },
      ],
      explains: [
        { title: localized(locale, { en: "Your steps baseline", ru: "Твоя база по шагам", sr: "Tvoja osnova koraka" }), body: localized(locale, { en: "The comparison uses your recent personal average, not a universal step target.", ru: "Сравнение строится с твоим недавним средним, а не с универсальной целью по шагам.", sr: "Poređenje koristi tvoj skorašnji prosek, ne univerzalni cilj." }) },
        { title: localized(locale, { en: "Exercise time", ru: "Время упражнений", sr: "Vreme vežbanja" }), body: localized(locale, { en: "Active minutes are most useful as a weekly pattern rather than a single-day verdict.", ru: "Активные минуты полезнее оценивать как недельный паттерн, а не как приговор одному дню.", sr: "Aktivne minute je korisnije pratiti kao nedeljni obrazac." }) },
      ],
    };
  }
  if (config.key === "cardio") {
    return {
      key: config.key,
      title: localized(locale, { en: "Heart & breathing", ru: "Сердце и дыхание", sr: "Srce i disanje" }),
      summary: localized(locale, {
        en: "Oxygen and breathing remain stable. VO₂ max is holding close to your recent range.",
        ru: "Кислород и дыхание стабильны. VO₂ max держится рядом с твоим недавним диапазоном.",
        sr: "Kiseonik i disanje su stabilni. VO₂ max je blizu tvog skorašnjeg raspona.",
      }),
      details: [
        { label: "SpO₂", value: "96,5%", trend: "stable", note: "" },
        { label: "VO₂ max", value: "34,4 ml/kg/min", trend: "stable", note: "" },
        { label: localized(locale, { en: "Respiratory rate", ru: "Частота дыхания", sr: "Respiratorni ritam" }), value: "17,3 br/min", trend: "stable", note: "" },
      ],
      charts: [
        { metric: "blood_oxygen_saturation", agg: "AVG", label: "SpO₂", unit: "%", type: "line", color: "#06b6d4", color_dark: "#22d3ee" },
        { metric: "vo2_max", agg: "AVG", label: "VO₂ max", unit: "ml/kg/min", type: "line", color: "#8b5cf6", color_dark: "#a78bfa" },
        { metric: "respiratory_rate", agg: "AVG", label: localized(locale, { en: "Respiratory rate", ru: "Частота дыхания", sr: "Respiratorni ritam" }), unit: "br/min", type: "line", color: "#0ea5e9", color_dark: "#38bdf8" },
      ],
      explains: [
        { title: "VO₂ max", body: localized(locale, { en: "The personal trend is more useful than a single absolute reading.", ru: "Личный тренд полезнее отдельного абсолютного значения.", sr: "Lični trend je korisniji od jedne apsolutne vrednosti." }) },
        { title: "SpO₂", body: localized(locale, { en: "Wearable readings are best interpreted as a repeated pattern.", ru: "Показания носимого устройства лучше оценивать как повторяющийся паттерн.", sr: "Očitavanja uređaja je najbolje tumačiti kao ponavljajući obrazac." }) },
      ],
    };
  }
  return {
    key: config.key,
    title: localized(locale, { en: "Recovery", ru: "Восстановление", sr: "Oporavak" }),
    summary: localized(locale, { en: "Recovery is moderate.", ru: "Восстановление умеренное.", sr: "Oporavak je umeren." }),
    details: [
      { label: "HRV", value: "62,9 ms", trend: "positive", note: "" },
      { label: localized(locale, { en: "Resting heart rate", ru: "Пульс в покое", sr: "Puls u miru" }), value: "58 bpm", trend: "stable", note: "" },
      { label: localized(locale, { en: "Readiness", ru: "Готовность", sr: "Spremnost" }), value: "74/100", trend: "positive", note: "" },
    ],
    charts: [
      { metric: "heart_rate_variability", agg: "AVG", label: "HRV", unit: "ms", type: "line", color: "#e11d48", color_dark: "#fb7185" },
      { metric: "resting_heart_rate", agg: "AVG", label: localized(locale, { en: "Resting heart rate", ru: "Пульс в покое", sr: "Puls u miru" }), unit: "bpm", type: "line", color: "#f97316", color_dark: "#fb923c" },
      { virtual: true, label: localized(locale, { en: "Readiness", ru: "Готовность", sr: "Spremnost" }), unit: "%", type: "line", color: "#0ea5e9", color_dark: "#38bdf8" },
    ],
    explains: [
      { title: "HRV", body: localized(locale, { en: "Compare HRV to your own baseline and multi-day direction.", ru: "Сравнивай ВСР со своей базой и направлением за несколько дней.", sr: "Poredi HRV sa sopstvenom osnovom i višednevnim smerom." }) },
      { title: localized(locale, { en: "Readiness", ru: "Готовность", sr: "Spremnost" }), body: localized(locale, { en: "Readiness combines recovery signals and sleep; it is context, not a command.", ru: "Готовность объединяет восстановление и сон; это контекст, а не команда.", sr: "Spremnost spaja oporavak i san; ona je kontekst, ne naredba." }) },
    ],
  };
}

export function healthDetailFixtureResources(
  config: HealthSectionConfig,
  locale: Locale,
  fixture: HealthDetailFixtureName,
): HealthDetailResources {
  const sectionData = section(config, locale);
  const sourceMetrics: Record<string, MetricDataResponse> = {
    step_count: points("step_count", 7200, 1900),
    active_energy: points("active_energy", 470, 110),
    apple_exercise_time: points("apple_exercise_time", 32, 13),
    blood_oxygen_saturation: points("blood_oxygen_saturation", 96.4, 0.8),
    vo2_max: points("vo2_max", 34.5, 1.2),
    respiratory_rate: points("respiratory_rate", 17.2, 1),
    heart_rate_variability: points("heart_rate_variability", 51, 9),
    resting_heart_rate: points("resting_heart_rate", 59, 4),
  };
  const metrics = Object.fromEntries(
    (sectionData.charts ?? [])
      .filter((chart) => chart.metric)
      .map((chart) => [chart.metric!, sourceMetrics[chart.metric!]]),
  );
  if (fixture === "empty") {
    Object.keys(metrics).forEach((metric) => {
      metrics[metric] = { ...metrics[metric], points: [] };
    });
  }
  if (fixture === "partial") {
    const firstMetric = Object.keys(metrics)[0];
    if (firstMetric) metrics[firstMetric] = { ...metrics[firstMetric], points: metrics[firstMetric].points?.slice(-12) };
  }

  return {
    briefing: {
      date: "2026-08-06",
      readiness: 74,
      readiness_today: 74,
      readiness_display_score: 74,
      sections: [{
        key: config.key,
        title: sectionData.title,
        icon: "",
        status: config.key === "activity" ? "fair" : "good",
        status_label: localized(locale, { en: "Within your range", ru: "В твоём диапазоне", sr: "U tvom rasponu" }),
        summary: sectionData.summary,
        details: sectionData.details,
      }],
    } as unknown as HealthDetailResources["briefing"],
    section: sectionData,
    ai: config.key === "recovery" ? {
      date: "2026-08-06",
      lang: locale,
      disabled: false,
      generating: false,
      insight: "",
      summary: "",
      sleep: "",
      yesterday: "",
      recovery: "",
      recommendation: "",
      sections: [],
      blocks: {
        RECOVERY: localized(locale, {
          en: "HRV is above your usual level while resting heart rate remains stable. Recovery looks useful, but it is still worth keeping some reserve.",
          ru: "ВСР выше твоего обычного уровня, а пульс в покое остаётся стабильным. Восстановление выглядит хорошим, но небольшой запас сегодня всё же стоит оставить.",
          sr: "HRV je iznad tvog uobičajenog nivoa, dok je puls u miru stabilan. Oporavak izgleda dobro, ali vredi ostaviti malu rezervu.",
        }),
      },
    } : undefined,
    session: { is_admin: true },
    readiness: {
      points: Array.from({ length: 90 }, (_, index) => ({
        date: dateOffset(89 - index),
        score: Math.round(70 + Math.sin(index * 0.55) * 9),
        band: "fair" as const,
      })),
    },
    metrics,
    from: dateOffset(89),
    to: "2026-08-06",
    missing: fixture === "partial" ? ["supporting-fixture"] : [],
  };
}
