package storage

import (
	"log"
	"time"
)

const readinessRedesignRoutineLookbackDays = 14

type readinessRedesignWriter struct {
	name string
	run  func(from, to string) (int, error)
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

func (s *DB) RunReadinessRedesignBackfillForDatesAt(dates []string, today time.Time) {
	from, to, ok := readinessRedesignRoutineWindow(dates, today)
	if !ok {
		return
	}
	s.runReadinessRedesignBackfillRange(from, to)
}

func (s *DB) runReadinessRedesignBackfillRange(from, to string) {
	writers := []readinessRedesignWriter{
		{name: "recovery_stability", run: s.BackfillRecoveryStabilitySnapshots},
		{name: "passive_efficiency", run: s.BackfillPassiveEfficiencySnapshots},
		{name: "acute_risk", run: s.BackfillAcuteRiskSnapshots},
		{name: "chronic_load", run: s.BackfillChronicLoadSnapshots},
	}
	runReadinessRedesignWriters(from, to, writers)
}

func runReadinessRedesignWriters(from, to string, writers []readinessRedesignWriter) {
	log.Printf("readiness redesign backfill: starting %s..%s", from, to)
	for _, w := range writers {
		if n, err := w.run(from, to); err != nil {
			log.Printf("readiness redesign backfill %s: wrote=%d err=%v", w.name, n, err)
		}
	}
	log.Printf("readiness redesign backfill: done %s..%s", from, to)
}
