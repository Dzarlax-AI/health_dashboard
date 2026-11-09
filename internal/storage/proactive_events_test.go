package storage

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestProactiveBudgetConcurrencyAndEpisodes(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	makeReport := func() *ReportHistory {
		return &ReportHistory{Kind: "proactive", LocalDate: "2026-11-09", Timezone: "UTC", Language: "en", PlainBody: "test"}
	}
	var wg sync.WaitGroup
	result := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := db.ReserveProactive(ctx, makeReport(), "placeholder", &ProactiveEventCandidate{Rule: "changed", FactualIdentity: "sleep-v1", PreviousConclusion: "push_hard", CurrentConclusion: "moderate", SourceReportID: uuid.New()}, now, time.UTC)
			if err != nil {
				t.Error(err)
			}
			result <- ok
		}()
	}
	wg.Wait()
	close(result)
	accepted := 0
	for ok := range result {
		if ok {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d concurrent reservations", accepted)
	}
	// Cooldown applies to legacy sends as well as daytime events.
	_, ok, err := db.ReserveProactive(ctx, makeReport(), "proactive:legacy:2026-11-09", nil, now.Add(59*time.Minute), time.UTC)
	if err != nil || ok {
		t.Fatalf("cooldown %v %v", ok, err)
	}
	_, ok, err = db.ReserveProactive(ctx, makeReport(), "proactive:legacy:2026-11-09", nil, now.Add(time.Hour), time.UTC)
	if err != nil || !ok {
		t.Fatalf("second send %v %v", ok, err)
	}
	_, ok, err = db.ReserveProactive(ctx, makeReport(), "proactive:other:2026-11-09", nil, now.Add(2*time.Hour), time.UTC)
	if err != nil || ok {
		t.Fatalf("daily limit %v %v", ok, err)
	}
	// An active unchanged episode remains suppressed on the following day.
	tomorrow := now.Add(24 * time.Hour)
	_, ok, err = db.ReserveProactive(ctx, makeReport(), "placeholder", &ProactiveEventCandidate{Rule: "changed", FactualIdentity: "sleep-v1", PreviousConclusion: "push_hard", CurrentConclusion: "moderate", SourceReportID: uuid.New()}, tomorrow, time.UTC)
	if err != nil || ok {
		t.Fatalf("episode repeat %v %v", ok, err)
	}
}

func TestProactiveRollbackAndMidnightCooldown(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	if _, err := db.pool.Exec(ctx, `DELETE FROM proactive_events; DELETE FROM report_delivery_attempts; DELETE FROM report_history; DELETE FROM notification_deliveries`); err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Fatal(err)
	}
	before := time.Date(2026, 10, 25, 23, 30, 0, 0, loc)
	report := func() *ReportHistory {
		return &ReportHistory{Kind: "proactive", LocalDate: before.Format("2006-01-02"), Timezone: loc.String(), Language: "en", PlainBody: "test"}
	}
	invalid := report()
	invalid.Context = []byte(`bad`)
	_, ok, err := db.ReserveProactive(ctx, invalid, "proactive:bad", nil, before, loc)
	if err == nil || ok {
		t.Fatalf("bad context accepted %v %v", ok, err)
	}
	var n int
	if err = db.pool.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries WHERE delivery_key='proactive:bad'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("leaked lease: %d %v", n, err)
	}
	token, ok, err := db.ReserveProactive(ctx, report(), "proactive:legacy", nil, before, loc)
	if err != nil || !ok {
		t.Fatalf("legacy %v %v", ok, err)
	}
	if err = db.CompleteNotificationDelivery(ctx, "proactive:legacy", token, "ambiguous", "transport_ambiguous"); err != nil {
		t.Fatal(err)
	}
	_, ok, err = db.ReserveProactive(ctx, report(), "proactive:nextday", nil, before.Add(45*time.Minute), loc)
	if err != nil || ok {
		t.Fatalf("midnight bypassed cooldown: %v %v", ok, err)
	}
	_, ok, err = db.ReserveProactive(ctx, report(), "proactive:nextday", nil, before.Add(time.Hour), loc)
	if err != nil || !ok {
		t.Fatalf("new day after cooldown: %v %v", ok, err)
	}
}

