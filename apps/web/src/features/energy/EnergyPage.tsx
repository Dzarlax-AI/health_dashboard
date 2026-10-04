import { useEffect, useRef, useState, type CSSProperties } from "react";

import {
  ClientApiError,
  getEnergyHistory,
  getHealthBriefing,
  type HealthBriefingResponse,
  getSession,
  getTodayInsights,
  type EnergyHistoryDayResponse,
  type SessionResponse,
  type TodayInsightsResponse,
} from "../../api/client";
import { clearSessionRecoveryAttempt, recoverSessionOnUnauthorized } from "../../auth/sessionRecovery";
import { AppHeader } from "../../components/AppHeader";
import { LazyTrendChart } from "../../components/charts/LazyTrendChart";
import { InsightPair } from "../../components/InsightPair";
import { StatusPanel } from "../../components/StatusPanel";
import { fixtureResources } from "../dashboard/fixtures";
import { isTodayInsightsGenerationPending, todayInsightsPollDelayMs } from "../dashboard/aiPolling";
import { resolveLocale, translate } from "../../i18n";

type EnergyState =
  | { status: "loading" }
  | { status: "ready"; insights: TodayInsightsResponse; session?: SessionResponse }
  | { status: "error"; message: string }
  | { status: "unauthenticated" };

