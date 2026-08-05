import { useEffect, useMemo, useState } from "react";

import { ClientApiError, getAIBriefing, type AIBriefingResponse } from "../../api/client";
import { AppHeader } from "../../components/AppHeader";
import { StatusPanel } from "../../components/StatusPanel";
import { resolveLocale, translate, type Locale } from "../../i18n";
import { sleepFixtureResources } from "./fixtures";
import { loadSleepResources, type SleepResources } from "./loader";
import { buildSleepDays, sleepInsight, type SleepDay } from "./model";

type SleepState =
  | { status: "loading" }
  | { status: "ready"; resources: SleepResources }
  | { status: "unauthenticated" }
  | { status: "error"; message: string };

const phaseKeys = ["deep", "rem", "core", "unspecified", "awake"] as const;

function hours(value: number, locale: Locale): string {
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(value)} ${translate(locale, "sleepHoursShort")}`;
}

function time(value: string | undefined, locale: Locale): string {
  if (!value) return "—";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return "—";
  return new Intl.DateTimeFormat(locale, { hour: "2-digit", minute: "2-digit" }).format(parsed);
}

function dateLabel(date: string, locale: Locale, compact = false): string {
  return new Intl.DateTimeFormat(locale, compact
    ? { day: "numeric", month: "short" }
    : { weekday: "short", day: "numeric", month: "long" }
  ).format(new Date(`${date}T12:00:00`));
}

function phaseTotal(day: SleepDay): number {
  return phaseKeys.reduce((sum, key) => sum + day[key], 0);
}

function PhaseBar({ day, label, locale }: { day: SleepDay; label: string; locale: Locale }) {
  const total = Math.max(phaseTotal(day), day.total + day.awake, 0.01);
  return (
    <button className="sleep-history__row" type="button" aria-label={`${label}: ${hours(day.total, locale)}`}>
      <span className="sleep-history__label">{label}</span>
      <span className="sleep-phase-bar" aria-hidden="true">
        {phaseKeys.map((key) =>
          day[key] > 0 ? (
            <span
              key={key}
              className={`sleep-phase-bar__segment sleep-phase-bar__segment--${key}`}
              style={{ width: `${Math.max((day[key] / total) * 100, 1)}%` }}
            />
          ) : null,
        )}
      </span>
      <strong>{day.total.toFixed(1)}</strong>
    </button>
  );
}

function ScoreRing({ score, duration, locale }: { score?: number; duration?: number; locale: Locale }) {
  const value = Math.max(0, Math.min(100, score ?? ((duration ?? 0) / 9) * 100));
  return (
    <div className="sleep-score" style={{ "--sleep-score": value } as React.CSSProperties}>
      <div className="sleep-score__inner">
        <strong>{score !== undefined ? `${score}%` : duration !== undefined ? hours(duration, locale) : "—"}</strong>
        <span>{score !== undefined ? translate(locale, "sleepQuality") : duration !== undefined ? translate(locale, "sleepDuration") : ""}</span>
      </div>
    </div>
  );
}

function SleepReady({ resources, locale }: { resources: SleepResources; locale: Locale }) {
  const days = useMemo(() => buildSleepDays(resources), [resources]);
  const [range, setRange] = useState<7 | 30 | 90>(30);
  const [selectedDate, setSelectedDate] = useState(resources.briefing.date);
  const [historicAI, setHistoricAI] = useState<AIBriefingResponse>();
  const [todayAI, setTodayAI] = useState(resources.ai);
  const selected = days.find((day) => day.date === selectedDate) ?? days[0];
  const current = days.find((day) => day.date === resources.briefing.date) ?? days[0];
  const visible = days.slice(0, range).reverse();
  const score = resources.briefing.sleep_quality?.score_pct;

  useEffect(() => {
    if (!selectedDate || selectedDate === resources.briefing.date) {
      return;
    }
    const controller = new AbortController();
    getAIBriefing(locale, controller.signal, selectedDate)
      .then(setHistoricAI)
      .catch(() => setHistoricAI(undefined));
    return () => controller.abort();
  }, [locale, resources.briefing.date, selectedDate]);

  useEffect(() => {
    if (todayAI?.disabled || (!todayAI?.generating && sleepInsight(todayAI))) {
      return;
    }
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      getAIBriefing(locale, controller.signal).then(setTodayAI).catch(() => undefined);
    }, 60_000);
    return () => {
      controller.abort();
      window.clearTimeout(timer);
    };
  }, [locale, todayAI]);

  const currentInsight = sleepInsight(todayAI);
  const historicInsight =
    historicAI?.date === selectedDate ? sleepInsight(historicAI) : "";

  return (
    <main className="sleep-page">
      <section className="sleep-hero">
        <div className="sleep-hero__heading">
          <a className="sleep-back" href={`/?lang=${locale}`} aria-label={translate(locale, "sleepBack")}>←</a>
          <div>
            <p>{translate(locale, "today")}</p>
            <h1>{translate(locale, "sleepDetailTitle")}</h1>
          </div>
        </div>
        <div className="sleep-hero__grid">
          <ScoreRing
            score={score}
            locale={locale}
          />
          <div className="sleep-key-metrics">
            <article><span>{translate(locale, "sleepDuration")}</span><strong>{current ? hours(current.total, locale) : "—"}</strong></article>
            <article><span>{translate(locale, "sleepWake")}</span><strong>{time(current?.wake, locale)}</strong></article>
            <article><span>{translate(locale, "sleepRegularity")}</span><strong>{resources.briefing.sleep_regularity_index !== undefined ? `${resources.briefing.sleep_regularity_index.toFixed(0)} / 100` : "—"}</strong></article>
          </div>
        </div>
        {currentInsight ? (
          <article className="sleep-ai-card">
            <span>✦ {translate(locale, "sleepInsight")}</span>
            <p>{currentInsight}</p>
          </article>
        ) : todayAI?.generating && selectedDate === resources.briefing.date ? (
          <p className="sleep-ai-pending">{translate(locale, "sleepInsightPending")}</p>
        ) : null}
      </section>

      <section className="sleep-section surface">
        <div className="sleep-section__title">
          <div><p>{dateLabel(selected?.date ?? selectedDate, locale)}</p><h2>{translate(locale, "sleepPhases")}</h2></div>
          <strong>{selected ? hours(selected.total, locale) : "—"}</strong>
        </div>
        {selected ? (
          <>
            <div className="sleep-phase-summary">
              {phaseKeys.map((key) => (
                <div key={key}>
                  <i className={`sleep-phase-dot sleep-phase-dot--${key}`} />
                  <span>{translate(locale, `sleepPhase_${key}`)}</span>
                  <strong>{hours(selected[key], locale)}</strong>
                </div>
              ))}
            </div>
            {selectedDate !== resources.briefing.date ? (
              historicInsight ? (
                <article className="sleep-selected-insight">
                  <span>✦ {translate(locale, "sleepInsight")}</span>
                  <p>{historicInsight}</p>
                </article>
              ) : (
                <p className="empty-note">{translate(locale, "sleepNoSavedInsight")}</p>
              )
            ) : null}
          </>
        ) : <p className="empty-note">{translate(locale, "sleepNoData")}</p>}
      </section>

      <section className="sleep-section surface">
        <div className="sleep-history__header">
          <div><p>{translate(locale, "sleepHistoryEyebrow")}</p><h2>{translate(locale, "sleepHistory")}</h2></div>
          <div className="sleep-range" role="group" aria-label={translate(locale, "sleepRange")}>
            {([7, 30, 90] as const).map((daysCount) => (
              <button key={daysCount} type="button" aria-pressed={range === daysCount} onClick={() => setRange(daysCount)}>{daysCount}</button>
            ))}
          </div>
        </div>
        <div className="sleep-phase-legend">
          {phaseKeys.map((key) => <span key={key}><i className={`sleep-phase-dot sleep-phase-dot--${key}`} />{translate(locale, `sleepPhase_${key}`)}</span>)}
        </div>
        <div className="sleep-history">
          {visible.map((day) => (
            <div key={day.date} onClick={() => setSelectedDate(day.date)} className={day.date === selectedDate ? "is-selected" : ""}>
              <PhaseBar day={day} label={dateLabel(day.date, locale, true)} locale={locale} />
            </div>
          ))}
        </div>
      </section>

      {resources.section?.explains?.length ? (
        <section className="sleep-section">
          <p className="sleep-section__eyebrow">{translate(locale, "sleepLearn")}</p>
          <div className="sleep-explainers">
            {resources.section.explains.map((item) => (
              <details className="surface" key={item.title}>
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

export function SleepPage() {
  const params = new URLSearchParams(window.location.search);
  const locale = resolveLocale(params.get("lang"));
  const fixture = import.meta.env.VITE_ENABLE_FIXTURES === "true" && params.get("fixture") === "normal";
  const [state, setState] = useState<SleepState>(() =>
    fixture ? { status: "ready", resources: sleepFixtureResources(locale) } : { status: "loading" },
  );
  const [reloadKey, setReloadKey] = useState(0);

  useEffect(() => {
    document.documentElement.lang = locale;
    if (fixture) return;
    const controller = new AbortController();
    loadSleepResources(locale, controller.signal)
      .then((resources) => setState({ status: "ready", resources }))
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        if (error instanceof ClientApiError && error.status === 401) {
          setState({ status: "unauthenticated" });
        } else {
          setState({ status: "error", message: error instanceof Error ? error.message : translate(locale, "errorDetail") });
        }
      });
    return () => controller.abort();
  }, [fixture, locale, reloadKey]);

  return (
    <div className="app-shell sleep-shell">
      <AppHeader locale={locale} isAdmin={state.status === "ready" && state.resources.session?.is_admin} />
      {state.status === "ready" ? <SleepReady resources={state.resources} locale={locale} /> : (
        <div className="page standalone-status">
          <StatusPanel
            state={state.status === "loading" ? "loading" : "error"}
            title={translate(locale, state.status === "loading" ? "loadingTitle" : state.status === "unauthenticated" ? "signInTitle" : "errorTitle")}
            detail={state.status === "error" ? state.message : translate(locale, state.status === "loading" ? "loadingDetail" : "signInDetail")}
          />
          {state.status === "unauthenticated" ? <a className="primary-action" href={`/login?next=${encodeURIComponent(`/sleep?lang=${locale}`)}`}>{translate(locale, "signIn")}</a> : null}
          {state.status === "error" ? <button className="primary-action" onClick={() => { setState({ status: "loading" }); setReloadKey((value) => value + 1); }}>{translate(locale, "retry")}</button> : null}
        </div>
      )}
    </div>
  );
}
