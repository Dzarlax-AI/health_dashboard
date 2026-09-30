package storage

import (
	"errors"
	"fmt"
	"log"
	"time"
)

const readinessRedesignRoutineLookbackDays = 14

type readinessRedesignWriter struct {
	name string
	run  func(from, to string, epochs *sourceEpochRunCache) (int, error)
}

func readinessRedesignRoutineWindow(dates []string, today time.Time) (string, string, bool) {
	var minDate, maxDate string
	for _, raw := range dates {
		if len(raw) < len(isoDate) {
			continue
		}
		d := raw[:len(isoDate)]
		if _, err := time.Parse(isoDate, d); err != nil {
			continue
		}
		if minDate == "" || d < minDate {
			minDate = d
		}
		if maxDate == "" || d > maxDate {
			maxDate = d
		}
	}
	if minDate == "" {
		return "", "", false
	}

	todayDate := today.Format(isoDate)
	if minDate > todayDate {
		return "", "", false
	}
	maxDate = todayDate
	minT, _ := time.Parse(isoDate, minDate)
	from := minT.AddDate(0, 0, -readinessRedesignRoutineLookbackDays).Format(isoDate)
	return from, maxDate, true
}

// RunReadinessRedesignBackfillForDates refreshes Phase 0 readiness-redesign
// serving rows for tenant-local affected dates. It is intentionally sequential
// per tenant to stay within the same connection-budget discipline as the
// date-aware cache backfill path.
func (s *DB) RunReadinessRedesignBackfillForDates(dates []string) {
	s.RunReadinessRedesignBackfillForDatesAt(dates, time.Now())
}

func (s *DB) RunReadinessRedesignBackfillForDatesAt(dates []string, today time.Time) error {
	from, to, ok := readinessRedesignRoutineWindow(dates, today)
	if !ok {
		return nil
	}
	return s.runReadinessRedesignBackfillRange(from, to)
}

func (s *DB) runReadinessRedesignBackfillRange(from, to string) error {
	epochs, err := s.loadSourceEpochRunCache()
	if err != nil {
		return err
	}
	writers := []readinessRedesignWriter{
		{name: "recovery_stability", run: s.backfillRecoveryStabilitySnapshots},
		{name: "passive_efficiency", run: s.backfillPassiveEfficiencySnapshots},
		{name: "acute_risk", run: s.backfillAcuteRiskSnapshots},
		{name: "chronic_load", run: s.backfillChronicLoadSnapshots},
	}
	return runReadinessRedesignWriters(from, to, epochs, writers)
}

func runReadinessRedesignWriters(from, to string, epochs *sourceEpochRunCache, writers []readinessRedesignWriter) error {
	log.Printf("readiness redesign backfill: starting %s..%s", from, to)
	var errs []error
	for _, w := range writers {
		started := time.Now()
		n, err := w.run(from, to, epochs)
		elapsed := time.Since(started)
		if err != nil {
			log.Printf("readiness redesign backfill %s: days=%d wrote=%d duration=%s err=%v", w.name, readinessRedesignDateCount(from, to), n, elapsed, err)
			errs = append(errs, fmt.Errorf("%s: %w", w.name, err))
		} else {
			log.Printf("readiness redesign backfill %s: days=%d wrote=%d duration=%s", w.name, readinessRedesignDateCount(from, to), n, elapsed)
		}
	}
	log.Printf("readiness redesign backfill: done %s..%s", from, to)
	return errors.Join(errs...)
}

func readinessRedesignDateCount(from, to string) int {
	fromDate, err := time.Parse(isoDate, from)
	if err != nil {
		return 0
	}
	toDate, err := time.Parse(isoDate, to)
	if err != nil || toDate.Before(fromDate) {
		return 0
	}
	return int(toDate.Sub(fromDate)/(24*time.Hour)) + 1
}
