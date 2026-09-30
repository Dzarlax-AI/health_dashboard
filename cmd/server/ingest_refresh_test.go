package main

import (
	"reflect"
	"testing"
	"time"
)

func TestDispatchIngestRefreshSuppressesReportAfterCacheFailure(t *testing.T) {
	refreshed := false
	dates := []string{"2026-09-30"}
	reports := make(chan struct{}, 1)
	dispatchIngestRefresh(dates, false, func(got []string, ready bool) {
		if ready || !reflect.DeepEqual(got, dates) {
			t.Fatal("missing dirty-date recovery")
		}
		refreshed = true
	}, func() { reports <- struct{}{} })
	if !refreshed {
		t.Fatal("recovery was not scheduled")
	}
	select {
	case <-reports:
		t.Fatal("report triggered after failed inline cache")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestDispatchIngestRefreshSchedulesDerivedWorkBeforeReport(t *testing.T) {
	refreshed := make(chan bool, 1)
	reports := make(chan bool, 1)
	dispatchIngestRefresh([]string{"2026-09-30"}, true, func(_ []string, ready bool) {
		refreshed <- ready
	}, func() {
		select {
		case ready := <-refreshed:
			reports <- ready
		default:
			reports <- false
		}
	})
	select {
	case ok := <-reports:
		if !ok {
			t.Fatal("report preceded refresh scheduling")
		}
	case <-time.After(time.Second):
		t.Fatal("report was not triggered")
	}
}
