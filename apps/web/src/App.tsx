import { useEffect, useMemo, useRef, useState } from "react";

import { ClientApiError } from "./api/client";
import { AppHeader } from "./components/AppHeader";
import { StatusPanel } from "./components/StatusPanel";
import { fixtureNames, fixtureResources, resolveFixture } from "./features/dashboard/fixtures";
import { DashboardDetails } from "./features/dashboard/DashboardDetails";
import { DashboardHero } from "./features/dashboard/DashboardHero";
import { shouldPollAI } from "./features/dashboard/aiPolling";
import { loadDashboardResources, type DashboardResources } from "./features/dashboard/loader";
import { buildDashboardViewModel } from "./features/dashboard/model";
import { ScoreSummaryCard } from "./features/dashboard/ScoreSummaryCard";
import { SleepPage } from "./features/sleep/SleepPage";
import { resolveLocale, translate, type Locale } from "./i18n";

type AppState =
  | { status: "loading" }
  | { status: "ready"; resources: DashboardResources }
  | { status: "unauthenticated" }
  | { status: "error"; message: string };

function fixtureHref(locale: Locale, fixture: string): string {
  return `/?${new URLSearchParams({ lang: locale, fixture }).toString()}`;
}

function DashboardApp() {
  const params = new URLSearchParams(window.location.search);
  const locale = resolveLocale(params.get("lang"));
  const fixtureEnabled = import.meta.env.VITE_ENABLE_FIXTURES === "true";
  const fixture = fixtureEnabled ? resolveFixture(params.get("fixture")) : undefined;
  const [reloadKey, setReloadKey] = useState(0);
  const [liveState, setLiveState] = useState<AppState>({ status: "loading" });
  const aiPollAttempts = useRef(0);
  const fixtureState: AppState | undefined = fixture
    ? fixture === "loading"
      ? { status: "loading" }
      : fixture === "error"
        ? { status: "error", message: translate(locale, "errorDetail") }
        : { status: "ready", resources: fixtureResources(locale, fixture) }
    : undefined;
  const state = fixtureState ?? liveState;

  useEffect(() => {
    document.documentElement.lang = locale;
    aiPollAttempts.current = 0;
  }, [locale]);

  useEffect(() => {
    if (fixture) {
      return;
    }
    const controller = new AbortController();
    loadDashboardResources(locale, controller.signal)
      .then((resources) => setLiveState({ status: "ready", resources }))
      .catch((error: unknown) => {
        if (controller.signal.aborted) {
          return;
        }
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
  }, [fixture, locale, reloadKey]);

  useEffect(() => {
    if (
      state.status !== "ready" ||
      !shouldPollAI(state.resources.ai, aiPollAttempts.current) ||
      fixture
    ) {
      return;
    }
    let timer: number | undefined;
    const schedule = () => {
      if (timer !== undefined) {
        window.clearTimeout(timer);
      }
      timer =
        document.visibilityState === "visible"
          ? window.setTimeout(() => {
              aiPollAttempts.current += 1;
              setReloadKey((value) => value + 1);
            }, 60_000)
          : undefined;
    };
    schedule();
    document.addEventListener("visibilitychange", schedule);
    return () => {
      document.removeEventListener("visibilitychange", schedule);
      if (timer !== undefined) {
        window.clearTimeout(timer);
      }
    };
  }, [fixture, state]);

  const model = useMemo(
    () =>
      state.status === "ready"
        ? buildDashboardViewModel(state.resources.briefing, state.resources.missing)
        : undefined,
    [state],
  );

  return (
    <div className="app-shell" data-locale={locale} data-fixture={fixture}>
      <AppHeader
        locale={locale}
        isAdmin={state.status === "ready" && state.resources.session?.is_admin}
      />
      <main className="page">
        {fixtureEnabled ? (
          <nav className="fixture-switcher" aria-label={translate(locale, "fixtureNav")}>
            {fixtureNames.map((candidate) => (
              <a
                key={candidate}
                href={fixtureHref(locale, candidate)}
                aria-current={candidate === fixture ? "page" : undefined}
              >
                {candidate}
              </a>
            ))}
          </nav>
        ) : null}

        {state.status === "loading" ? (
          <div className="standalone-status">
            <StatusPanel
              state="loading"
              title={translate(locale, "loadingTitle")}
              detail={translate(locale, "loadingDetail")}
            />
          </div>
        ) : state.status === "unauthenticated" ? (
          <div className="standalone-status">
            <StatusPanel
              state="error"
              title={translate(locale, "signInTitle")}
              detail={translate(locale, "signInDetail")}
            />
            <a
              className="primary-action"
              href={`/login?next=${encodeURIComponent(`/?lang=${locale}`)}`}
            >
              {translate(locale, "signIn")}
            </a>
          </div>
        ) : state.status === "error" ? (
          <div className="standalone-status">
            <StatusPanel
              state="error"
              title={translate(locale, "errorTitle")}
              detail={state.message}
            />
            <button
              className="primary-action"
              onClick={() => {
                setLiveState({ status: "loading" });
                setReloadKey((value) => value + 1);
              }}
            >
              {translate(locale, "retry")}
            </button>
          </div>
        ) : model ? (
          <>
            <DashboardHero locale={locale} model={model} />
            {model.energy || model.sleep ? (
              <section
                className="supporting-scores"
                aria-label={translate(locale, "supportingScores")}
              >
                {model.energy ? (
                  <ScoreSummaryCard
                    score={model.energy}
                    fallbackLabel={translate(locale, "energy")}
                  />
                ) : null}
                {model.sleep ? (
                  <ScoreSummaryCard
                    score={model.sleep}
                    fallbackLabel={translate(locale, "sleep")}
                    href={`/sleep?lang=${locale}`}
                    displayStatus={
                      model.sleep.status === "final"
                        ? translate(locale, "statusFinal")
                        : translate(locale, "statusPartial")
                    }
                  />
                ) : null}
              </section>
            ) : null}
            <DashboardDetails
              locale={locale}
              model={model}
              ai={state.resources.ai}
              readinessHistory={state.resources.readinessHistory}
              energyHistory={state.resources.energyHistory}
            />
          </>
        ) : null}
      </main>
    </div>
  );
}

export function App() {
  return window.location.pathname === "/sleep" ? <SleepPage /> : <DashboardApp />;
}
