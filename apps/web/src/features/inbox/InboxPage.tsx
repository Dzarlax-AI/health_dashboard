import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import {
  answerInboxPrompt,
  ClientApiError,
  getInbox,
  getInboxItem,
  getPendingInboxPrompts,
  getSession,
  markAllInboxRead,
  markInboxItemRead,
  type InboxDetailResponse,
  type InboxItemSummary,
  type PendingCheckinPrompt,
  type PromptAnswerRequest,
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

type InboxFilter = "all" | "unread";
type FeedRequest = { itemId?: string; locale: Locale; filter: InboxFilter; reloadKey: number };

type FeedState =
  | { status: "loading" }
  | { status: "unauthenticated" }
  | { status: "unavailable" }
  | { status: "error"; message: string }
  | {
      status: "ready";
      request: FeedRequest;
      items: InboxItemSummary[];
      nextCursor?: string;
      snapshotAt?: string;
      unreadCount: number;
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
      unreadCount: number;
      session?: SessionResponse;
    };

function isPromptKind(kind: InboxItemSummary["kind"]): boolean {
  return (
    kind === "prompt_checkin" ||
    kind === "prompt_wake_feedback" ||
    kind === "prompt_context"
  );
}

function kindLabel(locale: Locale, kind: InboxItemSummary["kind"]): string {
  switch (kind) {
    case "morning_report":
      return translate(locale, "inboxKindMorningReport");
    case "evening_report":
      return translate(locale, "inboxKindEveningReport");
    case "sleep_update":
      return translate(locale, "inboxKindSleepUpdate");
    case "guidance_change":
      return translate(locale, "inboxKindGuidanceChange");
    case "weekly_digest":
      return translate(locale, "inboxKindWeeklyDigest");
    case "nudge":
      return translate(locale, "inboxKindNudge");
    case "prompt_checkin":
      return translate(locale, "inboxKindCheckin");
    case "prompt_wake_feedback":
      return translate(locale, "inboxKindWakeFeedback");
    case "prompt_context":
      return translate(locale, "inboxKindContext");
    default:
      return kind;
  }
}

function statusLabelForPrompt(locale: Locale, status?: string): string {
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

function answerLabelForPrompt(locale: Locale, answer?: string): string {
  switch (answer) {
    case "great":
      return translate(locale, "inboxAnswerGreat");
    case "ok":
      return translate(locale, "inboxAnswerOk");
    case "meh":
      return translate(locale, "inboxAnswerMeh");
    case "sick":
      return translate(locale, "inboxAnswerSick");
    case "confirmed":
      return translate(locale, "inboxAnswerConfirmed");
    case "earlier":
      return translate(locale, "inboxAnswerEarlier");
    case "later":
      return translate(locale, "inboxAnswerLater");
    case "returned_to_sleep":
      return translate(locale, "inboxAnswerReturnedToSleep");
    case "poor_sleep_context":
      return translate(locale, "inboxAnswerPoorSleep");
    case "stress_context":
      return translate(locale, "inboxAnswerStress");
    case "travel_context":
      return translate(locale, "inboxAnswerTravel");
    case "unknown_context":
      return translate(locale, "inboxAnswerUnknown");
    case "skip_context":
      return translate(locale, "inboxAnswerSkip");
    default:
      return answer ?? "";
  }
}

function safeActionHref(route: string | undefined, locale: Locale): string | undefined {
  if (!route || !route.startsWith("/") || route.startsWith("//")) {
    return undefined;
  }
  const sep = route.includes("?") ? "&" : "?";
  return `${route}${sep}lang=${locale}`;
}

export function InboxPage({ itemId }: InboxPageProps) {
  const params = new URLSearchParams(window.location.search);
  const locale = resolveLocale(params.get("lang"));
  const [filter, setFilter] = useState<InboxFilter>("all");
  const [reloadKey, setReloadKey] = useState(0);
  const feedRequest = useMemo(
    () => ({ itemId, locale, filter, reloadKey }),
    [itemId, locale, filter, reloadKey],
  );
  const [feedState, setFeedState] = useState<FeedState>({ status: "loading" });
  const [detailState, setDetailState] = useState<DetailState>({ status: "loading" });
  const [loadingMore, setLoadingMore] = useState(false);
  const [markingAllRead, setMarkingAllRead] = useState(false);
  const [detailSubmitting, setDetailSubmitting] = useState(false);
  const [detailError, setDetailError] = useState<string | undefined>(undefined);
  const feedGenerationRef = useRef(0);
  const loadMoreAbortRef = useRef<AbortController | null>(null);
  const markedReadIdsRef = useRef<Set<string>>(new Set());
  const canLoadMore =
    feedState.status === "ready" && feedState.request === feedRequest && !loadingMore;

  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);

  useEffect(() => {
    const { itemId, locale, filter } = feedRequest;
    if (itemId) {
      return;
    }
    feedGenerationRef.current += 1;
    loadMoreAbortRef.current?.abort();
    const controller = new AbortController();
    const query = filter === "unread" ? { limit: 20, filter: "unread" as const } : { limit: 20 };
    Promise.all([
      getInbox(locale, query, controller.signal),
      getPendingInboxPrompts(locale, controller.signal).catch((err: unknown) => {
        if (err instanceof ClientApiError && err.status === 404) {
          return { prompts: [], unread_count: 0 };
        }
        throw err;
      }),
      getSession(controller.signal).catch(() => undefined),
    ])
      .then(([feed, pending, session]) => {
        if (controller.signal.aborted) {
          return;
        }
        clearSessionRecoveryAttempt(window.sessionStorage);
        setFeedState({
          status: "ready",
          request: feedRequest,
          items: feed.items ?? [],
          nextCursor: feed.next_cursor || undefined,
          snapshotAt: feed.snapshot_at || undefined,
          unreadCount: feed.unread_count ?? pending.unread_count ?? 0,
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
  }, [feedRequest]);

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
        if (controller.signal.aborted) {
          return;
        }
        clearSessionRecoveryAttempt(window.sessionStorage);
        setDetailState({ status: "ready", detail, unreadCount: 0, session });
        if (!detail.read_at && !markedReadIdsRef.current.has(detail.id)) {
          markedReadIdsRef.current.add(detail.id);
          void markInboxItemRead(detail.id, controller.signal)
            .then((res) => {
              if (controller.signal.aborted) {
                return;
              }
              setDetailState((prev) =>
                prev.status === "ready" && prev.detail.id === detail.id
                  ? {
                      ...prev,
                      detail: { ...prev.detail, read_at: res.read_at },
                      unreadCount: res.unread_count,
                    }
                  : prev,
              );
            })
            .catch(() => {
              markedReadIdsRef.current.delete(detail.id);
            });
        }
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
    if (!canLoadMore || !feedState.nextCursor) {
      return;
    }
    const generation = feedGenerationRef.current;
    const controller = new AbortController();
    loadMoreAbortRef.current = controller;
    setLoadingMore(true);
    try {
      const query =
        filter === "unread"
          ? { limit: 20, cursor: feedState.nextCursor, filter: "unread" as const }
          : { limit: 20, cursor: feedState.nextCursor };
      const more = await getInbox(locale, query, controller.signal);
      if (feedGenerationRef.current !== generation || controller.signal.aborted) {
        return;
      }
      setFeedState((prev) =>
        prev.status === "ready" && prev.request === feedRequest
          ? {
              ...prev,
              items: [...prev.items, ...(more.items ?? [])],
              nextCursor: more.next_cursor || undefined,
              unreadCount: more.unread_count ?? prev.unreadCount,
            }
          : prev,
      );
    } catch {
      // Keep existing feed visible so user can retry loading more.
    } finally {
      if (loadMoreAbortRef.current === controller) {
        loadMoreAbortRef.current = null;
        setLoadingMore(false);
      }
    }
  }, [canLoadMore, feedRequest, feedState, filter, locale]);

  const handleMarkAllRead = useCallback(async () => {
    if (feedState.status !== "ready" || markingAllRead) {
      return;
    }
    setMarkingAllRead(true);
    try {
      const res = await markAllInboxRead(
        feedState.snapshotAt ? { before: feedState.snapshotAt } : {},
      );
      setFeedState((prev) => {
        if (prev.status !== "ready") {
          return prev;
        }
        const cutoffMs = Date.parse(res.before);
        const updatedItems = prev.items
          .map((item) => {
            const itemMs = Date.parse(item.created_at);
            if (!item.read_at && (Number.isNaN(cutoffMs) || Number.isNaN(itemMs) || itemMs <= cutoffMs)) {
              return { ...item, read_at: res.before };
            }
            return item;
          })
          .filter((item) => (filter === "unread" ? !item.read_at : true));
        return {
          ...prev,
          items: updatedItems,
          unreadCount: res.unread_count,
        };
      });
    } catch {
      // Keep current state so user can retry.
    } finally {
      setMarkingAllRead(false);
    }
  }, [feedState, filter, markingAllRead]);

  const handlePromptResolved = useCallback(() => {
    setReloadKey((k) => k + 1);
  }, []);

  const handleDetailAnswer = useCallback(
    async (promptId: string, answer: PromptAnswerRequest["answer"]) => {
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
  const unreadCount = itemId
    ? detailState.status === "ready"
      ? detailState.unreadCount
      : 0
    : feedState.status === "ready"
      ? feedState.unreadCount
      : 0;

  return (
    <div className="app-shell" data-locale={locale}>
      <AppHeader locale={locale} isAdmin={isAdmin} showInbox inboxUnreadCount={unreadCount} />
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
                  <div>
                    <span className="inbox-item__kind">
                      {kindLabel(locale, detailState.detail.kind)}
                    </span>
                    <h1 className="inbox-detail-card__title">{detailState.detail.title}</h1>
                  </div>
                  <span className="inbox-detail-card__date">{detailState.detail.local_date}</span>
                </header>
                {!isPromptKind(detailState.detail.kind) ? (
                  <>
                    <div className="inbox-report-body">{detailState.detail.body ?? ""}</div>
                    {safeActionHref(detailState.detail.action_route, locale) ? (
                      <div className="inbox-detail-card__actions">
                        <a
                          className="primary-action"
                          href={safeActionHref(detailState.detail.action_route, locale)}
                        >
                          {translate(locale, "inboxOpenAction")}
                        </a>
                      </div>
                    ) : null}
                  </>
                ) : (
                  <div className="inbox-checkin-detail">
                    {detailState.detail.body ? (
                      <div className="inbox-report-body">{detailState.detail.body}</div>
                    ) : null}
                    <p className="inbox-checkin-detail__status">
                      {statusLabelForPrompt(locale, detailState.detail.status)}
                      {detailState.detail.answer
                        ? `: ${answerLabelForPrompt(locale, detailState.detail.answer)}`
                        : ""}
                    </p>
                    {(detailState.detail.status === "prompted" ||
                      detailState.detail.status === "expired") &&
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
                                opt.id as PromptAnswerRequest["answer"],
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
              <div className="inbox-page__header">
                <h1 className="inbox-page__title">{translate(locale, "inboxTitle")}</h1>
                {feedState.unreadCount > 0 ? (
                  <button
                    type="button"
                    className="btn btn--small"
                    disabled={markingAllRead}
                    onClick={() => void handleMarkAllRead()}
                  >
                    {translate(locale, "inboxMarkAllRead")}
                  </button>
                ) : null}
              </div>
              <div className="inbox-page__toolbar">
                <div className="metric-presets" role="tablist" aria-label={translate(locale, "inboxTitle")}>
                  <button
                    type="button"
                    role="tab"
                    aria-selected={filter === "all"}
                    className={filter === "all" ? "is-active" : undefined}
                    onClick={() => setFilter("all")}
                  >
                    {translate(locale, "inboxFilterAll")}
                  </button>
                  <button
                    type="button"
                    role="tab"
                    aria-selected={filter === "unread"}
                    className={filter === "unread" ? "is-active" : undefined}
                    onClick={() => setFilter("unread")}
                  >
                    {translate(locale, "inboxFilterUnread")}
                    {feedState.unreadCount > 0 ? ` (${feedState.unreadCount})` : ""}
                  </button>
                </div>
              </div>
              <PendingCheckinCard
                locale={locale}
                prompts={feedState.prompts}
                onAnswered={handlePromptResolved}
                onExpired={handlePromptResolved}
              />
              {feedState.items.length === 0 ? (
                <p className="inbox-page__empty">
                  {translate(locale, filter === "unread" ? "inboxEmptyUnread" : "inboxEmpty")}
                </p>
              ) : (
                <ul className="inbox-list">
                  {feedState.items.map((item) => {
                    const isUnread = !item.read_at;
                    return (
                      <li key={item.id} className="inbox-list__row">
                        <a
                          className={isUnread ? "inbox-item inbox-item--unread" : "inbox-item"}
                          href={`/inbox/${encodeURIComponent(item.id)}?lang=${locale}`}
                        >
                          <div className="inbox-item__top">
                            <span className="inbox-item__title-wrap">
                              {isUnread ? (
                                <span
                                  className="inbox-item__unread-dot"
                                  aria-label={translate(locale, "inboxUnread")}
                                />
                              ) : null}
                              <span className="inbox-item__title">{item.title}</span>
                            </span>
                            <span className="inbox-item__date">{item.local_date}</span>
                          </div>
                          <span className="inbox-item__kind">{kindLabel(locale, item.kind)}</span>
                          {!isPromptKind(item.kind) && item.preview ? (
                            <p className="inbox-item__preview">{item.preview}</p>
                          ) : isPromptKind(item.kind) ? (
                            <p className="inbox-item__meta">
                              {statusLabelForPrompt(locale, item.status)}
                              {item.answer
                                ? ` · ${answerLabelForPrompt(locale, item.answer)}`
                                : ""}
                            </p>
                          ) : null}
                        </a>
                      </li>
                    );
                  })}
                </ul>
              )}
              {feedState.nextCursor ? (
                <div className="inbox-page__more">
                  <button
                    type="button"
                    className="primary-action"
                    disabled={!canLoadMore}
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
