package storage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"health-receiver/internal/testdb"
)

func TestNormalizeReportPlainText(t *testing.T) {
	raw := "<b>Morning Report</b><br/>Sleep: 7h 15m &amp; steady\n\n\n<i>Recovery:</i> <a href=\"https://example.com\">Good</a>"
	got := NormalizeReportPlainText(raw)
	want := "Morning Report\nSleep: 7h 15m & steady\n\nRecovery: Good"
	if got != want {
		t.Fatalf("NormalizeReportPlainText() = %q, want %q", got, want)
	}
}

func TestValidateCheckinChannels(t *testing.T) {
	for _, ch := range []string{CheckinChannelIOSApp, CheckinChannelWeb} {
		if err := ValidateClientCheckinChannel(ch); err != nil {
			t.Fatalf("client channel %q rejected: %v", ch, err)
		}
		if err := ValidateCheckinChannel(ch); err != nil {
			t.Fatalf("channel %q rejected: %v", ch, err)
		}
	}
	if err := ValidateCheckinChannel(CheckinChannelTelegram); err != nil {
		t.Fatalf("telegram channel rejected: %v", err)
	}
	if err := ValidateClientCheckinChannel(CheckinChannelTelegram); err == nil {
		t.Fatal("client channel must reject telegram")
	}
	if err := ValidateClientCheckinChannel("unknown"); err == nil {
		t.Fatal("client channel must reject unknown")
	}
}

func TestIsInAppInboxEnabledDefaultsOffAndRespectsPerTenantOverride(t *testing.T) {
	t.Setenv("IN_APP_INBOX_ENABLED", "")
	if IsInAppInboxEnabled(nil) {
		t.Fatal("IsInAppInboxEnabled(nil) must default to false for phased rollout")
	}
	t.Setenv("IN_APP_INBOX_ENABLED", "true")
	if !IsInAppInboxEnabled(nil) {
		t.Fatal("IN_APP_INBOX_ENABLED=true should enable default")
	}
	t.Setenv("IN_APP_INBOX_ENABLED", "false")
	if IsInAppInboxEnabled(nil) {
		t.Fatal("IN_APP_INBOX_ENABLED=false should disable default")
	}
}

func TestResolveReportLocationMatchesTimeLocalFallback(t *testing.T) {
	if got := ResolveReportLocation(""); got != time.Local {
		t.Fatalf("empty tz resolved to %v, want time.Local (%v)", got, time.Local)
	}
	if got := ResolveReportLocation("Not/A_Zone"); got != time.Local {
		t.Fatalf("invalid tz resolved to %v, want time.Local (%v)", got, time.Local)
	}
	belgrade, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Fatal(err)
	}
	if got := ResolveReportLocation("Europe/Belgrade"); got.String() != belgrade.String() {
		t.Fatalf("Europe/Belgrade resolved to %v, want %v", got, belgrade)
	}
}

