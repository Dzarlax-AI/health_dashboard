package storage

import (
	"math"
	"testing"
	"time"
)

func TestParseSleepDate(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		wantY   int
		wantM   time.Month
		wantD   int
		wantH   int
		wantMin int
	}{
		{
			name:    "valid date with positive TZ",
			input:   "2026-05-07 23:15:00 +0300",
			wantY:   2026, wantM: time.May, wantD: 7,
			wantH: 23, wantMin: 15,
		},
		{
			name:    "valid date with negative TZ",
			input:   "2026-01-15 06:30:00 -0500",
			wantY:   2026, wantM: time.January, wantD: 15,
			wantH: 6, wantMin: 30,
		},
		{
			name:    "midnight summary (00:00:00) parses fine",
			input:   "2026-05-07 00:00:00 +0000",
			wantY:   2026, wantM: time.May, wantD: 7,
			wantH: 0, wantMin: 0,
		},
		{
			name:    "invalid format",
			input:   "2026-05-07",
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSleepDate(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Year() != tt.wantY || got.Month() != tt.wantM || got.Day() != tt.wantD {
				t.Errorf("date = %v, want %d-%02d-%02d", got, tt.wantY, tt.wantM, tt.wantD)
			}
			if got.Hour() != tt.wantH || got.Minute() != tt.wantMin {
				t.Errorf("time = %02d:%02d, want %02d:%02d", got.Hour(), got.Minute(), tt.wantH, tt.wantMin)
			}
		})
	}
}

func TestPaintMask_SingleDay(t *testing.T) {
	masks := map[string][1440]bool{}
	// 1h sleep starting at 23:00 on 2026-05-07 — fits within one day.
	start := time.Date(2026, 5, 7, 23, 0, 0, 0, time.UTC)
	paintMask(masks, start, 1.0) // 60 minutes

	mask, ok := masks["2026-05-07"]
	if !ok {
		t.Fatal("expected mask for 2026-05-07")
	}
	// 23:00 = minute 1380, should be set for 60 minutes (1380..1439).
	for m := 1380; m < 1440; m++ {
		if !mask[m] {
			t.Errorf("minute %d should be true", m)
		}
	}
	// Minute before should be false.
	if mask[1379] {
		t.Error("minute 1379 should be false")
	}
}

func TestPaintMask_CrossMidnight(t *testing.T) {
	masks := map[string][1440]bool{}
	// 2h sleep starting at 23:30 — crosses midnight into next day.
	start := time.Date(2026, 5, 7, 23, 30, 0, 0, time.UTC)
	paintMask(masks, start, 2.0) // 120 minutes

	// Day 1: 23:30 (minute 1410) to 23:59 (minute 1439) = 30 minutes.
	day1, ok := masks["2026-05-07"]
	if !ok {
		t.Fatal("expected mask for 2026-05-07")
	}
	for m := 1410; m < 1440; m++ {
		if !day1[m] {
			t.Errorf("day1 minute %d should be true", m)
		}
	}
	if day1[1409] {
		t.Error("day1 minute 1409 should be false")
	}

	// Day 2: 00:00 (minute 0) to 01:29 (minute 89) = 90 minutes.
	day2, ok := masks["2026-05-08"]
	if !ok {
		t.Fatal("expected mask for 2026-05-08")
	}
	for m := 0; m < 90; m++ {
		if !day2[m] {
			t.Errorf("day2 minute %d should be true", m)
		}
	}
	if day2[90] {
		t.Error("day2 minute 90 should be false")
	}
}

func TestPaintMask_ZeroHours(t *testing.T) {
	masks := map[string][1440]bool{}
	start := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	paintMask(masks, start, 0.0)
	if len(masks) != 0 {
		t.Error("zero-hour segment should not create any mask entries")
	}
}

func TestPaintMask_ORMerge(t *testing.T) {
	masks := map[string][1440]bool{}
	day := time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)

	// First segment: 01:00-02:00.
	paintMask(masks, day.Add(1*time.Hour), 1.0)
	// Second segment: 01:30-02:30 (overlaps).
	paintMask(masks, day.Add(90*time.Minute), 1.0)

	mask := masks["2026-05-07"]
	// Should cover 01:00 (m=60) through 02:29 (m=149) = 90 minutes total.
	for m := 60; m < 150; m++ {
		if !mask[m] {
			t.Errorf("minute %d should be true after OR-merge", m)
		}
	}
	if mask[59] {
		t.Error("minute 59 should be false")
	}
	if mask[150] {
		t.Error("minute 150 should be false")
	}
}

