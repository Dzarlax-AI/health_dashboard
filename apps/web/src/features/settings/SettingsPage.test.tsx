import { fireEvent, render, screen, waitFor } from "@testing-library/react";

import * as clientApi from "../../api/client";
import * as ops from "../../api/operations";
import { SettingsPage } from "./SettingsPage";

describe("SettingsPage", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    window.history.replaceState({}, "", "/");
  });

  it("loads settings, saves preferred sleep source, and gates webhook retry on admin", async () => {
    window.history.replaceState({}, "", "/settings?lang=en");
    vi.spyOn(ops, "getNotificationSettings").mockResolvedValue({
      enabled: true,
      is_admin: true,
      preferred_sleep_source: "",
      sleep_sources: ["Apple Watch", "Oura"],
      telegram_token: "bot:token",
      telegram_chat_id: "12345",
      report_lang: "en",
      timezone: "Europe/Belgrade",
      report_morning_weekday: 8,
      report_morning_weekend: 9,
      report_evening_weekday: 20,
      report_evening_weekend: 21,
      report_daytime_enabled: false,
      report_daytime_start_hour: 9,
      report_daytime_end_hour: 21,
    });
    vi.spyOn(ops, "getWebhookStatus").mockResolvedValue({
      state: "failed",
      reason: "bad webhook",
      description: "HTTPS URL required",
      updated_at: new Date().toISOString(),
    });
    vi.spyOn(ops, "getEnergyBackfillSummary").mockResolvedValue({
      complete_daily_scores: 40,
      backfilled_snapshots: 20,
      earliest_complete: "2026-09-01",
      yesterday_in_tz: "2026-11-23",
      tz: "Europe/Belgrade",
    });
    vi.spyOn(ops, "getImportStatus").mockResolvedValue({
      running: false,
      done: false,
      parsed: 0,
      inserted: 0,
      skipped: 0,
      workouts_parsed: 0,
      workouts_upserted: 0,
      workouts_failed: 0,
      bytes_read: 0,
      total_bytes: 0,
      elapsed_sec: 0,
    });
    vi.spyOn(ops, "getEnergyBackfillStatus").mockResolvedValue({
      running: false,
      done: false,
      elapsed_sec: 0,
      progress: {
        done: 0,
        errors: 0,
        from: "",
        ok: 0,
        skipped: 0,
        to: "",
        total: 0,
        tz: "Europe/Belgrade",
      },
    });
    const updateSpy = vi
      .spyOn(ops, "updateNotificationSettings")
      .mockResolvedValue({ status: "ok" });

    render(<SettingsPage />);

    const select = await screen.findByLabelText("Sleep source");
    fireEvent.change(select, { target: { value: "Apple Watch" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      expect(updateSpy).toHaveBeenCalledWith({
        preferred_sleep_source: "Apple Watch",
      });
    });

    expect(await screen.findByText(/HTTPS URL required/)).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Retry registration" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Run Energy backfill" }),
    ).toBeEnabled();
    expect(
      screen.getByRole("button", { name: "Choose file (.zip, .xml)" }),
    ).toBeEnabled();
  });

  it("loads and saves notification preferences and warns when disabling Telegram with no push devices", async () => {
    window.history.replaceState({}, "", "/settings?lang=en");
    vi.spyOn(ops, "getNotificationSettings").mockResolvedValue({
      enabled: true,
      is_admin: false,
      preferred_sleep_source: "",
      sleep_sources: [],
      telegram_token: "bot:token",
      telegram_chat_id: "12345",
      report_lang: "en",
      timezone: "Europe/Belgrade",
      report_morning_weekday: 8,
      report_morning_weekend: 9,
      report_evening_weekday: 20,
      report_evening_weekend: 21,
      report_daytime_enabled: true,
      report_daytime_start_hour: 9,
      report_daytime_end_hour: 21,
    });
    vi.spyOn(ops, "getWebhookStatus").mockResolvedValue({
      state: "ok",
      reason: "",
      updated_at: new Date().toISOString(),
    });
    vi.spyOn(ops, "getEnergyBackfillSummary").mockResolvedValue({
      complete_daily_scores: 0,
      backfilled_snapshots: 0,
      tz: "Europe/Belgrade",
    });
    vi.spyOn(ops, "getImportStatus").mockResolvedValue({
      running: false,
      done: false,
      parsed: 0,
      inserted: 0,
      skipped: 0,
      workouts_parsed: 0,
      workouts_upserted: 0,
      workouts_failed: 0,
      bytes_read: 0,
      total_bytes: 0,
      elapsed_sec: 0,
    });
    vi.spyOn(ops, "getEnergyBackfillStatus").mockResolvedValue({
      running: false,
      done: false,
      elapsed_sec: 0,
      progress: {
        done: 0,
        errors: 0,
        from: "",
        ok: 0,
        skipped: 0,
        to: "",
        total: 0,
        tz: "Europe/Belgrade",
      },
    });
    vi.spyOn(clientApi, "getNotificationPreferences").mockResolvedValue({
      morning_weekday_hour: 8,
      morning_weekend_hour: 9,
      evening_weekday_hour: 20,
      evening_weekend_hour: 21,
      daytime_enabled: true,
      daytime_start_hour: 9,
      daytime_end_hour: 21,
      push_morning_report: true,
      push_evening_report: true,
      push_checkins: true,
      push_daytime_updates: true,
      push_digests_nudges: true,
      telegram_notifications_enabled: true,
      inbox_enabled: true,
      apns_push_enabled: true,
      telegram_configured: true,
      active_push_devices_count: 0,
    });
    const putSpy = vi
      .spyOn(clientApi, "putNotificationPreferences")
      .mockResolvedValue({
        morning_weekday_hour: 8,
        morning_weekend_hour: 9,
        evening_weekday_hour: 20,
        evening_weekend_hour: 21,
        daytime_enabled: true,
        daytime_start_hour: 9,
        daytime_end_hour: 21,
        push_morning_report: true,
        push_evening_report: false,
        push_checkins: true,
        push_daytime_updates: true,
        push_digests_nudges: true,
        telegram_notifications_enabled: false,
        inbox_enabled: true,
        apns_push_enabled: true,
        telegram_configured: true,
        active_push_devices_count: 0,
      });

    render(<SettingsPage />);

    const tgToggle = await screen.findByLabelText("Send notifications to Telegram");
    fireEvent.click(tgToggle);
    expect(
      screen.getByText(/No active iOS push devices are registered/),
    ).toBeInTheDocument();

    const eveningPush = screen.getByLabelText("Evening report");
    fireEvent.click(eveningPush);

    fireEvent.click(screen.getByRole("button", { name: "Save preferences" }));

    await waitFor(() => {
      expect(putSpy).toHaveBeenCalledWith({
        push_morning_report: true,
        push_evening_report: false,
        push_checkins: true,
        push_daytime_updates: true,
        push_digests_nudges: true,
        telegram_notifications_enabled: false,
      });
    });
    expect(screen.getByText("Notification preferences saved.")).toBeInTheDocument();
  });
});
