package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPushDevicesUpsertBindingRotationAndAtomicTokenUniqueness(t *testing.T) {
	db, cleanup := setupInboxTestDB(t, "push_devices")
	defer cleanup()
	ctx := context.Background()
	if err := db.EnsurePushTablesContext(ctx); err != nil {
		t.Fatal(err)
	}

	dev1 := uuid.New()
	tokA := strings.Repeat("a", 64)
	tokB := strings.Repeat("b", 64)

	// 1. Fresh registration
	d1, err := db.UpsertPushDevice(ctx, UpsertPushDeviceInput{
		DeviceID:  dev1,
		APNsToken: tokA,
		APNsEnv:   PushEnvSandbox,
		BundleID:  "com.dzarlax.health-sync",
		Locale:    "en",
	})
	if err != nil {
		t.Fatalf("upsert 1: %v", err)
	}
	if d1.BindingID == uuid.Nil || d1.RegistrationVersion != 1 || !d1.Enabled {
		t.Fatalf("unexpected initial device: %+v", d1)
	}

	// 2. Heartbeat / locale update preserves binding_id and registration_version
	d1Locale, err := db.UpsertPushDevice(ctx, UpsertPushDeviceInput{
		DeviceID:  dev1,
		APNsToken: tokA,
		APNsEnv:   PushEnvSandbox,
		BundleID:  "com.dzarlax.health-sync",
		Locale:    "ru",
	})
	if err != nil {
		t.Fatalf("upsert locale: %v", err)
	}
	if d1Locale.BindingID != d1.BindingID || d1Locale.RegistrationVersion != 1 || d1Locale.Locale != "ru" {
		t.Fatalf("heartbeat unexpectedly mutated binding/version: %+v", d1Locale)
	}

	// 3. Token rotation on active device preserves binding_id and bumps registration_version
	d1Rotated, err := db.UpsertPushDevice(ctx, UpsertPushDeviceInput{
		DeviceID:  dev1,
		APNsToken: tokB,
		APNsEnv:   PushEnvSandbox,
		BundleID:  "com.dzarlax.health-sync",
		Locale:    "ru",
	})
	if err != nil {
		t.Fatalf("upsert rotate: %v", err)
	}
	if d1Rotated.BindingID != d1.BindingID || d1Rotated.RegistrationVersion != 2 {
		t.Fatalf("rotation: binding=%s want %s, ver=%d want 2", d1Rotated.BindingID, d1.BindingID, d1Rotated.RegistrationVersion)
	}

	// 4. Another device registering the same active (token, env) atomically deactivates dev1
	dev2 := uuid.New()
	d2, err := db.UpsertPushDevice(ctx, UpsertPushDeviceInput{
		DeviceID:  dev2,
		APNsToken: tokB,
		APNsEnv:   PushEnvSandbox,
		BundleID:  "com.dzarlax.health-sync",
		Locale:    "en",
	})
	if err != nil {
		t.Fatalf("upsert dev2 with same token: %v", err)
	}
	if !d2.Enabled || d2.BindingID == d1.BindingID {
		t.Fatalf("unexpected dev2: %+v", d2)
	}
	devices, err := db.ListPushDevices(ctx)
	if err != nil || len(devices) != 2 {
		t.Fatalf("list devices: len=%d err=%v", len(devices), err)
	}
	for _, dev := range devices {
		if dev.DeviceID == dev1 && dev.Enabled {
			t.Fatal("dev1 must have been deactivated when dev2 claimed the same active token")
		}
	}

	// 5. DisablePushDeviceBinding only disables the exact binding_id
	if disabled, err := db.DisablePushDeviceBinding(ctx, dev2, uuid.New()); err != nil || disabled {
		t.Fatalf("wrong binding_id disable: disabled=%v err=%v", disabled, err)
	}
	if disabled, err := db.DisablePushDeviceBinding(ctx, dev2, d2.BindingID); err != nil || !disabled {
		t.Fatalf("exact binding_id disable: disabled=%v err=%v", disabled, err)
	}

	// 6. Re-registering dev2 issues a brand new binding_id so delayed DELETE for old binding_id is a no-op
	d2Again, err := db.UpsertPushDevice(ctx, UpsertPushDeviceInput{
		DeviceID:  dev2,
		APNsToken: tokB,
		APNsEnv:   PushEnvSandbox,
		BundleID:  "com.dzarlax.health-sync",
		Locale:    "en",
	})
	if err != nil {
		t.Fatalf("re-register dev2: %v", err)
	}
	if d2Again.BindingID == d2.BindingID || d2Again.RegistrationVersion != 2 || !d2Again.Enabled {
		t.Fatalf("re-register must issue fresh binding_id and bump version: old=%+v new=%+v", d2, d2Again)
	}
	if disabled, err := db.DisablePushDeviceBinding(ctx, dev2, d2.BindingID); err != nil || disabled {
		t.Fatalf("stale delayed DELETE must not disable re-registered device: disabled=%v err=%v", disabled, err)
	}
}

