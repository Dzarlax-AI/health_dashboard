import { useCallback, useEffect, useState } from "react";

import {
  answerInboxPrompt,
  ClientApiError,
  getInbox,
  getInboxItem,
  getPendingInboxPrompts,
  getSession,
  type InboxDetailResponse,
  type InboxItemSummary,
  type PendingCheckinPrompt,
  type SessionResponse,
} from "../../api/client";
import {
  clearSessionRecoveryAttempt,
  recoverSessionOnUnauthorized,
} from "../../auth/sessionRecovery";
import { AppHeader } from "../../components/AppHeader";
import { StatusPanel } from "../../components/StatusPanel";
import { resolveLocale, translate, type Locale } from "../../i18n";
import { PendingCheckinCard } from "./PendingCheckinCard";

interface InboxPageProps {
  itemId?: string;
}

type FeedState =
  | { status: "loading" }
  | { status: "unauthenticated" }
  | { status: "unavailable" }
  | { status: "error"; message: string }
  | {
      status: "ready";
      items: InboxItemSummary[];
      nextCursor?: string;
      prompts: PendingCheckinPrompt[];
      session?: SessionResponse;
    };

type DetailState =
  | { status: "loading" }
  | { status: "unauthenticated" }
  | { status: "not_found" }
  | { status: "error"; message: string }
  | {
      status: "ready";
      detail: InboxDetailResponse;
      session?: SessionResponse;
    };

function statusLabelForCheckin(locale: Locale, status?: string): string {
  switch (status) {
    case "answered":
      return translate(locale, "inboxPromptStatusAnswered");
    case "late_answered":
      return translate(locale, "inboxPromptStatusLate");
    case "expired":
      return translate(locale, "inboxPromptStatusExpired");
    case "prompted":
      return translate(locale, "inboxPromptStatusPending");
    default:
      return status ?? "";
  }
}

