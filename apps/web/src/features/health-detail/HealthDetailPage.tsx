import { useEffect, useMemo, useState, type CSSProperties } from "react";

import { ClientApiError } from "../../api/client";
import { AppHeader } from "../../components/AppHeader";
import { LazyTrendChart } from "../../components/charts/LazyTrendChart";
import { StatusPanel } from "../../components/StatusPanel";
import { resolveLocale, translate, type Locale } from "../../i18n";
import type { HealthSectionConfig } from "./config";
import {
  healthDetailFixtureNames,
  healthDetailFixtureResources,
  resolveHealthDetailFixture,
} from "./fixtures";
import { loadHealthDetailResources, type HealthDetailResources } from "./loader";
import {
  buildHealthDetailModel,
  pointsForRange,
  type HealthDetailModel,
  type HistoryRange,
} from "./model";

type HealthDetailState =
  | { status: "loading" }
  | { status: "ready"; resources: HealthDetailResources }
  | { status: "unauthenticated" }
  | { status: "error"; message: string };

const ranges: HistoryRange[] = [7, 30, 90, "all"];

function dateLabel(value: string, locale: Locale): string {
  const date = new Date(`${value.slice(0, 10)}T12:00:00`);
  return Number.isNaN(date.getTime())
    ? value
    : new Intl.DateTimeFormat(locale, { day: "numeric", month: "long" }).format(date);
}

function heroLabel(config: HealthSectionConfig, locale: Locale): string {
  if (config.key === "activity") return translate(locale, "healthDetailSteps");
  if (config.key === "cardio") return translate(locale, "healthDetailVo2");
  return translate(locale, "readiness");
}

function HeroGauge({
  model,
  config,
  locale,
}: {
  model: HealthDetailModel;
  config: HealthSectionConfig;
  locale: Locale;
}) {
  const progress = model.heroProgress;
  return (
    <div
      className={`health-detail-gauge${progress === undefined ? " is-neutral" : ""}`}
      style={{ "--detail-progress": progress ?? 0 } as CSSProperties}
    >
      <div className="health-detail-gauge__inner">
        <strong>{model.heroValue}</strong>
        <span>{heroLabel(config, locale)}</span>
      </div>
    </div>
  );
}

function DetailStrip({
  model,
  config,
  locale,
}: {
  model: HealthDetailModel;
  config: HealthSectionConfig;
  locale: Locale;
}) {
  const primaryLabel = heroLabel(config, locale).toLocaleLowerCase(locale);
  const details = (model.details ?? []).filter(
    (detail) => detail.label.toLocaleLowerCase(locale) !== primaryLabel,
  );
  if (!details.length) return null;
  return (
    <section className="health-detail-kpis" aria-label={translate(locale, "healthDetailMain")}>
      {details.slice(0, 4).map((detail) => (
        <article key={`${detail.label}-${detail.value}`} data-trend={detail.trend}>
          <span>{detail.label}</span>
          <strong>{detail.value || "—"}</strong>
          {detail.note ? <small>{detail.note}</small> : null}
        </article>
      ))}
    </section>
  );
}

function HistorySection({
  model,
  locale,
  range,
  setRange,
}: {
  model: HealthDetailModel;
  locale: Locale;
  range: HistoryRange;
  setRange: (range: HistoryRange) => void;
}) {
  return (
    <section className="health-detail-section surface">
      <header className="health-detail-section__header">
        <div>
          <p>{translate(locale, "healthDetailRange")}</p>
          <h2>{translate(locale, "healthDetailHistory")}</h2>
        </div>
        <div className="health-detail-ranges" role="group" aria-label={translate(locale, "healthDetailRange")}>
          {ranges.map((candidate) => (
            <button
              key={candidate}
              type="button"
              className={candidate === range ? "is-active" : ""}
              aria-pressed={candidate === range}
              onClick={() => setRange(candidate)}
            >
              {candidate === "all" ? translate(locale, "healthDetailRangeAll") : candidate}
            </button>
          ))}
        </div>
      </header>
      <div className="health-detail-trends">
        {model.trends.length ? model.trends.map((trend) => {
          const points = pointsForRange(trend.points, range);
          return (
            <article className="health-detail-trend-card" key={trend.key}>
              <div className="health-detail-trend-card__heading">
                <h3>{trend.label}</h3>
                {trend.unit ? <span>{trend.unit}</span> : null}
              </div>
              {points.length >= 2 ? (
                <LazyTrendChart
                  ariaLabel={trend.label}
                  data={points.map((point) => ({
                    label: new Intl.DateTimeFormat(locale, { day: "numeric", month: "short" }).format(new Date(`${point.date}T12:00:00`)),
                    value: point.value,
                  }))}
                  tone={trend.key === "readiness" ? "readiness" : "energy"}
                  color={trend.color}
                  kind={trend.kind}
                  unit={trend.unit}
                />
              ) : (
                <p className="empty-note">{translate(locale, "healthDetailNoHistory")}</p>
              )}
            </article>
          );
        }) : (
          <p className="empty-note">{translate(locale, "healthDetailNoHistory")}</p>
        )}
      </div>
    </section>
  );
}

