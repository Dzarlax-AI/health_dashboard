import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";

import { ClientApiError } from "../../api/client";
import {
  getEnergyBackfillStatus,
  getEnergyBackfillSummary,
  getImportStatus,
  getNotificationSettings,
  getWebhookStatus,
  retryWebhookRegistration,
  sendTestNotification,
  startEnergyBackfill,
  updateNotificationSettings,
  uploadHealthExport,
  type EnergyBackfillStatus,
  type EnergyBackfillSummary,
  type ImportStatus,
  type WebhookStatusResponse,
} from "../../api/operations";
import {
  clearSessionRecoveryAttempt,
  recoverSessionOnUnauthorized,
} from "../../auth/sessionRecovery";
import { AppHeader } from "../../components/AppHeader";
import { StatusPanel } from "../../components/StatusPanel";
import { resolveLocale, translate, type Locale } from "../../i18n";
import { tp, type PageMessageKey } from "../../i18n/pages";

type PageStatus =
  | { status: "loading" }
  | { status: "ready" }
  | { status: "unauthenticated" }
  | { status: "error"; message: string };

interface NoticeState {
  tone: "good" | "warn" | "danger";
  text: string;
}

function formatRelativeAge(iso: string | null): string {
  if (!iso) return "";
  const parsed = new Date(iso);
  if (Number.isNaN(parsed.getTime())) return "";
  const diffMin = Math.floor(Math.max(0, Date.now() - parsed.getTime()) / 60000);
  if (diffMin < 1) return "just now";
  if (diffMin < 60) return `${diffMin}m ago`;
  if (diffMin < 24 * 60) return `${Math.floor(diffMin / 60)}h ago`;
  return `${Math.floor(diffMin / (24 * 60))}d ago`;
}

function webhookPillClass(state: string): string {
  if (state === "ok") return "pill pill--good";
  if (state === "pending") return "pill pill--warn";
  if (state === "failed") return "pill pill--danger";
  return "pill";
}

function webhookStateLabel(locale: Locale, state: string): string {
  const map: Record<string, PageMessageKey> = {
    ok: "webhook_ok",
    pending: "webhook_pending",
    failed: "webhook_failed",
    deleted: "webhook_deleted",
    unknown: "webhook_unknown",
  };
  return map[state] ? tp(locale, map[state]) : state || "—";
}

