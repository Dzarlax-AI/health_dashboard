import { useEffect, useMemo, useState } from "react";
import { Search } from "lucide-react";

import { ClientApiError, getSession, type SessionResponse } from "../../api/client";
import {
  getLatestMetricValues,
  listMetrics,
  type LatestMetricValue,
  type MetricCatalogItem,
} from "../../api/operations";
import {
  clearSessionRecoveryAttempt,
  recoverSessionOnUnauthorized,
} from "../../auth/sessionRecovery";
import { AppHeader } from "../../components/AppHeader";
import { StatusPanel } from "../../components/StatusPanel";
import { resolveLocale, translate } from "../../i18n";
import { tp } from "../../i18n/pages";
import { groupMetricsCatalog } from "./catalog";

type LoadState =
  | { status: "loading" }
  | {
      status: "ready";
      catalog: MetricCatalogItem[];
      latest: LatestMetricValue[];
      session?: SessionResponse;
    }
  | { status: "unauthenticated" }
  | { status: "error"; message: string };

export function MetricsPage() {
  const params = new URLSearchParams(window.location.search);
  const locale = resolveLocale(params.get("lang"));
  const [query, setQuery] = useState(() => params.get("q") ?? "");
  const [reloadKey, setReloadKey] = useState(0);
  const [state, setState] = useState<LoadState>({ status: "loading" });

  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);

  useEffect(() => {
    const controller = new AbortController();
    Promise.all([
      listMetrics(locale, controller.signal),
      getLatestMetricValues(controller.signal).catch(() => [] as LatestMetricValue[]),
      getSession(controller.signal).catch(() => undefined),
    ])
      .then(([catalog, latest, session]) => {
        if (controller.signal.aborted) return;
        clearSessionRecoveryAttempt(window.sessionStorage);
        setState({ status: "ready", catalog, latest, session });
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        if (
          recoverSessionOnUnauthorized(
            error,
            window.location,
            window.sessionStorage,
            (target) => window.location.assign(target),
          )
        ) {
          return;
        }
        if (error instanceof ClientApiError && error.status === 401) {
          setState({ status: "unauthenticated" });
          return;
        }
        setState({
          status: "error",
          message:
            error instanceof Error
              ? error.message
              : tp(locale, "metricsCatalogLoadError"),
        });
      });

    return () => controller.abort();
  }, [locale, reloadKey]);

  const categories = useMemo(() => {
    if (state.status !== "ready") return [];
    return groupMetricsCatalog(state.catalog, state.latest, query, locale);
  }, [locale, query, state]);

  const handleQueryChange = (next: string) => {
    setQuery(next);
    const nextParams = new URLSearchParams(window.location.search);
    if (next.trim()) {
      nextParams.set("q", next);
    } else {
      nextParams.delete("q");
    }
    const qs = nextParams.toString();
    window.history.replaceState(
      {},
      "",
      `${window.location.pathname}${qs ? `?${qs}` : ""}`,
    );
  };

  return (
    <div className="app-shell" data-locale={locale}>
      <AppHeader
        locale={locale}
        isAdmin={state.status === "ready" && Boolean(state.session?.is_admin)}
      />
      <main className="subpage">
        <header className="subpage__header">
          <div className="subpage__heading">
            <a className="subpage__back" href={`/?lang=${locale}`}>
              ← {tp(locale, "backToToday")}
            </a>
            <h1>{tp(locale, "metricsTitle")}</h1>
          </div>
          <label className="metrics-search">
            <Search size={15} aria-hidden="true" />
            <input
              type="search"
              value={query}
              onChange={(event) => handleQueryChange(event.target.value)}
              placeholder={tp(locale, "metricsSearchPlaceholder")}
              aria-label={tp(locale, "metricsSearchPlaceholder")}
              autoComplete="off"
            />
          </label>
        </header>

        {state.status === "loading" ? (
          <StatusPanel
            state="loading"
            title={tp(locale, "loading")}
            detail={translate(locale, "loadingDetail")}
          />
        ) : state.status === "unauthenticated" ? (
          <div className="standalone-status">
            <StatusPanel
              state="error"
              title={translate(locale, "signInTitle")}
              detail={translate(locale, "signInDetail")}
            />
            <a
              className="primary-action"
              href={`/login?next=${encodeURIComponent(`/metrics?lang=${locale}`)}`}
            >
              {translate(locale, "signIn")}
            </a>
          </div>
        ) : state.status === "error" ? (
          <div className="standalone-status">
            <StatusPanel
              state="error"
              title={tp(locale, "metricsCatalogLoadError")}
              detail={state.message}
            />
            <button
              type="button"
              className="primary-action"
              onClick={() => {
                setState({ status: "loading" });
                setReloadKey((n) => n + 1);
              }}
            >
              {tp(locale, "retry")}
            </button>
          </div>
        ) : categories.length === 0 ? (
          <section className="panel">
            <p className="subpage__muted">
              {state.catalog.length === 0
                ? tp(locale, "metricsCatalogEmpty")
                : tp(locale, "metricsEmpty")}
            </p>
          </section>
        ) : (
          categories.map((category) => (
            <section
              key={category.key}
              className="metrics-category"
              aria-label={category.label}
            >
              <div className="metrics-category__label">
                <span
                  className="metrics-category__dot"
                  style={{ background: category.color }}
                />
                {category.label}
              </div>
              <div className="metrics-grid">
                {category.items.map((metric) => (
                  <a
                    key={metric.name}
                    href={`/metrics/${encodeURIComponent(metric.name)}?lang=${locale}`}
                    className="metric-tile"
                  >
                    <span className="metric-tile__name">{metric.displayName}</span>
                    <span className="metric-tile__bottom">
                      {metric.value !== undefined ? (
                        <>
                          <strong className="metric-tile__value">
                            {metric.value}
                          </strong>
                          {metric.unit ? (
                            <span className="metric-tile__unit">
                              {metric.unit}
                            </span>
                          ) : null}
                        </>
                      ) : (
                        <span className="subpage__muted">—</span>
                      )}
                    </span>
                  </a>
                ))}
              </div>
            </section>
          ))
        )}
      </main>
    </div>
  );
}