func TestSortStringsAsc(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"already sorted", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"reverse", []string{"c", "b", "a"}, []string{"a", "b", "c"}},
		{"dates", []string{"2026-05-03", "2026-05-01", "2026-05-02"}, []string{"2026-05-01", "2026-05-02", "2026-05-03"}},
		{"single", []string{"x"}, []string{"x"}},
		{"empty", []string{}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := make([]string, len(tt.in))
			copy(got, tt.in)
			sortStringsAsc(got)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("index %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestSRIFormula verifies the SRI = 200*p - 100 formula with synthetic masks.
func TestSRIFormula(t *testing.T) {
	// Helper: build masks and compute SRI manually (same logic as ComputeSRI
	// post-DB-fetch). This validates the formula without needing a database.
	computeFromMasks := func(masks map[string][1440]bool, days int) (float64, int, bool) {
		sortedDates := make([]string, 0, len(masks))
		for d := range masks {
			sortedDates = append(sortedDates, d)
		}
		sortStringsAsc(sortedDates)
		if len(sortedDates) > days {
			sortedDates = sortedDates[len(sortedDates)-days:]
		}
		if len(sortedDates) < 7 {
			return 0, len(sortedDates), false
		}
		totalPairs := 0
		matchPairs := 0
		for i := 1; i < len(sortedDates); i++ {
			prev := masks[sortedDates[i-1]]
			cur := masks[sortedDates[i]]
			for m := 0; m < 1440; m++ {
				totalPairs++
				if prev[m] == cur[m] {
					matchPairs++
				}
			}
		}
		if totalPairs == 0 {
			return 0, len(sortedDates), false
		}
		p := float64(matchPairs) / float64(totalPairs)
		sri := 200*p - 100
		return sri, len(sortedDates), true
	}

	t.Run("perfectly regular sleep", func(t *testing.T) {
		// Same within-day sleep pattern every day → SRI = 100.
		// Use 01:00-08:00 to avoid cross-midnight boundary effects
		// (cross-midnight creates unequal first/last day masks).
		masks := map[string][1440]bool{}
		for d := 1; d <= 8; d++ {
			start := time.Date(2026, 5, d, 1, 0, 0, 0, time.UTC)
			paintMask(masks, start, 7.0)
		}
		sri, nights, ok := computeFromMasks(masks, 14)
		if !ok {
			t.Fatalf("expected ok=true, got false (nights=%d)", nights)
		}
		if math.Abs(sri-100) > 0.01 {
			t.Errorf("SRI = %.2f, want 100 for perfectly regular sleep", sri)
		}
	})

	t.Run("too few days returns not-ok", func(t *testing.T) {
		masks := map[string][1440]bool{}
		for d := 1; d <= 5; d++ {
			start := time.Date(2026, 5, d, 1, 0, 0, 0, time.UTC)
			paintMask(masks, start, 7.0)
		}
		_, _, ok := computeFromMasks(masks, 14)
		if ok {
			t.Error("expected ok=false for fewer than 7 days")
		}
	})

	t.Run("completely opposite sleep patterns", func(t *testing.T) {
		// Even days: sleep 00:00-12:00. Odd days: sleep 12:00-24:00.
		// Every minute differs between consecutive days → SRI = -100.
		masks := map[string][1440]bool{}
		for d := 1; d <= 8; d++ {
			var mask [1440]bool
			if d%2 == 0 {
				for m := 0; m < 720; m++ {
					mask[m] = true
				}
			} else {
				for m := 720; m < 1440; m++ {
					mask[m] = true
				}
			}
			masks[time.Date(2026, 5, d, 0, 0, 0, 0, time.UTC).Format("2006-01-02")] = mask
		}
		sri, _, ok := computeFromMasks(masks, 14)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if math.Abs(sri-(-100)) > 0.01 {
			t.Errorf("SRI = %.2f, want -100 for perfectly opposite patterns", sri)
		}
	})
}