func TestPushDeliveryAtomicEnqueueLeaseExpiryAndApple410Timestamp(t *testing.T) {
	t.Setenv("APNS_PUSH_ENABLED", "")
	db, cleanup := setupInboxTestDB(t, "push_delivery_lifecycle")
	defer cleanup()
	ctx := context.Background()
	if err := db.EnsurePushTablesContext(ctx); err != nil {
		t.Fatal(err)
	}

	devActive := uuid.New()
	devStale := uuid.New()
	dActive, err := db.UpsertPushDevice(ctx, UpsertPushDeviceInput{
		DeviceID:  devActive,
		APNsToken: strings.Repeat("1", 64),
		APNsEnv:   PushEnvProduction,
		BundleID:  "com.dzarlax.health-sync",
		Locale:    "en",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertPushDevice(ctx, UpsertPushDeviceInput{
		DeviceID:  devStale,
		APNsToken: strings.Repeat("2", 64),
		APNsEnv:   PushEnvProduction,
		BundleID:  "com.dzarlax.health-sync",
		Locale:    "en",
	}); err != nil {
		t.Fatal(err)
	}
	// Backdate devStale beyond 180-day TTL.
	if _, err := db.pool.Exec(ctx, `UPDATE push_devices SET last_seen_at = NOW() - INTERVAL '181 days' WHERE device_id = $1`, devStale); err != nil {
		t.Fatal(err)
	}

	// With apns_push_enabled=false (default), publishing a morning report must NOT enqueue push rows.
	dayOff := "2026-11-24"
	keyOff := "report:morning:" + dayOff
	if _, _, err := db.PublishMorningReportToInbox(ctx, &ReportHistory{
		DeliveryKey: &keyOff, Kind: "morning", LocalDate: dayOff, Timezone: "UTC", Language: "en", PlainBody: "off",
	}, false); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM push_deliveries`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("push_deliveries with flag off = %d (err=%v), want 0", count, err)
	}

	// Enable APNs for this tenant.
	if err := db.SaveSettings(map[string]string{SettingAPNSPushEnabled: "true"}); err != nil {
		t.Fatal(err)
	}

	// Pre-existing legacy Telegram check-in on dayOff: EnsureCanonicalCheckinPrompt
	// reuses it (created=false) and must NOT retroactively enqueue a push.
	now := time.Now().UTC()
	if err := db.SaveCheckinPrompted(dayOff, CheckinSourceTelegram, 101, now.Add(-10*time.Minute), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := db.EnsureCanonicalCheckinPrompt(ctx, dayOff, now, now.Add(time.Hour)); err != nil || created {
		t.Fatalf("mid-day reuse: created=%v err=%v", created, err)
	}
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM push_deliveries`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("mid-day existing checkin must not enqueue retroactive push: count=%d err=%v", count, err)
	}

	// Fresh morning report with flag ON enqueues ONLY for devActive (not devStale).
	dayOn := "2026-11-25"
	keyOn := "report:morning:" + dayOn
	if _, _, err := db.PublishMorningReportToInbox(ctx, &ReportHistory{
		DeliveryKey: &keyOn, Kind: "morning", LocalDate: dayOn, Timezone: "UTC", Language: "en", PlainBody: "on",
	}, false); err != nil {
		t.Fatal(err)
	}
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM push_deliveries`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("push_deliveries after enabled publish = %d (err=%v), want 1", count, err)
	}

	// Claim delivery, expire lease BEFORE housekeeping runs -> both StartPushDeliveryAttempt and CompletePushDelivery must reject.
	claimed, err := db.ClaimNextPushDelivery(ctx)
	if err != nil || claimed == nil {
		t.Fatalf("claim 1: claimed=%+v err=%v", claimed, err)
	}
	if _, err := db.pool.Exec(ctx, `UPDATE push_deliveries SET lease_until = clock_timestamp() - INTERVAL '5 seconds' WHERE id = $1`, claimed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.StartPushDeliveryAttempt(ctx, claimed.ID, claimed.LeaseToken); !errors.Is(err, ErrPushLeaseLost) {
		t.Fatalf("StartPushDeliveryAttempt on expired lease err=%v, want ErrPushLeaseLost", err)
	}
	code200 := 200
	if applied, err := db.CompletePushDelivery(ctx, CompletePushDeliveryInput{
		DeliveryID: claimed.ID, LeaseToken: claimed.LeaseToken, DeliveryState: PushDeliveryStateAccepted, StatusCode: &code200,
	}); err != nil || applied {
		t.Fatalf("CompletePushDelivery on expired lease applied=%v err=%v, want false", applied, err)
	}

	// Housekeeping returns zero-attempt expired lease to retry_wait and disables devStale.
	if err := db.HousekeepPushDeliveries(ctx, 50); err != nil {
		t.Fatal(err)
	}
	var staleEnabled bool
	if err := db.pool.QueryRow(ctx, `SELECT enabled FROM push_devices WHERE device_id = $1`, devStale).Scan(&staleEnabled); err != nil || staleEnabled {
		t.Fatalf("devStale should be disabled by 180d TTL housekeeping: enabled=%v err=%v", staleEnabled, err)
	}

	// Re-claim, start an attempt, then expire lease -> housekeeping marks ambiguous (never retried).
	claimed2, err := db.ClaimNextPushDelivery(ctx)
	if err != nil || claimed2 == nil {
		t.Fatalf("claim 2 after zero-attempt recovery: claimed=%+v err=%v", claimed2, err)
	}
	if _, err := db.StartPushDeliveryAttempt(ctx, claimed2.ID, claimed2.LeaseToken); err != nil {
		t.Fatalf("start attempt 2: %v", err)
	}
	if _, err := db.pool.Exec(ctx, `UPDATE push_deliveries SET lease_until = clock_timestamp() - INTERVAL '5 seconds' WHERE id = $1`, claimed2.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.HousekeepPushDeliveries(ctx, 50); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.pool.QueryRow(ctx, `SELECT state FROM push_deliveries WHERE id = $1`, claimed2.ID).Scan(&state); err != nil || state != PushDeliveryStateAmbiguous {
		t.Fatalf("expired attempted lease state=%q (err=%v), want ambiguous", state, err)
	}

	// Fresh check-in push tests late 410 Apple timestamp guard.
	if _, _, created, err := db.EnsureCanonicalCheckinPrompt(ctx, dayOn, now, now.Add(2*time.Hour)); err != nil || !created {
		t.Fatalf("fresh checkin: created=%v err=%v", created, err)
	}
	claimedCI, err := db.ClaimNextPushDelivery(ctx)
	if err != nil || claimedCI == nil || claimedCI.Kind != InboxKindPromptCheckin {
		t.Fatalf("claim checkin: %+v err=%v", claimedCI, err)
	}
	attCI, err := db.StartPushDeliveryAttempt(ctx, claimedCI.ID, claimedCI.LeaseToken)
	if err != nil {
		t.Fatal(err)
	}
	code410 := 410
	oldAppleTS := dActive.UpdatedAt.Add(-time.Minute)
	if applied, err := db.CompletePushDelivery(ctx, CompletePushDeliveryInput{
		DeliveryID:          claimedCI.ID,
		LeaseToken:          claimedCI.LeaseToken,
		AttemptID:           attCI,
		DeliveryState:       PushDeliveryStateRejected,
		AttemptStatus:       PushAttemptStatusRejected,
		StatusCode:          &code410,
		Reason:              "Unregistered",
		InvalidateDevice:    true,
		DeviceID:            dActive.DeviceID,
		RegistrationVersion: dActive.RegistrationVersion,
		TokenHash:           dActive.TokenHash,
		AppleInvalidatedAt:  &oldAppleTS,
	}); err != nil || !applied {
		t.Fatalf("complete 410 old ts: applied=%v err=%v", applied, err)
	}
	var activeStillEnabled bool
	if err := db.pool.QueryRow(ctx, `SELECT enabled FROM push_devices WHERE device_id = $1`, devActive).Scan(&activeStillEnabled); err != nil || !activeStillEnabled {
		t.Fatalf("late 410 with timestamp older than updated_at must NOT disable device: enabled=%v err=%v", activeStillEnabled, err)
	}

	// Now test 410 with Apple timestamp newer than updated_at -> MUST invalidate device.
	dayNext := "2026-11-26"
	if _, _, created, err := db.EnsureCanonicalCheckinPrompt(ctx, dayNext, now, now.Add(2*time.Hour)); err != nil || !created {
		t.Fatal(err)
	}
	claimedCI2, err := db.ClaimNextPushDelivery(ctx)
	if err != nil || claimedCI2 == nil {
		t.Fatalf("claim checkin 2: %+v err=%v", claimedCI2, err)
	}
	attCI2, err := db.StartPushDeliveryAttempt(ctx, claimedCI2.ID, claimedCI2.LeaseToken)
	if err != nil {
		t.Fatal(err)
	}
	freshAppleTS := time.Now().UTC().Add(time.Minute)
	if applied, err := db.CompletePushDelivery(ctx, CompletePushDeliveryInput{
		DeliveryID:          claimedCI2.ID,
		LeaseToken:          claimedCI2.LeaseToken,
		AttemptID:           attCI2,
		DeliveryState:       PushDeliveryStateRejected,
		AttemptStatus:       PushAttemptStatusRejected,
		StatusCode:          &code410,
		Reason:              "Unregistered",
		InvalidateDevice:    true,
		DeviceID:            dActive.DeviceID,
		RegistrationVersion: dActive.RegistrationVersion,
		TokenHash:           dActive.TokenHash,
		AppleInvalidatedAt:  &freshAppleTS,
	}); err != nil || !applied {
		t.Fatalf("complete 410 fresh ts: applied=%v err=%v", applied, err)
	}
	if err := db.pool.QueryRow(ctx, `SELECT enabled FROM push_devices WHERE device_id = $1`, devActive).Scan(&activeStillEnabled); err != nil || activeStillEnabled {
		t.Fatalf("410 with fresh Apple timestamp MUST disable device: enabled=%v err=%v", activeStillEnabled, err)
	}
}

func TestInboxCheckinIOSPushChannelAndCrossChannelRepeat(t *testing.T) {
	db, cleanup := setupInboxTestDB(t, "inbox_ios_push")
	defer cleanup()
	ctx := context.Background()

	if err := ValidateCheckinChannel(CheckinChannelIOSPush); err != nil {
		t.Fatalf("ValidateCheckinChannel(ios_push): %v", err)
	}
	if err := ValidateClientCheckinChannel(CheckinChannelIOSPush); err != nil {
		t.Fatalf("ValidateClientCheckinChannel(ios_push): %v", err)
	}

	today := "2026-11-25"
	promptedAt := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	expiresAt := promptedAt.Add(2 * time.Hour)
	promptID, _, _, err := db.EnsureCanonicalCheckinPrompt(ctx, today, promptedAt, expiresAt)
	if err != nil {
		t.Fatal(err)
	}

	res1, err := db.AnswerCheckinByPromptID(ctx, promptID, today, CheckinChannelIOSPush, CheckinAnswerGreat, promptedAt.Add(3*time.Minute))
	if err != nil || res1.Status != CheckinAnswerStatusSaved || res1.AnsweredChannel != CheckinChannelIOSPush {
		t.Fatalf("ios_push first answer = %+v err=%v", res1, err)
	}
	resWeb, err := db.AnswerCheckinByPromptID(ctx, promptID, today, CheckinChannelWeb, CheckinAnswerSick, promptedAt.Add(4*time.Minute))
	if err != nil || resWeb.Status != CheckinAnswerStatusAlreadyAnswered || resWeb.Answer != CheckinAnswerGreat || resWeb.AnsweredChannel != CheckinChannelIOSPush {
		t.Fatalf("web repeat after ios_push = %+v err=%v", resWeb, err)
	}
	resTG, err := db.AnswerCheckinByDate(ctx, today, today, CheckinChannelTelegram, CheckinAnswerMeh, promptedAt.Add(5*time.Minute))
	if err != nil || resTG.Status != CheckinAnswerStatusAlreadyAnswered || resTG.Answer != CheckinAnswerGreat || resTG.AnsweredChannel != CheckinChannelIOSPush {
		t.Fatalf("telegram repeat after ios_push = %+v err=%v", resTG, err)
	}
	cov, err := db.GetCheckinCoverage(today, "all", 7)
	if err != nil || len(cov.Rows) != 1 || cov.Rows[0].AnsweredChannel != CheckinChannelIOSPush {
		t.Fatalf("coverage = %+v err=%v", cov, err)
	}
}

func TestPushGateFailsClosedOnReadError(t *testing.T) {
	t.Setenv("APNS_PUSH_ENABLED", "true")
	db, cleanup := setupInboxTestDB(t, "push_gate")
	defer cleanup()
	if !IsAPNSPushEnabled(db) {
		t.Fatal("absent tenant flag should use the process default")
	}
	if err := db.SaveSettings(map[string]string{SettingAPNSPushEnabled: "false"}); err != nil {
		t.Fatal(err)
	}
	if IsAPNSPushEnabled(db) {
		t.Fatal("explicit opt-out must override the process default")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if apnsPushEnabled(ctx, db) {
		t.Fatal("query failure must fail closed even with an enabled process default")
	}
}

func TestMorningReportRevisionDoesNotEnqueuePush(t *testing.T) {
	t.Setenv("APNS_PUSH_ENABLED", "")
	db, cleanup := setupInboxTestDB(t, "push_revision")
	defer cleanup()
	ctx := context.Background()
	if err := db.EnsurePushTablesContext(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertPushDevice(ctx, UpsertPushDeviceInput{
		DeviceID: uuid.New(), APNsToken: strings.Repeat("a", 64), APNsEnv: PushEnvProduction,
		BundleID: "com.dzarlax.health-sync", Locale: "en",
	}); err != nil {
		t.Fatal(err)
	}
	date := "2026-11-26"
	key := "report:morning:" + date
	first := ReportHistory{DeliveryKey: &key, LocalDate: date, Kind: "morning", Timezone: "UTC", Language: "en", PlainBody: "first"}
	inboxID, _, err := db.PublishMorningReportToInbox(ctx, &first, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSettings(map[string]string{SettingAPNSPushEnabled: "true"}); err != nil {
		t.Fatal(err)
	}
	revision := ReportHistory{DeliveryKey: &key, LocalDate: date, Kind: "morning", Timezone: "UTC", Language: "en", PlainBody: "revision"}
	if id, created, err := db.PublishMorningReportToInbox(ctx, &revision, true); err != nil || !created || id != inboxID {
		t.Fatalf("revision should be published to the same card: id=%s created=%v err=%v", id, created, err)
	}
	var count int
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM push_deliveries`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("revision enqueued %d retroactive deliveries: %v", count, err)
	}
}