export function InboxPage({ itemId }: InboxPageProps) {
  const params = new URLSearchParams(window.location.search);
  const locale = resolveLocale(params.get("lang"));
  const [reloadKey, setReloadKey] = useState(0);
  const [feedState, setFeedState] = useState<FeedState>({ status: "loading" });
  const [detailState, setDetailState] = useState<DetailState>({ status: "loading" });
  const [loadingMore, setLoadingMore] = useState(false);
  const [detailSubmitting, setDetailSubmitting] = useState(false);
  const [detailError, setDetailError] = useState<string | undefined>(undefined);

  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);

  useEffect(() => {
    if (itemId) {
      return;
    }
    const controller = new AbortController();
    Promise.all([
      getInbox(locale, { limit: 20 }, controller.signal),
      getPendingInboxPrompts(locale, controller.signal).catch((err: unknown) => {
        if (err instanceof ClientApiError && err.status === 404) {
          return { prompts: [] };
        }
        throw err;
      }),
      getSession(controller.signal).catch(() => undefined),
    ])
      .then(([feed, pending, session]) => {
        clearSessionRecoveryAttempt(window.sessionStorage);
        setFeedState({
          status: "ready",
          items: feed.items ?? [],
          nextCursor: feed.next_cursor || undefined,
          prompts: pending.prompts ?? [],
          session,
        });
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) {
          return;
        }
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
          setFeedState({ status: "unauthenticated" });
          return;
        }
        if (error instanceof ClientApiError && error.status === 404) {
          setFeedState({ status: "unavailable" });
          return;
        }
        setFeedState({
          status: "error",
          message: error instanceof Error ? error.message : translate(locale, "errorDetail"),
        });
      });
    return () => controller.abort();
  }, [itemId, locale, reloadKey]);

  useEffect(() => {
    if (!itemId) {
      return;
    }
    const controller = new AbortController();
    Promise.all([
      getInboxItem(itemId, locale, controller.signal),
      getSession(controller.signal).catch(() => undefined),
    ])
      .then(([detail, session]) => {
        clearSessionRecoveryAttempt(window.sessionStorage);
        setDetailState({ status: "ready", detail, session });
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) {
          return;
        }
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
          setDetailState({ status: "unauthenticated" });
          return;
        }
        if (error instanceof ClientApiError && error.status === 404) {
          setDetailState({ status: "not_found" });
          return;
        }
        setDetailState({
          status: "error",
          message: error instanceof Error ? error.message : translate(locale, "errorDetail"),
        });
      });
    return () => controller.abort();
  }, [itemId, locale, reloadKey]);

  const handleLoadMore = useCallback(async () => {
    if (feedState.status !== "ready" || !feedState.nextCursor || loadingMore) {
      return;
    }
    setLoadingMore(true);
    try {
      const more = await getInbox(locale, { limit: 20, cursor: feedState.nextCursor });
      setFeedState((prev) =>
        prev.status === "ready"
          ? {
              ...prev,
              items: [...prev.items, ...(more.items ?? [])],
              nextCursor: more.next_cursor || undefined,
            }
          : prev,
      );
    } catch {
      // Keep existing feed visible so user can retry loading more.
    } finally {
      setLoadingMore(false);
    }
  }, [feedState, loadingMore, locale]);

  const handlePromptResolved = useCallback(() => {
    setReloadKey((k) => k + 1);
  }, []);

  const handleDetailAnswer = useCallback(
    async (promptId: string, answer: "great" | "ok" | "meh" | "sick") => {
      if (detailSubmitting) {
        return;
      }
      setDetailSubmitting(true);
      setDetailError(undefined);
      try {
        await answerInboxPrompt(promptId, { answer, channel: "web" });
        setReloadKey((k) => k + 1);
      } catch (error: unknown) {
        if (error instanceof ClientApiError && error.status === 409) {
          setDetailError(translate(locale, "inboxPromptExpired"));
          setReloadKey((k) => k + 1);
        } else {
          setDetailError(translate(locale, "inboxPromptSaveError"));
        }
      } finally {
        setDetailSubmitting(false);
      }
    },
    [detailSubmitting, locale],
  );

  const isAdmin = itemId
    ? detailState.status === "ready" && Boolean(detailState.session?.is_admin)
    : feedState.status === "ready" && Boolean(feedState.session?.is_admin);

  return (
    <div className="app-shell" data-locale={locale}>
      <AppHeader locale={locale} isAdmin={isAdmin} showInbox />
      <main className="page">
        <div className="inbox-page">
          <div className="inbox-page__nav">
            <a
              className="inbox-back-link"
              href={itemId ? `/inbox?lang=${locale}` : `/?lang=${locale}`}
            >
              ← {translate(locale, itemId ? "inboxBackToFeed" : "inboxBackToToday")}
            </a>
          </div>

          {itemId ? (
            detailState.status === "loading" ? (
              <StatusPanel
                state="loading"
                title={translate(locale, "inboxLoading")}
                detail={translate(locale, "loadingDetail")}
              />
            ) : detailState.status === "unauthenticated" ? (
              <div className="standalone-status">
                <StatusPanel
                  state="error"
                  title={translate(locale, "signInTitle")}
                  detail={translate(locale, "signInDetail")}
                />
                <a
                  className="primary-action"
                  href={`/login?next=${encodeURIComponent(`/inbox/${itemId}?lang=${locale}`)}`}
                >
                  {translate(locale, "signIn")}
                </a>
              </div>
            ) : detailState.status === "not_found" ? (
              <StatusPanel
                state="error"
                title={translate(locale, "inboxTitle")}
                detail={translate(locale, "inboxNotFound")}
              />
            ) : detailState.status === "error" ? (
              <div className="standalone-status">
                <StatusPanel
                  state="error"
                  title={translate(locale, "errorTitle")}
                  detail={detailState.message}
                />
                <button
                  type="button"
                  className="primary-action"
                  onClick={() => setReloadKey((k) => k + 1)}
                >
                  {translate(locale, "retry")}
                </button>
              </div>
            ) : (
              <article className="inbox-detail-card">
                <header className="inbox-detail-card__header">
                  <h1 className="inbox-detail-card__title">{detailState.detail.title}</h1>
                  <span className="inbox-detail-card__date">{detailState.detail.local_date}</span>
                </header>
                {detailState.detail.kind === "morning_report" ? (
                  <div className="inbox-report-body">{detailState.detail.body ?? ""}</div>
                ) : (
                  <div className="inbox-checkin-detail">
                    <p className="inbox-checkin-detail__status">
                      {statusLabelForCheckin(locale, detailState.detail.status)}
                      {detailState.detail.answer ? `: ${detailState.detail.answer}` : ""}
                    </p>
                    {detailState.detail.status === "prompted" &&
                    detailState.detail.prompt_id &&
                    detailState.detail.options &&
                    detailState.detail.options.length > 0 ? (
                      <div className="inbox-prompt-options">
                        {detailState.detail.options.map((opt) => (
                          <button
                            key={opt.id}
                            type="button"
                            className="inbox-prompt-option"
                            disabled={detailSubmitting}
                            onClick={() =>
                              void handleDetailAnswer(
                                detailState.detail.prompt_id!,
                                opt.id as "great" | "ok" | "meh" | "sick",
                              )
                            }
                          >
                            {opt.label}
                          </button>
                        ))}
                      </div>
                    ) : null}
                    {detailError ? (
                      <p className="inbox-prompt-card__error" role="alert">
                        {detailError}
                      </p>
                    ) : null}
                  </div>
                )}
              </article>
            )
          ) : feedState.status === "loading" ? (
            <StatusPanel
              state="loading"
              title={translate(locale, "inboxLoading")}
              detail={translate(locale, "loadingDetail")}
            />
          ) : feedState.status === "unauthenticated" ? (
            <div className="standalone-status">
              <StatusPanel
                state="error"
                title={translate(locale, "signInTitle")}
                detail={translate(locale, "signInDetail")}
              />
              <a
                className="primary-action"
                href={`/login?next=${encodeURIComponent(`/inbox?lang=${locale}`)}`}
              >
                {translate(locale, "signIn")}
              </a>
            </div>
          ) : feedState.status === "unavailable" ? (
            <StatusPanel
              state="error"
              title={translate(locale, "inboxTitle")}
              detail={translate(locale, "inboxUnavailable")}
            />
          ) : feedState.status === "error" ? (
            <div className="standalone-status">
              <StatusPanel
                state="error"
                title={translate(locale, "errorTitle")}
                detail={feedState.message}
              />
              <button
                type="button"
                className="primary-action"
                onClick={() => setReloadKey((k) => k + 1)}
              >
                {translate(locale, "retry")}
              </button>
            </div>
          ) : (
            <>
              <h1 className="inbox-page__title">{translate(locale, "inboxTitle")}</h1>
              <PendingCheckinCard
                locale={locale}
                prompts={feedState.prompts}
                onAnswered={handlePromptResolved}
                onExpired={handlePromptResolved}
              />
              {feedState.items.length === 0 ? (
                <p className="inbox-page__empty">{translate(locale, "inboxEmpty")}</p>
              ) : (
                <ul className="inbox-list">
                  {feedState.items.map((item) => (
                    <li key={item.id} className="inbox-list__row">
                      <a
                        className="inbox-item"
                        href={`/inbox/${encodeURIComponent(item.id)}?lang=${locale}`}
                      >
                        <div className="inbox-item__top">
                          <span className="inbox-item__title">{item.title}</span>
                          <span className="inbox-item__date">{item.local_date}</span>
                        </div>
                        {item.kind === "morning_report" && item.preview ? (
                          <p className="inbox-item__preview">{item.preview}</p>
                        ) : item.kind === "prompt_checkin" ? (
                          <p className="inbox-item__meta">
                            {statusLabelForCheckin(locale, item.status)}
                            {item.answer ? ` · ${item.answer}` : ""}
                          </p>
                        ) : null}
                      </a>
                    </li>
                  ))}
                </ul>
              )}
              {feedState.nextCursor ? (
                <div className="inbox-page__more">
                  <button
                    type="button"
                    className="primary-action"
                    disabled={loadingMore}
                    onClick={() => void handleLoadMore()}
                  >
                    {translate(locale, "inboxLoadMore")}
                  </button>
                </div>
              ) : null}
            </>
          )}
        </div>
      </main>
    </div>
  );
}
