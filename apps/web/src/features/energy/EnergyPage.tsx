import { useEffect, useRef, useState } from "react";

import {
  ClientApiError,
  getEnergyHistory,
  getSession,
  getTodayInsights,
  type EnergyHistoryDayResponse,
  type SessionResponse,
  type TodayInsightsResponse,
} from "../../api/client";
import { clearSessionRecoveryAttempt, recoverSessionOnUnauthorized } from "../../auth/sessionRecovery";
import { AppHeader } from "../../components/AppHeader";
import { InsightPair } from "../../components/InsightPair";
import { StatusPanel } from "../../components/StatusPanel";
import { fixtureResources } from "../dashboard/fixtures";
import { isTodayInsightsGenerationPending, todayInsightsPollDelayMs } from "../dashboard/aiPolling";
import { resolveLocale, translate } from "../../i18n";

type EnergyState =
  | { status: "loading" }
  | { status: "ready"; insights: TodayInsightsResponse; history?: EnergyHistoryDayResponse; session?: SessionResponse }
  | { status: "error"; message: string }
  | { status: "unauthenticated" };

export function EnergyPage() {
  const params = new URLSearchParams(window.location.search);
  const locale = resolveLocale(params.get("lang"));
  const fixture = import.meta.env.VITE_ENABLE_FIXTURES === "true" && params.get("fixture") === "normal";
  const fixtureInsights = fixture ? fixtureResources(locale, "normal").todayInsights : undefined;
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
          history: current.status === "ready" ? current.history : undefined,
          session: current.status === "ready" ? current.session : undefined }));
        void getEnergyHistory(14, controller.signal).then((history) => {
          if (!controller.signal.aborted) setState((current) => current.status === "ready" ? { ...current, history } : current);
        }).catch(() => undefined);
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
  const history = state.status === "ready" ? state.history?.points : undefined;
  return (
    <div className="app-shell health-detail-shell energy-shell">
      <AppHeader locale={locale} isAdmin={state.status === "ready" && state.session?.is_admin} />
      <main className="health-detail-page energy-page" data-section="energy">
        <section className="health-detail-hero">
          <div className="health-detail-hero__heading">
            <a className="health-detail-back" href={`/?lang=${locale}`} aria-label={translate(locale, "healthDetailBack")}>←</a>
            <div><p>{translate(locale, "today")}</p><h1>{translate(locale, "energyDetailTitle")}</h1></div>
          </div>
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
        {history?.length ? (
          <section className="health-detail-section surface">
            <div className="health-detail-section__header"><h2>{translate(locale, "energyTrend")}</h2></div>
            <div className="energy-history-list">
              {history.slice(-14).reverse().map((point) => (
                <div key={point.date} className="energy-history-list__row">
                  <span>{new Intl.DateTimeFormat(locale, { day: "numeric", month: "short" }).format(new Date(`${point.date}T12:00:00`))}</span>
                  <meter min={-50} max={100} value={point.current_eod} aria-label={point.date} />
                  <strong>{point.current_eod}</strong>
                </div>
              ))}
            </div>
          </section>
        ) : null}
      </main>
    </div>
  );
}
