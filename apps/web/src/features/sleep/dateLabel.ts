import type { Locale } from "../../i18n";

export function dateLabel(date: string, locale: Locale, compact = false): string {
  const candidate = date.slice(0, 10);
  const parsed = new Date(`${candidate}T12:00:00Z`);
  if (
    !/^\d{4}-\d{2}-\d{2}$/.test(candidate) ||
    Number.isNaN(parsed.getTime()) ||
    parsed.toISOString().slice(0, 10) !== candidate
  ) return date;
  return new Intl.DateTimeFormat(locale, compact
    ? { timeZone: "UTC", day: "numeric", month: "short" }
    : { weekday: "short", timeZone: "UTC", day: "numeric", month: "long" }
  ).format(parsed);
}
