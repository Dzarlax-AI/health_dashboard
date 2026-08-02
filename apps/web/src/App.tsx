import { ReadinessRing } from "./components/ReadinessRing";
import { ScoreCard } from "./components/ScoreCard";
import { StatusPanel } from "./components/StatusPanel";
import { Surface } from "./components/Surface";
import {
  resolveLocale,
  supportedLocales,
  translate,
  type Locale,
  type MessageKey,
} from "./i18n";

const fixtureNames = [
  "normal",
  "partial",
  "stale",
  "loading",
  "unavailable",
  "error",
] as const;

type FixtureName = (typeof fixtureNames)[number];

const fixtureLabelKeys: Record<FixtureName, MessageKey> = {
  normal: "normalFixture",
  partial: "partialFixture",
  stale: "staleFixture",
  loading: "loadingFixture",
  unavailable: "unavailableFixture",
  error: "errorFixture",
};

function resolveFixture(value: string | null): FixtureName {
  return fixtureNames.includes(value as FixtureName) ? (value as FixtureName) : "normal";
}

function fixtureHref(locale: Locale, fixture: FixtureName): string {
  const params = new URLSearchParams({ lang: locale, fixture });
  return `/?${params.toString()}`;
}

function HeroFixture({ locale, fixture }: { locale: Locale; fixture: FixtureName }) {
  if (fixture === "loading") {
    return (
      <StatusPanel
        state="loading"
        title={translate(locale, "loadingTitle")}
        detail={translate(locale, "loadingDetail")}
      />
    );
  }

  if (fixture === "unavailable") {
    return (
      <StatusPanel
        state="unavailable"
        title={translate(locale, "unavailableTitle")}
        detail={translate(locale, "unavailableDetail")}
      />
    );
  }

  if (fixture === "error") {
    return (
      <StatusPanel
        state="error"
        title={translate(locale, "errorTitle")}
        detail={translate(locale, "errorDetail")}
      />
    );
  }

  const score = fixture === "partial" ? 53 : 65;
  const status =
    fixture === "partial"
      ? translate(locale, "statusPartial")
      : fixture === "stale"
        ? translate(locale, "statusStale")
        : translate(locale, "moderate");
  const summaryKey =
    fixture === "partial"
      ? "partialSummary"
      : fixture === "stale"
        ? "staleSummary"
        : "normalSummary";
  const detailKey =
    fixture === "partial"
      ? "partialDetail"
      : fixture === "stale"
        ? "staleDetail"
        : "normalDetail";

  return (
    <>
      <ReadinessRing
        value={score}
        label={translate(locale, "readiness")}
        status={status}
      />
      <div className="today-hero__recommendation" data-resource-state={fixture}>
        <p className="today-hero__status">
          {fixture === "normal"
            ? translate(locale, "statusFinal")
            : fixture === "partial"
              ? translate(locale, "statusPartial")
              : translate(locale, "statusStale")}
        </p>
        <h1>{translate(locale, summaryKey)}</h1>
        <p>{translate(locale, detailKey)}</p>
      </div>
    </>
  );
}

export function App() {
  const params = new URLSearchParams(window.location.search);
  const locale = resolveLocale(params.get("lang"));
  const fixture = resolveFixture(params.get("fixture"));
  const hasSupportingScores = ["normal", "partial", "stale"].includes(fixture);

  return (
    <div className="app-shell" data-locale={locale} data-fixture={fixture}>
      <header className="app-header">
        <a className="app-header__brand" href={fixtureHref(locale, "normal")}>
          {translate(locale, "appTitle")}
        </a>
        <nav className="locale-switcher" aria-label="Language">
          {supportedLocales.map((candidate) => (
            <a
              key={candidate}
              href={fixtureHref(candidate, fixture)}
              aria-current={candidate === locale ? "page" : undefined}
            >
              {candidate.toUpperCase()}
            </a>
          ))}
        </nav>
      </header>

      <main className="page">
        <nav className="fixture-switcher" aria-label="Component states">
          {fixtureNames.map((candidate) => (
            <a
              key={candidate}
              href={fixtureHref(locale, candidate)}
              aria-current={candidate === fixture ? "page" : undefined}
            >
              {translate(locale, fixtureLabelKeys[candidate])}
            </a>
          ))}
        </nav>

        <Surface tone="glass" className="today-hero">
          <p className="today-hero__date">{translate(locale, "foundationsTitle")}</p>
          <div className="today-hero__layout">
            <HeroFixture locale={locale} fixture={fixture} />
          </div>
        </Surface>

        {hasSupportingScores ? (
          <section className="supporting-scores" aria-label="Supporting score primitives">
            <ScoreCard
              label={translate(locale, "energy")}
              value={72}
              status={translate(locale, "statusFinal")}
              tone="energy"
            />
            <ScoreCard
              label={translate(locale, "sleep")}
              value={77}
              status={translate(locale, "statusFinal")}
              tone="sleep"
            />
          </section>
        ) : null}
      </main>
    </div>
  );
}