export function EnergyPage() {
  const params = new URLSearchParams(window.location.search);
  const locale = resolveLocale(params.get("lang"));
  const fixture = import.meta.env.VITE_ENABLE_FIXTURES === "true" && params.get("fixture") === "normal";
  const resources = fixture ? fixtureResources(locale, "normal") : undefined;
  const fixtureInsights = resources?.todayInsights;
  const [briefing, setBriefing] = useState<HealthBriefingResponse | undefined>(resources?.briefing);
  const [history, setHistory] = useState<EnergyHistoryDayResponse | undefined>(resources?.energyHistory);
  const [briefingFailed, setBriefingFailed] = useState(false);
  const [historyFailed, setHistoryFailed] = useState(false);
  const [state, setState] = useState<EnergyState>(() => fixtureInsights
    ? { status: "ready", insights: fixtureInsights }
    : { status: "loading" });
  const [reloadKey, setReloadKey] = useState(0);
  const pollAttempts = useRef(0);

  useEffect(() => {
    document.documentElement.lang = locale;
    if (fixture) return;
    const controller = new AbortController();
    getTodayInsights(locale, controller.signal)
      .then((insights) => {
        if (controller.signal.aborted) return;
        clearSessionRecoveryAttempt(window.sessionStorage);
        setState((current) => ({ status: "ready", insights,
          session: current.status === "ready" ? current.session : undefined }));
        void getSession(controller.signal).then((session) => {
          if (!controller.signal.aborted) setState((current) => current.status === "ready" ? { ...current, session } : current);
        }).catch(() => undefined);
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        if (recoverSessionOnUnauthorized(error, window.location, window.sessionStorage,
          (target) => window.location.assign(target))) return;
        if (error instanceof ClientApiError && error.status === 401) {
          setState({ status: "unauthenticated" });
        } else {
          setState({ status: "error", message: error instanceof Error ? error.message : translate(locale, "errorDetail") });
        }
      });
    return () => controller.abort();
  }, [fixture, locale, reloadKey]);

  useEffect(() => {
    if (fixture) return;
    const controller = new AbortController();
    const handleError = (error: unknown, setFailed: (failed: boolean) => void) => {
      if (controller.signal.aborted) return;
      if (recoverSessionOnUnauthorized(error, window.location, window.sessionStorage,
        (target) => window.location.assign(target))) return;
      setFailed(true);
    };
    // Current reserve, history and opinions load independently.
    void getHealthBriefing(locale, controller.signal).then((value) => {
      if (!controller.signal.aborted) setBriefing(value);
    }).catch((error: unknown) => handleError(error, setBriefingFailed));
    void getEnergyHistory(14, controller.signal).then((value) => {
      if (!controller.signal.aborted) setHistory(value);
    }).catch((error: unknown) => handleError(error, setHistoryFailed));
    return () => controller.abort();
  }, [fixture, locale]);

  useEffect(() => {
    if (state.status === "ready" && ["disabled", "ready"].includes(state.insights.generation.state)) {
      pollAttempts.current = 0;
    }
  }, [state]);

  useEffect(() => {
    const delay = state.status === "ready" ? todayInsightsPollDelayMs(state.insights, pollAttempts.current) : undefined;
    if (fixture || delay === undefined) return;
    let timer: number | undefined;
    const schedule = () => {
      if (timer !== undefined) window.clearTimeout(timer);
      timer = document.visibilityState === "visible" ? window.setTimeout(() => {
        if (state.status === "ready" && isTodayInsightsGenerationPending(state.insights)) {
          pollAttempts.current += 1;
        }
        setReloadKey((value) => value + 1);
      }, delay) : undefined;
    };
    schedule();
    document.addEventListener("visibilitychange", schedule);
    return () => {
      document.removeEventListener("visibilitychange", schedule);
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, [fixture, state]);

  const domain = state.status === "ready" ? state.insights.domains?.find((item) => item.key === "energy") : undefined;
  const bank = briefing?.energy_bank;
  const points = history?.points;
  const number = new Intl.NumberFormat(locale);
  return (
    <div className="app-shell health-detail-shell energy-shell">
      <AppHeader locale={locale} isAdmin={state.status === "ready" && state.session?.is_admin} />
      <main className="health-detail-page energy-page" data-section="energy">
        <section className="health-detail-hero">
          <div className="health-detail-hero__heading">
            <a className="health-detail-back" href={`/?lang=${locale}`} aria-label={translate(locale, "healthDetailBack")}>←</a>
            <div><p>{briefing?.date || translate(locale, "today")}</p><h1>{translate(locale, "energyDetailTitle")}</h1></div>
          </div>
          <div className="health-detail-hero__body">
            <div className={`health-detail-gauge${bank ? "" : " is-neutral"}`}
              style={{ "--detail-progress": bank ? Math.min(100, Math.max(0, bank.current)) : 0 } as CSSProperties}>
              <div className="health-detail-gauge__inner">
                <strong data-testid="energy-reserve">{bank ? number.format(bank.current) : "—"}</strong>
                <span>{translate(locale, "energyReserve")}</span>
              </div>
            </div>
            <div className="health-detail-hero__copy">
              {bank?.verdict_label ? <span className="health-detail-status">{bank.verdict_label}</span> : null}
              <h2>{translate(locale, "energyReserve")}</h2>
              {briefing?.readiness_serving?.status === "stale" ? <p>{translate(locale, "state_stale")}</p> : null}
              {briefing?.readiness_serving && briefing.readiness_serving.status !== "stale" && briefing.readiness_serving.confidence !== "final" ? <p>{translate(locale, "state_partial")}</p> : null}
              <p>{bank?.verdict_reason || translate(locale, briefingFailed ? "errorDetail" : briefing ? "unavailableDetail" : "loadingDetail")}</p>
            </div>
          </div>
        </section>
        {bank ? <section className="health-detail-kpis" aria-label={translate(locale, "healthDetailMain")}>
          {([
            ["energyCapacity", bank.capacity], ["energyDrain", bank.drain_so_far],
            ["energyStrain", bank.strain], ["energyStress", bank.stress],
          ] as const).map(([key, value]) => <article key={key}><span>{translate(locale, key)}</span><strong>{number.format(value)}</strong></article>)}
        </section> : null}
        {points?.length ? (
          <section className="health-detail-section surface">
            <div className="health-detail-section__header"><h2>{translate(locale, "energyTrend")}</h2></div>
            <LazyTrendChart ariaLabel={translate(locale, "energyTrend")} tone="energy" kind="bar"
              data={[...points].sort((a, b) => a.date.localeCompare(b.date)).slice(-14).map((point) => ({
                label: new Intl.DateTimeFormat(locale, { day: "numeric", month: "short" }).format(new Date(`${point.date}T12:00:00`)),
                value: point.current_eod,
              }))} />
            <details className="energy-history-details" open={points.length === 1}>
              <summary>{translate(locale, "energyDailyValues")}</summary>
            <div className="energy-history-list">
              {[...points].sort((a, b) => b.date.localeCompare(a.date)).slice(0, 14).map((point) => (
                <div key={point.date} className="energy-history-list__row">
                  <span>{new Intl.DateTimeFormat(locale, { day: "numeric", month: "short" }).format(new Date(`${point.date}T12:00:00`))}</span>
                  <meter min={Math.min(-50, ...points.map((p) => p.current_eod))} max={Math.max(100, ...points.map((p) => p.current_eod))} value={point.current_eod} aria-label={point.date} />
                  <strong>{point.current_eod}</strong>
                </div>
              ))}
            </div>
            </details>
          </section>
        ) : <section className="health-detail-section surface"><h2>{translate(locale, "energyTrend")}</h2><p className="empty-note">{translate(locale, historyFailed ? "errorDetail" : history ? "historyAccruing" : "loadingDetail")}</p></section>}
        <section className="health-detail-section surface energy-insights">
          {domain ? (
            <InsightPair
              locale={locale}
              observation={domain.insight.observation}
              meaning={domain.insight.meaning}
              action={domain.insight.next_step?.text}
              ai={domain.ai_insight}
              state={state.status === "ready" ? state.insights.generation.slots?.find((slot) => slot.key === "energy")?.state : undefined}
              preview={state.status === "ready" && state.insights.generation.narrative_mode === "preview"}
            />
          ) : state.status === "loading" ? (
            <StatusPanel state="loading" title={translate(locale, "loadingTitle")} detail={translate(locale, "loadingDetail")} />
          ) : state.status === "unauthenticated" ? (
            <a className="primary-action" href={`/login?next=${encodeURIComponent(`/energy?lang=${locale}`)}`}>{translate(locale, "signIn")}</a>
          ) : state.status === "error" ? (
            <StatusPanel state="error" title={translate(locale, "errorTitle")} detail={state.message} />
          ) : <p className="empty-note">{translate(locale, "historyAccruing")}</p>}
        </section>
      </main>
    </div>
  );
}