export function HealthDetailReady({
  resources,
  config,
  locale,
}: {
  resources: HealthDetailResources;
  config: HealthSectionConfig;
  locale: Locale;
}) {
  const model = useMemo(
    () => buildHealthDetailModel(resources, config, locale),
    [config, locale, resources],
  );
  const [range, setRange] = useState<HistoryRange>(30);

  return (
    <main className="health-detail-page" data-section={config.accent}>
      <section className="health-detail-hero">
        <div className="health-detail-hero__heading">
          <a className="health-detail-back" href={`/?lang=${locale}`} aria-label={translate(locale, "healthDetailBack")}>←</a>
          <div>
            <p>{dateLabel(model.date, locale)}</p>
            <h1>{translate(locale, model.titleKey)}</h1>
          </div>
        </div>
        <div className="health-detail-hero__body">
          <HeroGauge model={model} config={config} locale={locale} />
          <div className="health-detail-hero__copy">
            {model.statusLabel ? <span className="health-detail-status" data-status={model.status}>{model.statusLabel}</span> : null}
            <h2>{heroLabel(config, locale)}</h2>
            <p>{model.heroProgress !== undefined && config.key === "activity"
              ? `${model.heroComparison}% ${translate(locale, "healthDetailUsual")}`
              : model.heroProgress === undefined && config.key === "activity"
                ? translate(locale, "healthDetailNoBaseline")
                : translate(locale, "healthDetailMain")}</p>
          </div>
        </div>
        {model.context ? (
          <article className="health-detail-context">
            <span>✦ {translate(locale, model.contextIsAI ? "healthDetailRecoveryInsight" : "healthDetailContext")}</span>
            <p>{model.context}</p>
          </article>
        ) : null}
      </section>

      {model.partial ? <p className="health-detail-partial">{translate(locale, "healthDetailPartial")}</p> : null}
      <DetailStrip model={model} config={config} locale={locale} />
      <HistorySection model={model} locale={locale} range={range} setRange={setRange} />

      {model.explains?.length ? (
        <section className="health-detail-section surface">
          <div className="health-detail-section__header">
            <h2>{translate(locale, "healthDetailLearn")}</h2>
          </div>
          <div className="health-detail-explainers">
            {model.explains.map((item) => (
              <details key={item.title}>
                <summary>{item.title}</summary>
                <p>{item.body}</p>
              </details>
            ))}
          </div>
        </section>
      ) : null}
    </main>
  );
}

export function HealthDetailPage({ config }: { config: HealthSectionConfig }) {
  const params = new URLSearchParams(window.location.search);
  const locale = resolveLocale(params.get("lang"));
  const fixtureEnabled = import.meta.env.VITE_ENABLE_FIXTURES === "true";
  const fixture = fixtureEnabled ? resolveHealthDetailFixture(params.get("fixture")) : undefined;
  const [reloadKey, setReloadKey] = useState(0);
  const [liveState, setLiveState] = useState<HealthDetailState>({ status: "loading" });
  const fixtureState: HealthDetailState | undefined = fixture
    ? { status: "ready", resources: healthDetailFixtureResources(config, locale, fixture) }
    : undefined;
  const state = fixtureState ?? liveState;

  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);

  useEffect(() => {
    if (fixture) return;
    const controller = new AbortController();
    loadHealthDetailResources(config, locale, controller.signal)
      .then((resources) => setLiveState({ status: "ready", resources }))
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        if (error instanceof ClientApiError && error.status === 401) {
          setLiveState({ status: "unauthenticated" });
          return;
        }
        setLiveState({
          status: "error",
          message: error instanceof Error ? error.message : translate(locale, "errorDetail"),
        });
      });
    return () => controller.abort();
  }, [config, fixture, locale, reloadKey]);

  return (
    <div className="app-shell health-detail-shell">
      <AppHeader locale={locale} isAdmin={state.status === "ready" && state.resources.session?.is_admin} />
      {fixtureEnabled ? (
        <nav className="fixture-switcher health-detail-fixtures" aria-label={translate(locale, "fixtureNav")}>
          {healthDetailFixtureNames.map((candidate) => (
            <a
              key={candidate}
              href={`/${config.key}?${new URLSearchParams({ lang: locale, fixture: candidate }).toString()}`}
              aria-current={candidate === fixture ? "page" : undefined}
            >
              {candidate}
            </a>
          ))}
        </nav>
      ) : null}
      {state.status === "ready" ? (
        <HealthDetailReady resources={state.resources} config={config} locale={locale} />
      ) : (
        <main className="health-detail-page">
          <div className="standalone-status">
            <StatusPanel
              state={state.status === "loading" ? "loading" : "error"}
              title={translate(locale, state.status === "loading" ? "loadingTitle" : state.status === "unauthenticated" ? "signInTitle" : "errorTitle")}
              detail={state.status === "loading" ? translate(locale, "loadingDetail") : state.status === "unauthenticated" ? translate(locale, "signInDetail") : state.message}
            />
            {state.status === "unauthenticated" ? (
              <a className="primary-action" href={`/login?next=${encodeURIComponent(`/${config.key}?lang=${locale}`)}`}>{translate(locale, "signIn")}</a>
            ) : state.status === "error" ? (
              <button
                className="primary-action"
                type="button"
                onClick={() => {
                  setLiveState({ status: "loading" });
                  setReloadKey((value) => value + 1);
                }}
              >
                {translate(locale, "retry")}
              </button>
            ) : null}
          </div>
        </main>
      )}
    </div>
  );
}

export default HealthDetailPage;
