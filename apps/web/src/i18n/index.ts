export const supportedLocales = ["en", "ru", "sr"] as const;

export type Locale = (typeof supportedLocales)[number];

const messages = {
  en: {
    appTitle: "Health",
    foundationsTitle: "Today",
    readiness: "Readiness",
    moderate: "Fair",
    normalSummary: "Today’s signals support a measured day.",
    normalDetail: "Recovery is useful now; keep the effort controlled.",
    partialSummary: "Useful signals are still arriving.",
    partialDetail: "The score is provisional and will update with fresh recovery data.",
    staleSummary: "Your last known pattern is shown carefully.",
    staleDetail: "Fresh wearable data is needed before treating this as today’s score.",
    unavailableTitle: "Today’s score is still taking shape",
    unavailableDetail: "The useful sections stay available while recovery data arrives.",
    errorTitle: "Today could not be refreshed",
    errorDetail: "Try again shortly. Existing health history remains available.",
    loadingTitle: "Refreshing today",
    loadingDetail: "Recent health signals are being prepared.",
    statusFinal: "Updated",
    statusPartial: "Provisional",
    statusStale: "Not fresh",
    energy: "Energy",
    sleep: "Sleep quality",
    normalFixture: "Normal",
    partialFixture: "Partial",
    staleFixture: "Stale",
    unavailableFixture: "Unavailable",
    errorFixture: "Error",
    loadingFixture: "Loading",
  },
  ru: {
    appTitle: "Здоровье",
    foundationsTitle: "Сегодня",
    readiness: "Готовность",
    moderate: "Умеренно",
    normalSummary: "Сегодняшние сигналы поддерживают размеренный день.",
    normalDetail: "Восстановление уже помогает — нагрузку лучше держать под контролем.",
    partialSummary: "Полезные сигналы ещё поступают.",
    partialDetail: "Оценка предварительная и обновится со свежими данными восстановления.",
    staleSummary: "Последний известный паттерн показан осторожно.",
    staleDetail: "Нужны свежие данные устройства, прежде чем считать это оценкой за сегодня.",
    unavailableTitle: "Сегодняшняя оценка ещё формируется",
    unavailableDetail: "Полезные разделы остаются доступны, пока поступают данные восстановления.",
    errorTitle: "Не удалось обновить сегодняшний день",
    errorDetail: "Попробуйте ещё раз немного позже. История здоровья остаётся доступна.",
    loadingTitle: "Обновляем сегодняшний день",
    loadingDetail: "Подготавливаем последние сигналы здоровья.",
    statusFinal: "Обновлено",
    statusPartial: "Предварительно",
    statusStale: "Не свежие данные",
    energy: "Энергия",
    sleep: "Качество сна",
    normalFixture: "Обычно",
    partialFixture: "Частично",
    staleFixture: "Устарело",
    unavailableFixture: "Недоступно",
    errorFixture: "Ошибка",
    loadingFixture: "Загрузка",
  },
  sr: {
    appTitle: "Zdravlje",
    foundationsTitle: "Danas",
    readiness: "Spremnost",
    moderate: "Umereno",
    normalSummary: "Današnji signali podržavaju odmeren dan.",
    normalDetail: "Oporavak već pomaže — zadrži opterećenje pod kontrolom.",
    partialSummary: "Korisni signali još pristižu.",
    partialDetail: "Ocena je privremena i osvežiće se novim podacima o oporavku.",
    staleSummary: "Poslednji poznati obrazac je prikazan oprezno.",
    staleDetail: "Potrebni su sveži podaci uređaja pre nego što ovo postane današnja ocena.",
    unavailableTitle: "Današnja ocena se još formira",
    unavailableDetail: "Korisni odeljci ostaju dostupni dok podaci o oporavku pristižu.",
    errorTitle: "Današnji podaci nisu osveženi",
    errorDetail: "Pokušaj ponovo uskoro. Istorija zdravlja ostaje dostupna.",
    loadingTitle: "Osvežavamo današnje podatke",
    loadingDetail: "Pripremamo najnovije zdravstvene signale.",
    statusFinal: "Osveženo",
    statusPartial: "Privremeno",
    statusStale: "Nije sveže",
    energy: "Energija",
    sleep: "Kvalitet sna",
    normalFixture: "Uobičajeno",
    partialFixture: "Delimično",
    staleFixture: "Zastarelo",
    unavailableFixture: "Nedostupno",
    errorFixture: "Greška",
    loadingFixture: "Učitavanje",
  },
} as const;

export type MessageKey = keyof (typeof messages)["en"];

export function isLocale(value: string | null | undefined): value is Locale {
  return supportedLocales.includes(value as Locale);
}

export function resolveLocale(value: string | null | undefined): Locale {
  return isLocale(value) ? value : "en";
}

export function translate(locale: Locale, key: MessageKey): string {
  return messages[locale][key];
}
