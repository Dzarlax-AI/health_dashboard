import type { Locale } from "../../i18n";
import { translate } from "../../i18n";
import { sleepMetrics, type SleepResources } from "./loader";

function dateOffset(days: number): string {
  const date = new Date("2026-08-05T12:00:00");
  date.setDate(date.getDate() - days);
  return date.toISOString().slice(0, 10);
}

export function sleepFixtureResources(locale: Locale): SleepResources {
  const values = Array.from({ length: 30 }, (_, index) => ({
    date: dateOffset(index),
    total: 6.7 + ((index * 7) % 16) / 10,
    deep: 0.8 + ((index * 3) % 6) / 10,
    rem: 1.2 + ((index * 5) % 8) / 10,
    core: 3.5 + ((index * 2) % 11) / 10,
    unspecified: index % 6 === 0 ? 0.45 : 0,
    awake: 0.25 + ((index * 2) % 5) / 10,
  }));
  const fields = {
    sleep_total: "total",
    sleep_deep: "deep",
    sleep_rem: "rem",
    sleep_core: "core",
    sleep_unspecified: "unspecified",
    sleep_awake: "awake",
  } as const;

  return {
    briefing: {
      date: "2026-08-05",
      sleep_quality: { score_pct: 82, duration_pct: 88, confidence: "final" },
      sleep_regularity_index: 76,
      sleep_regularity_nights: 14,
    } as SleepResources["briefing"],
    section: {
      key: "sleep",
      title: translate(locale, "sleepDetailTitle"),
      summary: "",
      details: [],
      charts: [],
      explains: [
        { title: translate(locale, "sleepPhase_deep"), body: locale === "ru" ? "Глубокий сон поддерживает физическое восстановление и чаще встречается в первой половине ночи." : "Deep sleep supports physical recovery and is concentrated in the first half of the night." },
        { title: "REM", body: locale === "ru" ? "REM связан с памятью, обучением и эмоциональной регуляцией." : "REM supports memory, learning, and emotional regulation." },
        { title: translate(locale, "sleepRegularity"), body: locale === "ru" ? "Стабильное время сна и пробуждения важно независимо от общей продолжительности." : "Consistent sleep and wake times matter independently of duration." },
      ],
    },
    ai: {
      date: "2026-08-05",
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
        SLEEP: locale === "ru"
          ? "Сон получился достаточно долгим и цельным. Глубокая и REM-фазы близки к твоему обычному диапазону, а стабильное пробуждение поддерживает хороший ритм."
          : "Sleep was long and fairly continuous. Deep and REM stayed close to your usual range, while the steady wake time supports a healthy rhythm.",
      },
    },
    wake: {
      metric: "wake_time",
      from: dateOffset(29),
      to: "2026-08-05",
      values: values.map((value, index) => ({
        metric_name: "wake_time",
        metric_date: value.date,
        value_type: "timestamp",
        value_timestamp: `${value.date}T0${7 + (index % 2)}:${index % 3}0:00+02:00`,
        unit: "timestamp",
        state: "final",
        formula_version: "1",
        calculated_at: `${value.date}T09:00:00+02:00`,
      })),
    },
    session: { is_admin: true },
    metrics: Object.fromEntries(
      sleepMetrics.map((metric) => [
        metric,
        {
          metric,
          bucket: "day",
          agg: "AVG",
          points: values.map((value) => ({
            date: value.date,
            qty: value[fields[metric]],
            min: value[fields[metric]],
            max: value[fields[metric]],
          })),
        },
      ]),
    ),
    missing: [],
  };
}
