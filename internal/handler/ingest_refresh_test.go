package handler

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"health-receiver/internal/storage"
)

func TestFlushDatesReportsCacheOutcome(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "needs-recovery"}[failed], func(t *testing.T) {
			db := &storage.DB{}
			dates := []string{"2026-09-30"}
			var calls []string
			h := &Handler{
				refreshCache: func(got *storage.DB, gotDates []string, scores bool) error {
					if got != db || !reflect.DeepEqual(gotDates, dates) || !scores {
						t.Fatal("cache refresh lost ingest arguments")
					}
					calls = append(calls, "cache")
					if failed {
						return errors.New("temporary failure")
					}
					return nil
				},
				onNewData: func(got *storage.DB, gotDates []string, ready bool) {
					if got != db || !reflect.DeepEqual(gotDates, dates) || ready == failed {
						t.Fatal("incorrect cache outcome")
					}
					calls = append(calls, "notify")
				},
			}
			h.flushDates(db, dates, true)
			if !reflect.DeepEqual(calls, []string{"cache", "notify"}) {
				t.Fatalf("order: %v", calls)
			}
		})
	}
}

func TestSessionFinalizationRefreshesUnionOnce(t *testing.T) {
	for _, mode := range []string{"complete", "timeout", "shutdown", "unbatched"} {
		t.Run(mode, func(t *testing.T) {
			db := &storage.DB{}
			type outcome struct {
				dates  []string
				ready  bool
				scores bool
			}
			results := make(chan outcome, 3)
			var cacheCalls atomic.Int32
			var scores bool
			h := &Handler{
				sessions: make(map[string]*syncSession),
				refreshCache: func(_ *storage.DB, dates []string, recompute bool) error {
					cacheCalls.Add(1)
					scores = recompute
					return nil
				},
				onNewData: func(_ *storage.DB, dates []string, ready bool) {
					copyDates := append([]string(nil), dates...)
					sort.Strings(copyDates)
					results <- outcome{copyDates, ready, scores}
				},
			}
			if mode == "timeout" {
				h.sessionWait = 10 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			defer func() {
				if err := h.Shutdown(ctx); err != nil {
					t.Error(err)
				}
			}()
			if mode == "unbatched" {
				h.finalizeChunk("", 0, db, []string{"2026-09-29", "2026-09-30"}, true)
			} else {
				total := 3
				if mode == "complete" {
					total = 2
				}
				h.finalizeChunk("synthetic-session", total, db, []string{"2026-09-29"}, false)
				h.finalizeChunk("synthetic-session", total, db, []string{"2026-09-29", "2026-09-30"}, true)
				if mode == "shutdown" {
					if err := h.Shutdown(ctx); err != nil {
						t.Fatal(err)
					}
				}
			}
			select {
			case got := <-results:
				if !reflect.DeepEqual(got.dates, []string{"2026-09-29", "2026-09-30"}) || !got.ready || !got.scores {
					t.Fatalf("outcome: %+v", got)
				}
			case <-ctx.Done():
				t.Fatal("no refresh")
			}
			if err := h.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			if cacheCalls.Load() != 1 {
				t.Fatalf("cache calls: %d", cacheCalls.Load())
			}
		})
	}
}
