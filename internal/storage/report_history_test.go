package storage

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestArchivedReportReservationAndRetry(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	key := "report:morning:2026-11-09"
	makeReport := func() *ReportHistory {
		return &ReportHistory{DeliveryKey: &key, Kind: "morning", LocalDate: "2026-11-09", Timezone: "Europe/Belgrade", Language: "en", PlainBody: "body", Context: json.RawMessage(`{"decision":"rest"}`)}
	}
	h := makeReport()
	token, reserved, err := db.ReserveArchivedReport(ctx, h)
	if err != nil || !reserved {
		t.Fatalf("reserve: %v %v", reserved, err)
	}
	if h.Revision != 1 || h.ID == uuid.Nil {
		t.Fatalf("invalid revision/id: %+v", h)
	}
	if _, reserved, err = db.ReserveArchivedReport(ctx, makeReport()); err != nil || reserved {
		t.Fatalf("duplicate reserved: %v %v", reserved, err)
	}
	attempt, err := db.ReserveReportAttempt(ctx, h.ID, "rich", "rich body", token)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CompleteReportAttempt(ctx, attempt, "failed", "provider_rejected", nil); err != nil {
		t.Fatal(err)
	}
	if err = db.CompleteNotificationDelivery(ctx, key, token, "failed", "provider_rejected"); err != nil {
		t.Fatal(err)
	}
	retry := makeReport()
	_, reserved, err = db.ReserveArchivedReport(ctx, retry)
	if err != nil || !reserved || retry.Revision != 2 {
		t.Fatalf("retry: %v %+v %v", reserved, retry, err)
	}
	detail, err := db.GetReportHistory(ctx, h.ID)
	if err != nil || detail.FormatRevision != "morning-domains-v2" || len(detail.Attempts) != 1 || detail.Attempts[0].Body != "rich body" {
		t.Fatalf("detail: %+v %v", detail, err)
	}
	badKey := "report:bad"
	bad := makeReport()
	bad.DeliveryKey = &badKey
	bad.Context = json.RawMessage(`invalid`)
	if _, _, err = db.ReserveArchivedReport(ctx, bad); err == nil {
		t.Fatal("expected invalid JSON error")
	}
	preview := makeReport()
	preview.DeliveryKey = nil
	preview.IsPreview = true
	if _, ok, e := db.ReserveArchivedReport(ctx, preview); e != nil || !ok || preview.ID == h.ID {
		t.Fatalf("preview: %v %v", ok, e)
	}
	legacyKey := "report:evening:2026-11-08"
	if _, _, e := db.ReserveNotificationDelivery(ctx, legacyKey); e != nil {
		t.Fatal(e)
	}
	items, e := db.ListReportHistory(ctx, ReportHistoryQuery{From: "2026-11-01", To: "2026-11-30", Limit: 20})
	if e != nil {
		t.Fatal(e)
	}
	foundLegacy := false
	for _, item := range items {
		if item.Kind == "evening" {
			foundLegacy = true
			if item.ContentAvailable {
				t.Fatal("legacy content fabricated")
			}
			legacy, e := db.GetLegacyReportHistory(ctx, item.ID)
			if e != nil || legacy.ContentAvailable {
				t.Fatalf("legacy detail: %+v %v", legacy, e)
			}
		}
	}
	if !foundLegacy {
		t.Fatal("missing legacy delivery")
	}
	var count int
	if err = db.pool.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries WHERE delivery_key=$1`, badKey).Scan(&count); err != nil || count != 0 {
		t.Fatalf("reservation leaked on rollback: %d %v", count, err)
	}
}