func TestProactiveRevisionAndResolution(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	if _, err := db.pool.Exec(ctx, `DELETE FROM proactive_events; DELETE FROM report_delivery_attempts; DELETE FROM report_history; DELETE FROM notification_deliveries`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	source := uuid.New()
	event := &ProactiveEventCandidate{Rule: "change", FactualIdentity: "fact1", PreviousConclusion: "push_hard", CurrentConclusion: "moderate", SourceReportID: source}
	newReport := func() *ReportHistory {
		return &ReportHistory{Kind: "proactive", LocalDate: "2026-11-11", Timezone: "UTC", Language: "en", PlainBody: "message"}
	}
	first := newReport()
	_, ok, err := db.ReserveProactive(ctx, first, "unused", event, now, time.UTC)
	if err != nil || !ok {
		t.Fatalf("first %v %v", ok, err)
	}
	changed := *event
	changed.FactualIdentity = "fact2"
	changed.CurrentConclusion = "active_recovery"
	active, e := db.ActiveProactiveEpisode(ctx, "change")
	if e != nil || active == nil {
		t.Fatalf("active %v", e)
	}
	changed.ExpectedEpisodeID = active.ID
	changed.ExpectedRevision = active.Revision
	changed.PreviousConclusion = active.Conclusion
	second := newReport()
	_, ok, err = db.ReserveProactive(ctx, second, "unused", &changed, now.Add(time.Hour), time.UTC)
	if err != nil || !ok {
		t.Fatalf("revision %v %v", ok, err)
	}
	var id uuid.UUID
	var rev int
	var prior string
	if err = db.pool.QueryRow(ctx, `SELECT id,revision,previous_conclusion FROM proactive_events WHERE rule='change' AND state='active'`).Scan(&id, &rev, &prior); err != nil || rev != 2 || prior != "moderate" {
		t.Fatalf("revision state %s %d %s %v", id, rev, prior, err)
	}
	if resolved, e := db.ResolveProactiveEvent(ctx, "change", *active, now.Add(90*time.Minute)); e != nil || resolved {
		t.Fatalf("stale revision resolved newer event: %v %v", resolved, e)
	}
	current, e := db.ActiveProactiveEpisode(ctx, "change")
	if e != nil || current == nil {
		t.Fatal(e)
	}
	if resolved, e := db.ResolveProactiveEvent(ctx, "change", *current, now.Add(2*time.Hour)); e != nil || !resolved {
		t.Fatalf("resolve %v %v", resolved, e)
	}
	_, ok, err = db.ReserveProactive(ctx, newReport(), "unused", event, now.Add(24*time.Hour), time.UTC)
	if err != nil || !ok {
		t.Fatalf("new episode %v %v", ok, err)
	}
	var newID uuid.UUID
	if err = db.pool.QueryRow(ctx, `SELECT id FROM proactive_events WHERE rule='change' AND state='active'`).Scan(&newID); err != nil || newID == id {
		t.Fatalf("episode identity %s %v", newID, err)
	}
	if resolved, e := db.ResolveProactiveEvent(ctx, "change", *current, now.Add(25*time.Hour)); e != nil || resolved {
		t.Fatalf("stale episode resolved replacement: %v %v", resolved, e)
	}
	latest, e := db.ActiveProactiveEpisode(ctx, "change")
	if e != nil || latest == nil || latest.ID != newID {
		t.Fatalf("replacement disappeared: %+v %v", latest, e)
	}
}

func TestRuntimeEnsureDoesNotProvisionProactiveEvents(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	if _, err := db.pool.Exec(ctx, `DROP TABLE proactive_events`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.EnsureProactiveEventsTableContext(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	if err := db.EnsureSchemaContractContext(ctx); err == nil {
		t.Fatal("runtime accepted missing proactive schema")
	}
	var exists bool
	if err := db.pool.QueryRow(ctx, `SELECT to_regclass(current_schema() || '.proactive_events') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatalf("runtime created proactive table: %v %v", exists, err)
	}
	if err := db.MigrateSchemaContractContext(ctx); err != nil {
		t.Fatalf("offline migration: %v", err)
	}
}

func TestProactiveRecoversOnlyProvenPreNetwork(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	if _, err := db.pool.Exec(ctx, `DELETE FROM proactive_events; DELETE FROM report_delivery_attempts; DELETE FROM report_history; DELETE FROM notification_deliveries`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	newReport := func() *ReportHistory {
		return &ReportHistory{Kind: "proactive", LocalDate: "2026-11-12", Timezone: "UTC", Language: "en", PlainBody: "body"}
	}
	candidate := &ProactiveEventCandidate{Rule: "recover", FactualIdentity: "f", PreviousConclusion: "moderate", CurrentConclusion: "push_hard", SourceReportID: uuid.New()}
	first := newReport()
	old, ok, err := db.ReserveProactive(ctx, first, "unused", candidate, now, time.UTC)
	if err != nil || !ok {
		t.Fatalf("first %v %v", ok, err)
	}
	active, err := db.ActiveProactiveEpisode(ctx, "recover")
	if err != nil || active == nil {
		t.Fatal(err)
	}
	candidate.ExpectedEpisodeID = active.ID
	candidate.ExpectedRevision = active.Revision
	recovered := newReport()
	token, ok, err := db.ReserveProactive(ctx, recovered, "unused", candidate, now.Add(31*time.Second), time.UTC)
	if err != nil || !ok || recovered.Revision != 2 {
		t.Fatalf("recovery %v %v %+v", ok, err, recovered)
	}
	if _, err = db.ReserveReportAttempt(ctx, first.ID, "html", "body", old); err == nil {
		t.Fatal("stale lease could send")
	}
	id, err := db.ReserveReportAttempt(ctx, recovered.ID, "html", "body", token)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CompleteReportAttempt(ctx, id, "ambiguous", "transport_ambiguous", nil); err != nil {
		t.Fatal(err)
	}
	if err = db.CompleteNotificationDelivery(ctx, *recovered.DeliveryKey, token, "ambiguous", "transport_ambiguous"); err != nil {
		t.Fatal(err)
	}
	_, ok, err = db.ReserveProactive(ctx, newReport(), "unused", candidate, now.Add(24*time.Hour), time.UTC)
	if err != nil || ok {
		t.Fatalf("ambiguous retry %v %v", ok, err)
	}
}

func TestProactiveProviderRejectionHasBoundedRetries(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	if _, err := db.pool.Exec(ctx, `DELETE FROM proactive_events; DELETE FROM report_delivery_attempts; DELETE FROM report_history; DELETE FROM notification_deliveries`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	newReport := func() *ReportHistory {
		return &ReportHistory{Kind: "proactive", LocalDate: "2026-11-12", Timezone: "UTC", Language: "en", PlainBody: "body"}
	}
	event := &ProactiveEventCandidate{Rule: "rejection", FactualIdentity: "fact", PreviousConclusion: "moderate", CurrentConclusion: "push_hard", SourceReportID: uuid.New()}
	reject := func(at time.Time) {
		t.Helper()
		h := newReport()
		token, ok, err := db.ReserveProactive(ctx, h, "unused", event, at, time.UTC)
		if err != nil || !ok {
			t.Fatalf("reserve %v %v", ok, err)
		}
		id, err := db.ReserveReportAttempt(ctx, h.ID, "html", "body", token)
		if err != nil {
			t.Fatal(err)
		}
		if err = db.CompleteReportAttempt(ctx, id, "failed", "provider_rejected", nil); err != nil {
			t.Fatal(err)
		}
		if err = db.CompleteNotificationDelivery(ctx, *h.DeliveryKey, token, "failed", "provider_rejected"); err != nil {
			t.Fatal(err)
		}
	}
	reject(now)
	active, err := db.ActiveProactiveEpisode(ctx, "rejection")
	if err != nil || active == nil {
		t.Fatal(err)
	}
	event.ExpectedEpisodeID = active.ID
	event.ExpectedRevision = active.Revision
	reject(now.Add(31 * time.Second))
	for i := 1; i <= 5; i++ {
		_, ok, err := db.ReserveProactive(ctx, newReport(), "unused", event, now.Add(time.Duration(i)*time.Hour), time.UTC)
		if err != nil || ok {
			t.Fatalf("unbounded retry %d: %v %v", i, ok, err)
		}
	}
}