func setupInboxTestDB(t *testing.T, prefix string) (*DB, func()) {
	t.Helper()
	dsn := testdb.DSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	schema := testdb.SchemaName(prefix)
	bootstrap, err := testdb.NewPool(ctx, dsn, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := testdb.CreateSchema(ctx, bootstrap, schema); err != nil {
		bootstrap.Close()
		t.Fatal(err)
	}
	pool, err := testdb.NewPool(ctx, dsn, schema)
	if err != nil {
		_ = testdb.DropSchema(context.Background(), bootstrap, schema)
		bootstrap.Close()
		t.Fatal(err)
	}
	db := NewFromPool(pool)
	if err := db.MigrateSchemaContractContext(ctx); err != nil {
		db.Close()
		_ = testdb.DropSchema(context.Background(), bootstrap, schema)
		bootstrap.Close()
		t.Fatal(err)
	}
	cleanup := func() {
		db.Close()
		_ = testdb.DropSchema(context.Background(), bootstrap, schema)
		bootstrap.Close()
	}
	return db, cleanup
}

func TestInboxMorningReportPublishAndTelegramRetryReusesReportID(t *testing.T) {
	db, cleanup := setupInboxTestDB(t, "inbox_morning")
	defer cleanup()

	ctx := context.Background()
	today := "2026-11-25"
	key := "report:morning:" + today
	first := &ReportHistory{
		DeliveryKey: &key,
		Kind:        "morning",
		LocalDate:   today,
		Timezone:    "UTC",
		Language:    "en",
		PlainBody:   "<b>Morning</b>\nSleep 7h 30m",
	}
	inboxID, created, err := db.PublishMorningReportToInbox(ctx, first, false)
	if err != nil || !created || inboxID == uuid.Nil || first.ID == uuid.Nil {
		t.Fatalf("first publish: inboxID=%s created=%v id=%s err=%v", inboxID, created, first.ID, err)
	}
	if !db.HasPublishedMorningInboxReport(today) {
		t.Fatal("expected morning inbox report to be marked published")
	}

	// Repeat publish without allowNewRevision returns the exact same report and inbox card.
	second := &ReportHistory{
		DeliveryKey: &key,
		Kind:        "morning",
		LocalDate:   today,
		Timezone:    "UTC",
		Language:    "en",
		PlainBody:   "<b>Morning v2</b>",
	}
	inboxID2, created2, err := db.PublishMorningReportToInbox(ctx, second, false)
	if err != nil || created2 || inboxID2 != inboxID || second.ID != first.ID {
		t.Fatalf("repeat publish: inboxID=%s (want %s) created=%v id=%s (want %s) err=%v", inboxID2, inboxID, created2, second.ID, first.ID, err)
	}

	// Telegram delivery reserves existing report_id; active <30s reservation is settled,
	// stale >=30s zero-attempt reservation becomes recoverable (HasSettledMorningReport=false),
	// failed allows retry with same report_id, and ambiguous stops retry.
	tok1, ok, err := db.ReserveReportDeliveryForExisting(ctx, first.ID)
	if err != nil || !ok {
		t.Fatalf("reserve 1: ok=%v err=%v", ok, err)
	}
	if !db.HasSettledMorningReport(today) {
		t.Fatal("fresh <30s reservation must appear settled")
	}
	if _, err := db.pool.Exec(ctx, `UPDATE notification_deliveries SET reserved_at = NOW() - INTERVAL '35 seconds' WHERE delivery_key = $1`, key); err != nil {
		t.Fatalf("age reservation: %v", err)
	}
	if db.HasSettledMorningReport(today) {
		t.Fatal("stale >=30s zero-attempt reservation must NOT appear settled so minute tick can recover")
	}
	// Recovering the stale zero-attempt reservation succeeds with the same report_id.
	tokRecovered, ok, err := db.ReserveReportDeliveryForExisting(ctx, first.ID)
	if err != nil || !ok || tokRecovered == tok1 {
		t.Fatalf("recover stale zero-attempt reserve: ok=%v err=%v", ok, err)
	}
	if err := db.CompleteNotificationDelivery(ctx, key, tokRecovered, "failed", "provider_rejected"); err != nil {
		t.Fatalf("complete failed: %v", err)
	}
	if db.HasSettledMorningReport(today) {
		t.Fatal("failed delivery must not appear settled")
	}
	tok2, ok, err := db.ReserveReportDeliveryForExisting(ctx, first.ID)
	if err != nil || !ok || tok2 == tokRecovered {
		t.Fatalf("retry reserve after failed: ok=%v err=%v", ok, err)
	}
	if err := db.CompleteNotificationDelivery(ctx, key, tok2, "ambiguous", "transport_ambiguous"); err != nil {
		t.Fatalf("complete ambiguous: %v", err)
	}
	if !db.HasSettledMorningReport(today) {
		t.Fatal("ambiguous delivery must stay settled")
	}
	if _, ok, err := db.ReserveReportDeliveryForExisting(ctx, first.ID); err != nil || ok {
		t.Fatalf("ambiguous must block automatic retry: ok=%v err=%v", ok, err)
	}

	// Explicit new user revision updates inbox_items.report_id while report_history retains both revisions.
	rev2 := &ReportHistory{
		DeliveryKey: &key,
		Kind:        "morning",
		LocalDate:   today,
		Timezone:    "UTC",
		Language:    "en",
		PlainBody:   "<b>Morning revised</b>",
	}
	inboxID3, created3, err := db.PublishMorningReportToInbox(ctx, rev2, true)
	if err != nil || !created3 || inboxID3 != inboxID || rev2.ID == first.ID || rev2.Revision != 2 {
		t.Fatalf("revision 2 publish: inboxID=%s created=%v rev=%d err=%v", inboxID3, created3, rev2.Revision, err)
	}
	item, err := db.GetInboxItem(ctx, inboxID)
	if err != nil || item.ReportID == nil || *item.ReportID != rev2.ID || item.ReadAt != nil {
		t.Fatalf("inbox item after rev2 = %+v, err=%v", item, err)
	}
}

func TestInboxCheckinUnifiedAnswersConcurrencyAndCrossDayGuard(t *testing.T) {
	db, cleanup := setupInboxTestDB(t, "inbox_checkin")
	defer cleanup()

	ctx := context.Background()
	today := "2026-11-25"
	tomorrow := "2026-11-26"
	promptedAt := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	expiresAt := promptedAt.Add(2 * time.Hour)

	promptID, row, created, err := db.EnsureCanonicalCheckinPrompt(ctx, today, promptedAt, expiresAt)
	if err != nil || !created || promptID == uuid.Nil || row.Source != CheckinSourceInbox {
		t.Fatalf("ensure prompt: id=%s created=%v row=%+v err=%v", promptID, created, row, err)
	}
	// Idempotent re-ensure returns same promptID.
	promptID2, _, created2, err := db.EnsureCanonicalCheckinPrompt(ctx, today, promptedAt.Add(time.Minute), expiresAt)
	if err != nil || created2 || promptID2 != promptID {
		t.Fatalf("re-ensure: id=%s created=%v err=%v", promptID2, created2, err)
	}

	// Concurrent answers across ios_app, web, and telegram: exactly one saved, all agree on canonical answer.
	type callResult struct {
		res *CheckinAnswerResult
		err error
	}
	channels := []struct {
		ch, ans string
		byDate  bool
	}{
		{CheckinChannelIOSApp, CheckinAnswerGreat, false},
		{CheckinChannelWeb, CheckinAnswerOK, false},
		{CheckinChannelTelegram, CheckinAnswerSick, true},
	}
	answeredAt := promptedAt.Add(5*time.Minute + 123456789*time.Nanosecond)
	results := make([]callResult, len(channels))
	var wg sync.WaitGroup
	for i, c := range channels {
		wg.Add(1)
		go func(idx int, ch, ans string, byDate bool) {
			defer wg.Done()
			if byDate {
				results[idx].res, results[idx].err = db.AnswerCheckinByDate(ctx, today, today, ch, ans, answeredAt)
			} else {
				results[idx].res, results[idx].err = db.AnswerCheckinByPromptID(ctx, promptID, today, ch, ans, answeredAt)
			}
		}(i, c.ch, c.ans, c.byDate)
	}
	wg.Wait()

	savedCount := 0
	var winningAnswer string
	var winningAnsweredAt time.Time
	for i, r := range results {
		if r.err != nil || r.res == nil {
			t.Fatalf("call %d failed: res=%+v err=%v", i, r.res, r.err)
		}
		if r.res.Timing != CheckinTimingOnTime {
			t.Fatalf("call %d timing=%q, want on_time", i, r.res.Timing)
		}
		if r.res.Status == CheckinAnswerStatusSaved {
			savedCount++
			winningAnswer = r.res.Answer
			winningAnsweredAt = r.res.AnsweredAt
		} else if r.res.Status != CheckinAnswerStatusAlreadyAnswered {
			t.Fatalf("call %d unexpected status=%q", i, r.res.Status)
		}
	}
	if savedCount != 1 {
		t.Fatalf("savedCount = %d, want 1", savedCount)
	}
	for i, r := range results {
		if r.res.Answer != winningAnswer {
			t.Fatalf("call %d returned answer %q, want canonical %q", i, r.res.Answer, winningAnswer)
		}
		if !r.res.AnsweredAt.Equal(winningAnsweredAt) {
			t.Fatalf("call %d returned answered_at %s, want stored %s", i, r.res.AnsweredAt, winningAnsweredAt)
		}
	}

	// Cross-day guard runs even on already-answered prompts and leaves stored row unchanged.
	expiredRes, err := db.AnswerCheckinByPromptID(ctx, promptID, tomorrow, CheckinChannelWeb, CheckinAnswerMeh, promptedAt.Add(24*time.Hour))
	if !errors.Is(err, ErrCheckinPromptExpired) || expiredRes == nil || expiredRes.Status != CheckinAnswerStatusExpired {
		t.Fatalf("cross-day answered prompt: res=%+v err=%v, want ErrCheckinPromptExpired", expiredRes, err)
	}
	stored, err := db.GetCanonicalTodayCheckin(today)
	if err != nil || stored == nil || stored.Answer != winningAnswer || stored.Status != CheckinStatusAnswered {
		t.Fatalf("stored row mutated after cross-day rejection: %+v err=%v", stored, err)
	}
}

func TestInboxCheckinMidDayTransitionAndLateAnswerRepeat(t *testing.T) {
	db, cleanup := setupInboxTestDB(t, "inbox_midday")
	defer cleanup()

	ctx := context.Background()
	today := "2026-11-25"
	promptedAt := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	expiresAt := promptedAt.Add(2 * time.Hour)

	// Pre-existing legacy Telegram row on transition day must stay canonical.
	if err := db.SaveCheckinPrompted(today, CheckinSourceTelegram, 777, promptedAt, expiresAt); err != nil {
		t.Fatalf("seed legacy telegram prompt: %v", err)
	}
	promptID, row, created, err := db.EnsureCanonicalCheckinPrompt(ctx, today, promptedAt.Add(10*time.Minute), expiresAt)
	if err != nil || created || row.Source != CheckinSourceTelegram || promptID == uuid.Nil {
		t.Fatalf("mid-day ensure: id=%s created=%v row=%+v err=%v", promptID, created, row, err)
	}
	var count int
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM subjective_checkins WHERE date=$1`, today).Scan(&count); err != nil || count != 1 {
		t.Fatalf("row count = %d (err=%v), want 1", count, err)
	}

	// Expire past expires_at -> disappears from pending, same-day late answer succeeds with timing=late.
	if _, err := db.ExpireCanonicalCheckin(today, expiresAt.Add(time.Minute)); err != nil {
		t.Fatalf("expire: %v", err)
	}
	pending, err := db.ListPendingInboxCheckins(ctx, today, expiresAt.Add(time.Minute))
	if err != nil || len(pending) != 0 {
		t.Fatalf("expired prompt must not be pending: %+v err=%v", pending, err)
	}
	lateAt := expiresAt.Add(30 * time.Minute)
	res1, err := db.AnswerCheckinByPromptID(ctx, promptID, today, CheckinChannelWeb, CheckinAnswerMeh, lateAt)
	if err != nil || res1.Status != CheckinAnswerStatusSaved || res1.Timing != CheckinTimingLate || res1.Answer != CheckinAnswerMeh {
		t.Fatalf("late answer 1: %+v err=%v", res1, err)
	}
	// Repeat tap on late_answered in same local day returns already_answered + timing=late without overwriting.
	res2, err := db.AnswerCheckinByPromptID(ctx, promptID, today, CheckinChannelIOSApp, CheckinAnswerGreat, lateAt.Add(5*time.Minute))
	if err != nil || res2.Status != CheckinAnswerStatusAlreadyAnswered || res2.Timing != CheckinTimingLate || res2.Answer != CheckinAnswerMeh {
		t.Fatalf("late answer repeat: %+v err=%v", res2, err)
	}

	// Canonical coverage counts 1 row for today and attributes answered_channel=web.
	cov, err := db.GetCheckinCoverage(today, "all", 7)
	if err != nil || len(cov.Rows) != 1 || cov.Rows[0].AnsweredChannel != CheckinChannelWeb {
		t.Fatalf("coverage = %+v err=%v", cov, err)
	}
}
