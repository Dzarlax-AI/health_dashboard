import type { SleepDurationComparison } from "../../api/client";
import { translate, type Locale } from "../../i18n";

import { dateLabel } from "./dateLabel";

export function SleepComparisonCard({ value, locale, onToday = false }: {
  value: SleepDurationComparison;
  locale: Locale;
  onToday?: boolean;
}) {
  const t = (key: Parameters<typeof translate>[1]) => translate(locale, key);
  const duration = (minutes: number) =>
    `${Math.floor(minutes / 60)} ${t("sleepHoursShort")} ${minutes % 60} ${t("sleepComparisonMinute")}`;
  const statusCopy = {
    available: "sleepComparisonUnavailable",
    unavailable: "sleepComparisonUnavailable",
    no_night: "sleepComparisonNoNight",
    ineligible_night: "sleepComparisonIneligible",
    insufficient_history: "sleepComparisonInsufficient",
  } as const;
  const delta = value.delta_minutes;
  const details = <>
    {onToday && (value.status === "available" || value.status === "insufficient_history") && <p>{t("sleepComparisonBasis")}</p>}
    {value.reference_minutes !== null && <p>{t("sleepComparisonReference")} {duration(value.reference_minutes)}</p>}
    {value.eligible_nights !== null && <p>{t("sleepComparisonCoverage")} {value.eligible_nights} · {dateLabel(value.window_start, locale, true)} – {dateLabel(value.window_end_exclusive, locale, true)} ({t("sleepComparisonEndExclusive")})</p>}
    {value.source && <p>{t("sleepComparisonSource")} {value.source}</p>}
    {value.rule_version && <p>{t("sleepComparisonMethod")} <code>{value.rule_version}</code></p>}
  </>;
  return (
    <section className="sleep-section surface" aria-labelledby="sleep-comparison-title">
      <div className="sleep-section__title">
        <div><p>{dateLabel(value.wake_date, locale)}</p><h2 id="sleep-comparison-title">{t("sleepComparisonTitle")}</h2></div>
        {delta !== null && <strong>{delta === 0 ? t("sleepComparisonUsual") :
          t(delta < 0 ? "sleepComparisonLess" : "sleepComparisonMore").replace("{minutes}", String(Math.abs(delta)))}</strong>}
      </div>
      {value.duration_minutes !== null && <p>{t("sleepComparisonRecorded")} {duration(value.duration_minutes)}</p>}
      {value.status !== "available" && <p>{t(statusCopy[value.status])}</p>}
      {value.finalization === "provisional" && <p className="sleep-coverage">{t("sleepComparisonPreliminary")}</p>}
      {onToday
        ? <details><summary>{t("sleepComparisonWhy")}</summary>{details}</details>
        : details}
    </section>
  );
}