export function SettingsPage() {
  const params = new URLSearchParams(window.location.search);
  const locale = resolveLocale(params.get("lang"));

  const [pageStatus, setPageStatus] = useState<PageStatus>({ status: "loading" });
  const [reloadKey, setReloadKey] = useState(0);
  const [isAdmin, setIsAdmin] = useState(false);
  const [canRetryWebhook, setCanRetryWebhook] = useState(true);

  // Preferred sleep source
  const [sleepSources, setSleepSources] = useState<string[]>([]);
  const [savedSleepSource, setSavedSleepSource] = useState("");
  const [selectedSleepSource, setSelectedSleepSource] = useState("");
  const [savingSleepSource, setSavingSleepSource] = useState(false);
  const [sleepSourceNotice, setSleepSourceNotice] = useState<NoticeState | null>(null);

  // Apple Health Import
  const importFileInputRef = useRef<HTMLInputElement | null>(null);
  const [importFile, setImportFile] = useState<File | null>(null);
  const [importBatch, setImportBatch] = useState("500");
  const [importPause, setImportPause] = useState("150");
  const [importPhase, setImportPhase] = useState<"idle" | "uploading" | "running">("idle");
  const [importStatus, setImportStatus] = useState<ImportStatus | null>(null);
  const [importError, setImportError] = useState<string | null>(null);

  // Telegram notifications
  const [telegramToken, setTelegramToken] = useState("");
  const [telegramChatId, setTelegramChatId] = useState("");
  const [reportLang, setReportLang] = useState("en");
  const [timezone, setTimezone] = useState("");
  const [morningWeekday, setMorningWeekday] = useState("");
  const [morningWeekend, setMorningWeekend] = useState("");
  const [eveningWeekday, setEveningWeekday] = useState("");
  const [eveningWeekend, setEveningWeekend] = useState("");
  const [daytimeEnabled, setDaytimeEnabled] = useState(false);
  const [daytimeStart, setDaytimeStart] = useState("9");
  const [daytimeEnd, setDaytimeEnd] = useState("21");
  const [savingNotify, setSavingNotify] = useState(false);
  const [notifyNotice, setNotifyNotice] = useState<NoticeState | null>(null);

  // Webhook status
  const [webhook, setWebhook] = useState<WebhookStatusResponse | null>(null);

  // EnergyBank backfill
  const [energySummary, setEnergySummary] = useState<EnergyBackfillSummary | null>(null);
  const [energySummaryError, setEnergySummaryError] = useState<string | null>(null);
  const [energyStatus, setEnergyStatus] = useState<EnergyBackfillStatus | null>(null);
  const [energyError, setEnergyError] = useState<string | null>(null);

  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);

  const refreshWebhook = useCallback(async () => {
    try {
      const next = await getWebhookStatus();
      setWebhook(next);
    } catch {
      // ignore background webhook refresh errors
    }
  }, []);

  const refreshEnergySummary = useCallback(async () => {
    try {
      const summary = await getEnergyBackfillSummary();
      setEnergySummary(summary);
      setEnergySummaryError(null);
    } catch (error: unknown) {
      setEnergySummaryError(
        error instanceof Error ? error.message : tp(locale, "errorGeneric"),
      );
    }
  }, [locale]);

  // Poll webhook while pending
  useEffect(() => {
    if (webhook?.state !== "pending") return;
    const id = window.setInterval(() => {
      void refreshWebhook();
    }, 5000);
    return () => window.clearInterval(id);
  }, [refreshWebhook, webhook?.state]);

  // Poll import progress while running
  useEffect(() => {
    if (importPhase !== "running") return;
    let active = true;
    let timer: number | undefined;
    const tick = async () => {
      try {
        const status = await getImportStatus();
        if (!active) return;
        setImportStatus(status);
        if (status.done || !status.running) {
          setImportPhase("idle");
          return;
        }
        timer = window.setTimeout(() => void tick(), 2000);
      } catch {
        if (active) timer = window.setTimeout(() => void tick(), 3000);
      }
    };
    timer = window.setTimeout(() => void tick(), 1500);
    return () => {
      active = false;
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, [importPhase]);

  // Poll EnergyBank backfill progress while running
  const energyRunningState = Boolean(energyStatus?.running);
  useEffect(() => {
    if (!energyRunningState) return;
    let active = true;
    let timer: number | undefined;
    const tick = async () => {
      try {
        const status = await getEnergyBackfillStatus();
        if (!active) return;
        setEnergyStatus(status);
        if (status.done || !status.running) {
          void refreshEnergySummary();
          return;
        }
        timer = window.setTimeout(() => void tick(), 1500);
      } catch {
        if (active) timer = window.setTimeout(() => void tick(), 3000);
      }
    };
    timer = window.setTimeout(() => void tick(), 1500);
    return () => {
      active = false;
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, [energyRunningState, refreshEnergySummary]);

  useEffect(() => {
    const controller = new AbortController();

    getNotificationSettings(controller.signal)
      .then((settings) => {
        if (controller.signal.aborted) return;
        clearSessionRecoveryAttempt(window.sessionStorage);

        const sources = settings.sleep_sources ?? [];
        const preferred = settings.preferred_sleep_source ?? "";
        setSleepSources(sources);
        setSavedSleepSource(preferred);
        setSelectedSleepSource(preferred);

        if (typeof settings.is_admin === "boolean") {
          setIsAdmin(settings.is_admin);
          setCanRetryWebhook(settings.is_admin);
        } else {
          setCanRetryWebhook(true);
        }

        setTelegramToken(settings.telegram_token ?? "");
        setTelegramChatId(settings.telegram_chat_id ?? "");
        setReportLang(settings.report_lang || "en");

        if (settings.timezone) {
          setTimezone(settings.timezone);
        } else {
          try {
            const detected = Intl.DateTimeFormat().resolvedOptions().timeZone;
            if (detected) setTimezone(detected);
          } catch {
            // ignore
          }
        }

        setMorningWeekday(
          settings.report_morning_weekday ? String(settings.report_morning_weekday) : "",
        );
        setMorningWeekend(
          settings.report_morning_weekend ? String(settings.report_morning_weekend) : "",
        );
        setEveningWeekday(
          settings.report_evening_weekday ? String(settings.report_evening_weekday) : "",
        );
        setEveningWeekend(
          settings.report_evening_weekend ? String(settings.report_evening_weekend) : "",
        );
        setDaytimeEnabled(Boolean(settings.report_daytime_enabled));
        setDaytimeStart(
          settings.report_daytime_start_hour != null
            ? String(settings.report_daytime_start_hour)
            : "9",
        );
        setDaytimeEnd(
          settings.report_daytime_end_hour != null
            ? String(settings.report_daytime_end_hour)
            : "21",
        );

        setPageStatus({ status: "ready" });

        // Hydrate secondary panels independently
        void refreshWebhook();
        void refreshEnergySummary();
        void getImportStatus(controller.signal)
          .then((s) => {
            if (!controller.signal.aborted && s.running) {
              setImportStatus(s);
              setImportPhase("running");
            }
          })
          .catch(() => undefined);
        void getEnergyBackfillStatus(controller.signal)
          .then((s) => {
            if (!controller.signal.aborted && s.running) {
              setEnergyStatus(s);
            }
          })
          .catch(() => undefined);
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
          setPageStatus({ status: "unauthenticated" });
          return;
        }
        setPageStatus({
          status: "error",
          message:
            error instanceof Error
              ? error.message
              : tp(locale, "sleepSourceLoadError"),
        });
      });

    return () => controller.abort();
  }, [locale, refreshEnergySummary, refreshWebhook, reloadKey]);

  const handleSaveSleepSource = async (event: FormEvent) => {
    event.preventDefault();
    if (savingSleepSource || selectedSleepSource === savedSleepSource) return;
    setSavingSleepSource(true);
    setSleepSourceNotice(null);
    try {
      await updateNotificationSettings({
        preferred_sleep_source: selectedSleepSource,
      });
      setSavedSleepSource(selectedSleepSource);
      setSleepSourceNotice({
        tone: "good",
        text: tp(locale, "sleepSourceSaved"),
      });
    } catch (error: unknown) {
      setSleepSourceNotice({
        tone: "danger",
        text:
          error instanceof Error
            ? error.message
            : tp(locale, "sleepSourceSaveError"),
      });
    } finally {
      setSavingSleepSource(false);
    }
  };

  const handleStartImport = async () => {
    if (!importFile || importPhase !== "idle") return;
    setImportPhase("uploading");
    setImportError(null);
    try {
      await uploadHealthExport(importFile, {
        batch: importBatch,
        pause: importPause,
      });
      setImportPhase("running");
    } catch (error: unknown) {
      setImportPhase("idle");
      setImportError(
        error instanceof Error ? error.message : tp(locale, "errorGeneric"),
      );
    }
  };

  const handleSaveNotify = async (event: FormEvent) => {
    event.preventDefault();
    setSavingNotify(true);
    setNotifyNotice(null);
    try {
      await updateNotificationSettings({
        telegram_token: telegramToken,
        telegram_chat_id: telegramChatId,
        report_lang: reportLang,
        timezone,
        report_morning_weekday: morningWeekday,
        report_morning_weekend: morningWeekend,
        report_evening_weekday: eveningWeekday,
        report_evening_weekend: eveningWeekend,
        report_daytime_enabled: String(daytimeEnabled),
        report_daytime_start_hour: daytimeStart,
        report_daytime_end_hour: daytimeEnd,
      });
      setNotifyNotice({ tone: "good", text: tp(locale, "notifySaved") });
      void refreshWebhook();
    } catch (error: unknown) {
      setNotifyNotice({
        tone: "danger",
        text:
          error instanceof Error
            ? error.message
            : tp(locale, "notifySaveError"),
      });
    } finally {
      setSavingNotify(false);
    }
  };

  const handleTestNotify = async (kind: "morning" | "evening") => {
    setNotifyNotice(null);
    try {
      const result = await sendTestNotification(kind);
      setNotifyNotice({
        tone: result.status === "ok" ? "good" : "danger",
        text: result.message || tp(locale, "notifyTestSent"),
      });
    } catch (error: unknown) {
      setNotifyNotice({
        tone: "danger",
        text:
          error instanceof Error ? error.message : tp(locale, "errorGeneric"),
      });
    }
  };

  const handleRetryWebhook = async () => {
    try {
      await retryWebhookRegistration();
    } finally {
      void refreshWebhook();
    }
  };

  const handleRunEnergyBackfill = async () => {
    if (energyStatus?.running) return;
    if (!window.confirm(tp(locale, "energyBackfillConfirm"))) return;
    setEnergyError(null);
    try {
      const status = await startEnergyBackfill({});
      setEnergyStatus(status);
    } catch (error: unknown) {
      setEnergyError(
        error instanceof Error ? error.message : tp(locale, "errorGeneric"),
      );
    }
  };

  const hasSavedUnavailableSource =
    Boolean(savedSleepSource) && !sleepSources.includes(savedSleepSource);
  const sleepSourceEmpty = sleepSources.length === 0 && !savedSleepSource;

  const hasTimezone = Boolean(timezone.trim());
  const energyRunning = Boolean(energyStatus?.running);
  const energyHasData = (energySummary?.complete_daily_scores ?? 0) > 0;
  const energyDisabledReason = energyRunning
    ? tp(locale, "energyBackfillRunning")
    : !hasTimezone
      ? tp(locale, "energyBackfillNeedTz")
      : !energyHasData
        ? tp(locale, "energyBackfillNoData")
        : "";

  const importPercent =
    importStatus && importStatus.total_bytes > 0
      ? Math.min(100, (importStatus.bytes_read / importStatus.total_bytes) * 100)
      : 0;

  const energyPercent =
    energyStatus && energyStatus.progress.total > 0
      ? Math.min(
          100,
          (energyStatus.progress.done / energyStatus.progress.total) * 100,
        )
      : 0;

  return (
    <div className="app-shell" data-locale={locale}>
      <AppHeader locale={locale} isAdmin={isAdmin} />
      <main className="subpage subpage--narrow">
        <header className="subpage__header">
          <div className="subpage__heading">
            <a className="subpage__back" href={`/?lang=${locale}`}>
              ← {tp(locale, "backToToday")}
            </a>
            <h1>{tp(locale, "settingsTitle")}</h1>
          </div>
        </header>

        {pageStatus.status === "loading" ? (
          <StatusPanel
            state="loading"
            title={tp(locale, "loading")}
            detail={translate(locale, "loadingDetail")}
          />
        ) : pageStatus.status === "unauthenticated" ? (
          <div className="standalone-status">
            <StatusPanel
              state="error"
              title={translate(locale, "signInTitle")}
              detail={translate(locale, "signInDetail")}
            />
            <a
              className="primary-action"
              href={`/login?next=${encodeURIComponent(`/settings?lang=${locale}`)}`}
            >
              {translate(locale, "signIn")}
            </a>
          </div>
        ) : pageStatus.status === "error" ? (
          <div className="standalone-status">
            <StatusPanel
              state="error"
              title={tp(locale, "sleepSourceLoadError")}
              detail={pageStatus.message}
            />
            <button
              type="button"
              className="primary-action"
              onClick={() => {
                setPageStatus({ status: "loading" });
                setReloadKey((n) => n + 1);
              }}
            >
              {tp(locale, "retry")}
            </button>
          </div>
        ) : (
          <>
            {/* Preferred sleep source */}
            <section className="panel" aria-labelledby="sleep-source-heading">
              <h2 className="panel__title" id="sleep-source-heading">
                {tp(locale, "sleepSourceTitle")}
              </h2>
              <form className="field-grid" onSubmit={handleSaveSleepSource}>
                <div className="field">
                  <label className="field__label" htmlFor="cfg-sleep-source">
                    {tp(locale, "sleepSourceLabel")}
                  </label>
                  <select
                    id="cfg-sleep-source"
                    className="field__select"
                    value={selectedSleepSource}
                    disabled={savingSleepSource || sleepSourceEmpty}
                    onChange={(event) => {
                      setSelectedSleepSource(event.target.value);
                      setSleepSourceNotice(null);
                    }}
                  >
                    <option value="">{tp(locale, "sleepSourceAutomatic")}</option>
                    {sleepSources.map((source) => (
                      <option key={source} value={source}>
                        {source}
                      </option>
                    ))}
                    {hasSavedUnavailableSource ? (
                      <option value={savedSleepSource} disabled>
                        {savedSleepSource} ({tp(locale, "sleepSourceUnavailable")})
                      </option>
                    ) : null}
                  </select>
                  <span className="field__hint">
                    {sleepSourceEmpty
                      ? tp(locale, "sleepSourceEmpty")
                      : tp(locale, "sleepSourceHint")}
                  </span>
                </div>
                <div className="btn-row">
                  <button
                    type="submit"
                    className="btn btn--primary"
                    disabled={
                      savingSleepSource ||
                      sleepSourceEmpty ||
                      selectedSleepSource === savedSleepSource
                    }
                  >
                    {savingSleepSource ? tp(locale, "saving") : tp(locale, "save")}
                  </button>
                </div>
                {sleepSourceNotice ? (
                  <div
                    role="status"
                    className={`notice notice--${sleepSourceNotice.tone}`}
                  >
                    {sleepSourceNotice.text}
                  </div>
                ) : null}
              </form>
            </section>

            {/* Apple Health import */}
            <section className="panel" aria-labelledby="import-heading">
              <h2 className="panel__title" id="import-heading">
                {tp(locale, "importTitle")}
              </h2>
              <p className="subpage__muted">{tp(locale, "importDesc")}</p>
              <div className="field-grid">
                <div className="field">
                  <div className="btn-row">
                    <button
                      type="button"
                      className="btn"
                      onClick={() => importFileInputRef.current?.click()}
                    >
                      {importFile ? importFile.name : tp(locale, "importChooseFile")}
                    </button>
                  </div>
                  <input
                    ref={importFileInputRef}
                    id="import-file-input"
                    type="file"
                    accept=".zip,.xml"
                    tabIndex={-1}
                    aria-hidden="true"
                    style={{ display: "none" }}
                    onChange={(event) =>
                      setImportFile(event.target.files?.[0] ?? null)
                    }
                  />
                </div>
                <div className="field-grid field-grid--two">
                  <div className="field">
                    <label className="field__label" htmlFor="import-batch">
                      {tp(locale, "importBatch")}
                    </label>
                    <input
                      id="import-batch"
                      type="number"
                      className="field__input"
                      min={100}
                      max={5000}
                      value={importBatch}
                      onChange={(event) => setImportBatch(event.target.value)}
                    />
                  </div>
                  <div className="field">
                    <label className="field__label" htmlFor="import-pause">
                      {tp(locale, "importPause")}
                    </label>
                    <input
                      id="import-pause"
                      type="number"
                      className="field__input"
                      min={0}
                      max={5000}
                      value={importPause}
                      onChange={(event) => setImportPause(event.target.value)}
                    />
                  </div>
                </div>
                <div className="btn-row">
                  <button
                    type="button"
                    className="btn btn--primary"
                    disabled={!importFile || importPhase !== "idle"}
                    onClick={handleStartImport}
                  >
                    {importPhase === "uploading"
                      ? tp(locale, "importUploading")
                      : importPhase === "running"
                        ? tp(locale, "importRunning")
                        : tp(locale, "importStart")}
                  </button>
                </div>
                {importError ? (
                  <div className="notice notice--danger" role="alert">
                    {importError}
                  </div>
                ) : null}
                {importStatus ? (
                  <div className="field">
                    <div className="progress-track">
                      <div
                        className="progress-fill"
                        style={{
                          width: `${importStatus.done ? 100 : importPercent}%`,
                        }}
                      />
                    </div>
                    <p className="subpage__muted" role="status">
                      {importStatus.done
                        ? tp(locale, "importDone", {
                            inserted: importStatus.inserted,
                            skipped: importStatus.skipped,
                          })
                        : tp(locale, "importProgress", {
                            parsed: importStatus.parsed,
                            inserted: importStatus.inserted,
                          })}
                      {importStatus.error ? ` · ${importStatus.error}` : ""}
                    </p>
                    {importStatus.workouts_parsed > 0 ? (
                      <p className="field__hint">
                        {tp(locale, "importWorkouts", {
                          upserted: importStatus.workouts_upserted,
                          parsed: importStatus.workouts_parsed,
                        })}
                      </p>
                    ) : null}
                  </div>
                ) : null}
              </div>
            </section>

            {/* Telegram notifications */}
            <section className="panel" aria-labelledby="notify-heading">
              <h2 className="panel__title" id="notify-heading">
                {tp(locale, "notifyTitle")}
              </h2>
              <form className="field-grid" onSubmit={handleSaveNotify}>
                <div className="field-grid field-grid--two">
                  <div className="field">
                    <label className="field__label" htmlFor="cfg-telegram-token">
                      {tp(locale, "notifyToken")}
                    </label>
                    <input
                      id="cfg-telegram-token"
                      type="password"
                      className="field__input"
                      autoComplete="off"
                      placeholder="123456:ABC-DEF..."
                      value={telegramToken}
                      onChange={(event) => setTelegramToken(event.target.value)}
                    />
                  </div>
                  <div className="field">
                    <label className="field__label" htmlFor="cfg-telegram-chat-id">
                      {tp(locale, "notifyChatId")}
                    </label>
                    <input
                      id="cfg-telegram-chat-id"
                      type="text"
                      className="field__input"
                      autoComplete="off"
                      placeholder="123456789"
                      value={telegramChatId}
                      onChange={(event) => setTelegramChatId(event.target.value)}
                    />
                  </div>
                </div>

                {webhook ? (
                  <div className="field">
                    <span className="field__label">
                      {tp(locale, "notifyWebhookLabel")}
                    </span>
                    <div className="btn-row">
                      <span className={webhookPillClass(webhook.state)}>
                        {webhookStateLabel(locale, webhook.state)}
                        {webhook.state === "failed" && webhook.reason
                          ? `: ${webhook.reason}`
                          : ""}
                      </span>
                      {webhook.updated_at ? (
                        <span className="field__hint">
                          ({formatRelativeAge(webhook.updated_at)})
                        </span>
                      ) : null}
                      {webhook.state === "failed" && canRetryWebhook ? (
                        <button
                          type="button"
                          className="btn btn--small"
                          onClick={handleRetryWebhook}
                        >
                          {tp(locale, "notifyWebhookRetry")}
                        </button>
                      ) : null}
                    </div>
                    {webhook.state === "failed" && webhook.description ? (
                      <div className="notice notice--danger">
                        {webhook.description}
                      </div>
                    ) : null}
                  </div>
                ) : null}

                <div className="field-grid field-grid--two">
                  <div className="field">
                    <label className="field__label" htmlFor="cfg-report-lang">
                      {tp(locale, "notifyLang")}
                    </label>
                    <select
                      id="cfg-report-lang"
                      className="field__select"
                      value={reportLang}
                      onChange={(event) => setReportLang(event.target.value)}
                    >
                      <option value="en">English</option>
                      <option value="ru">Русский</option>
                      <option value="sr">Srpski</option>
                    </select>
                  </div>
                  <div className="field">
                    <label className="field__label" htmlFor="cfg-timezone">
                      {tp(locale, "notifyTimezone")}
                      {!hasTimezone ? (
                        <span className="field__required" aria-hidden="true">
                          *
                        </span>
                      ) : null}
                    </label>
                    <input
                      id="cfg-timezone"
                      type="text"
                      className="field__input"
                      autoComplete="off"
                      placeholder="Europe/Belgrade"
                      value={timezone}
                      onChange={(event) => setTimezone(event.target.value)}
                    />
                    <span className="field__hint">
                      {tp(locale, "notifyTimezoneHint")}
                    </span>
                  </div>
                </div>

                <div className="field">
                  <span className="field__label">
                    {tp(locale, "notifyScheduleMorning")}
                  </span>
                  <div className="field-grid field-grid--two">
                    <div className="field">
                      <label className="field__hint" htmlFor="cfg-morning-weekday">
                        {tp(locale, "notifyWeekday")}
                      </label>
                      <input
                        id="cfg-morning-weekday"
                        type="number"
                        className="field__input"
                        min={0}
                        max={23}
                        placeholder="8"
                        value={morningWeekday}
                        onChange={(event) => setMorningWeekday(event.target.value)}
                      />
                    </div>
                    <div className="field">
                      <label className="field__hint" htmlFor="cfg-morning-weekend">
                        {tp(locale, "notifyWeekend")}
                      </label>
                      <input
                        id="cfg-morning-weekend"
                        type="number"
                        className="field__input"
                        min={0}
                        max={23}
                        placeholder="9"
                        value={morningWeekend}
                        onChange={(event) => setMorningWeekend(event.target.value)}
                      />
                    </div>
                  </div>
                </div>

                <div className="field">
                  <span className="field__label">
                    {tp(locale, "notifyScheduleEvening")}
                  </span>
                  <div className="field-grid field-grid--two">
                    <div className="field">
                      <label className="field__hint" htmlFor="cfg-evening-weekday">
                        {tp(locale, "notifyWeekday")}
                      </label>
                      <input
                        id="cfg-evening-weekday"
                        type="number"
                        className="field__input"
                        min={0}
                        max={23}
                        placeholder="20"
                        value={eveningWeekday}
                        onChange={(event) => setEveningWeekday(event.target.value)}
                      />
                    </div>
                    <div className="field">
                      <label className="field__hint" htmlFor="cfg-evening-weekend">
                        {tp(locale, "notifyWeekend")}
                      </label>
                      <input
                        id="cfg-evening-weekend"
                        type="number"
                        className="field__input"
                        min={0}
                        max={23}
                        placeholder="21"
                        value={eveningWeekend}
                        onChange={(event) => setEveningWeekend(event.target.value)}
                      />
                    </div>
                  </div>
                </div>

                <div className="field">
                  <span className="field__label">
                    {tp(locale, "notifyDaytimeTitle")}
                  </span>
                  <label className="field__checkbox">
                    <input
                      type="checkbox"
                      checked={daytimeEnabled}
                      onChange={(event) => setDaytimeEnabled(event.target.checked)}
                    />
                    {tp(locale, "notifyDaytimeEnabled")}
                  </label>
                  <span className="field__hint">
                    {tp(locale, "notifyDaytimeHint")}
                  </span>
                  <div className="field-grid field-grid--two">
                    <div className="field">
                      <label className="field__hint" htmlFor="cfg-daytime-start">
                        {tp(locale, "notifyDaytimeStart")}
                      </label>
                      <input
                        id="cfg-daytime-start"
                        type="number"
                        className="field__input"
                        min={0}
                        max={23}
                        value={daytimeStart}
                        onChange={(event) => setDaytimeStart(event.target.value)}
                      />
                    </div>
                    <div className="field">
                      <label className="field__hint" htmlFor="cfg-daytime-end">
                        {tp(locale, "notifyDaytimeEnd")}
                      </label>
                      <input
                        id="cfg-daytime-end"
                        type="number"
                        className="field__input"
                        min={0}
                        max={23}
                        value={daytimeEnd}
                        onChange={(event) => setDaytimeEnd(event.target.value)}
                      />
                    </div>
                  </div>
                </div>

                <div className="btn-row">
                  <button
                    type="submit"
                    className="btn btn--primary"
                    disabled={savingNotify}
                  >
                    {savingNotify ? tp(locale, "saving") : tp(locale, "notifySave")}
                  </button>
                  <button
                    type="button"
                    className="btn"
                    onClick={() => handleTestNotify("morning")}
                  >
                    {tp(locale, "notifyTestMorning")}
                  </button>
                  <button
                    type="button"
                    className="btn"
                    onClick={() => handleTestNotify("evening")}
                  >
                    {tp(locale, "notifyTestEvening")}
                  </button>
                </div>

                {notifyNotice ? (
                  <div
                    role="status"
                    className={`notice notice--${notifyNotice.tone}`}
                  >
                    {notifyNotice.text}
                  </div>
                ) : null}
              </form>
            </section>

            {/* Historical EnergyBank backfill */}
            <section className="panel" aria-labelledby="energy-backfill-heading">
              <h2 className="panel__title" id="energy-backfill-heading">
                {tp(locale, "energyBackfillTitle")}
              </h2>
              <p className="subpage__muted">{tp(locale, "energyBackfillDesc")}</p>
              {energySummaryError ? (
                <div className="notice notice--danger">{energySummaryError}</div>
              ) : energySummary ? (
                <p className="subpage__muted">
                  {tp(locale, "energyBackfillSummary", {
                    backfilled: energySummary.backfilled_snapshots,
                    complete: energySummary.complete_daily_scores,
                    earliest: energySummary.earliest_complete || "—",
                    to: energySummary.yesterday_in_tz || "—",
                  })}
                </p>
              ) : (
                <p className="subpage__muted">{tp(locale, "loading")}</p>
              )}

              <div className="btn-row">
                <button
                  type="button"
                  className="btn btn--primary"
                  disabled={Boolean(energyDisabledReason)}
                  title={energyDisabledReason || undefined}
                  onClick={handleRunEnergyBackfill}
                >
                  {tp(locale, "energyBackfillRun")}
                </button>
                {energyDisabledReason ? (
                  <span className="field__hint">{energyDisabledReason}</span>
                ) : null}
              </div>

              {energyError ? (
                <div className="notice notice--danger" role="alert">
                  {energyError}
                </div>
              ) : null}

              {energyStatus ? (
                <div className="field">
                  <div className="progress-track">
                    <div
                      className="progress-fill"
                      style={{
                        width: `${energyStatus.done ? 100 : energyPercent}%`,
                      }}
                    />
                  </div>
                  <p className="subpage__muted" role="status">
                    {energyStatus.done
                      ? tp(locale, "energyBackfillDone", {
                          ok: energyStatus.progress.ok,
                          skipped: energyStatus.progress.skipped,
                          errors: energyStatus.progress.errors,
                        })
                      : tp(locale, "energyBackfillProgress", {
                          done: energyStatus.progress.done,
                          total: energyStatus.progress.total,
                          ok: energyStatus.progress.ok,
                        })}
                    {energyStatus.error ? ` · ${energyStatus.error}` : ""}
                  </p>
                </div>
              ) : null}
            </section>
          </>
        )}
      </main>
    </div>
  );
}