func TestPushAttemptRechecksClaimedCheckinAndRenewsLease(t *testing.T) {
	t.Setenv("APNS_PUSH_ENABLED", "true")
	db, cleanup := setupInboxTestDB(t, "push_stale_claim")
	defer cleanup()
	ctx := context.Background()
	if err := db.EnsurePushTablesContext(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertPushDevice(ctx, UpsertPushDeviceInput{
		DeviceID: uuid.New(), APNsToken: strings.Repeat("b", 64), APNsEnv: PushEnvProduction,
		BundleID: "com.dzarlax.health-sync", Locale: "en",
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, day := range []string{"2026-11-25", "2026-11-26"} {
		promptID, _, _, err := db.EnsureCanonicalCheckinPrompt(ctx, day, now, now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		claim, err := db.ClaimNextPushDelivery(ctx)
		if err != nil || claim == nil {
			t.Fatalf("claim: %v, %v", claim, err)
		}
		if day == "2026-11-25" {
			if _, err := db.AnswerCheckinByPromptID(ctx, promptID, day, CheckinChannelWeb, CheckinAnswerGreat, now); err != nil {
				t.Fatal(err)
			}
			if _, err := db.StartPushDeliveryAttempt(ctx, claim.ID, claim.LeaseToken); !errors.Is(err, ErrPushDeliveryIneligible) {
				t.Fatalf("answered after claim: %v", err)
			}
			continue
		}
		if _, err := db.pool.Exec(ctx, `UPDATE push_deliveries SET lease_until = clock_timestamp() + interval '10 seconds' WHERE id=$1`, claim.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.StartPushDeliveryAttempt(ctx, claim.ID, claim.LeaseToken); err != nil {
			t.Fatal(err)
		}
		var sufficient bool
		if err := db.pool.QueryRow(ctx, `SELECT lease_until > clock_timestamp() + interval '30 seconds' FROM push_deliveries WHERE id=$1`, claim.ID).Scan(&sufficient); err != nil || !sufficient {
			t.Fatalf("attempt needs a refreshed lease: %v, %v", sufficient, err)
		}
	}
}

func TestPushPublicationWorksWithSinglePoolConnection(t *testing.T) {
	t.Setenv("APNS_PUSH_ENABLED", "true")
	db, cleanup := setupInboxTestDB(t, "push_single_connection")
	defer cleanup()
	config := db.pool.(*pgxpool.Pool).Config()
	config.MaxConns = 1
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	single := NewFromPool(pool)
	defer single.Close()

	date := "2026-11-26"
	key := "report:morning:" + date
	if _, _, err := single.PublishMorningReportToInbox(ctx, &ReportHistory{
		DeliveryKey: &key, LocalDate: date, Kind: "morning", Timezone: "UTC", Language: "en", PlainBody: "report",
	}, false); err != nil {
		t.Fatalf("publish report on a single connection: %v", err)
	}
	now := time.Now()
	if _, _, _, err := single.EnsureCanonicalCheckinPrompt(ctx, date, now, now.Add(time.Hour)); err != nil {
		t.Fatalf("publish check-in on a single connection: %v", err)
	}
}
