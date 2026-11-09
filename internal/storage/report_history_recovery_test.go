package storage

import (
	"context"
	"testing"
)

func TestArchivedReportRecoversOnlyFencedZeroAttemptReservation(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	key := "report:morning:2026-11-09"
	makeReport := func() *ReportHistory {
		return &ReportHistory{DeliveryKey: &key, Kind: "morning", LocalDate: "2026-11-09", Timezone: "UTC", Language: "en", PlainBody: "recorded content"}
	}
	first := makeReport()
	oldToken, ok, err := db.ReserveArchivedReport(ctx, first)
	if err != nil || !ok {
		t.Fatalf("reserve=%v %v", ok, err)
	}
	if _, err = db.pool.Exec(ctx, `UPDATE notification_deliveries SET reserved_at=now()-interval '1 minute' WHERE delivery_key=$1`, key); err != nil {
		t.Fatal(err)
	}
	second := makeReport()
	newToken, ok, err := db.ReserveArchivedReport(ctx, second)
	if err != nil || !ok || second.Revision != 2 {
		t.Fatalf("recover=%v %#v %v", ok, second, err)
	}
	if _, err = db.ReserveReportAttempt(ctx, first.ID, "html", first.PlainBody, oldToken); err == nil {
		t.Fatal("stale preparer could start transport after reservation was reclaimed")
	}
	if _, err = db.ReserveReportAttempt(ctx, second.ID, "html", second.PlainBody, newToken); err != nil {
		t.Fatal(err)
	}
	if _, err = db.pool.Exec(ctx, `UPDATE notification_deliveries SET reserved_at=now()-interval '1 minute' WHERE delivery_key=$1`, key); err != nil {
		t.Fatal(err)
	}
	if _, ok, err = db.ReserveArchivedReport(ctx, makeReport()); err != nil || ok {
		t.Fatalf("reservation with possible network call was recovered: %v %v", ok, err)
	}
	legacy := "report:evening:2026-11-08"
	if _, _, err = db.ReserveNotificationDelivery(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err = db.pool.Exec(ctx, `UPDATE notification_deliveries SET reserved_at=now()-interval '1 minute' WHERE delivery_key=$1`, legacy); err != nil {
		t.Fatal(err)
	}
	h := makeReport()
	h.DeliveryKey = &legacy
	if _, ok, err = db.ReserveArchivedReport(ctx, h); err != nil || ok {
		t.Fatal("legacy reservation with unknown transport was replayed")
	}
}
