package notify

import (
	"strings"
	"testing"
	"time"

	"health-receiver/internal/health"
)

// TestMorningCapTime_FloorsPastCapsToPromptWindow pins the floor that
// keeps the check-in prompt window alive for users whose adaptive cap
// (typical_wake + 60min) lands earlier than the configured morning
// hour. Without it the smart-retry loop enters past cap on first tick
// and skips MorningActionPrompt entirely — silently disabling
// subjective check-in. See morning_gate.go.
func TestMorningCapTime_FloorsPastCapsToPromptWindow(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Belgrade")
	enter := time.Date(2026, 5, 19, 10, 0, 0, 0, loc)

	t.Run("adaptive cap already past at scheduler entry → floored to now+MinPromptWindow", func(t *testing.T) {
		// User wakes at 07:47, adaptive cap = 08:47. Scheduler enters at
		// morning_weekday=10 → cap is 73 min in the past. Floor must
		// push it to 11:00 so the gate has a prompt window.
		cfg := Config{
			Timezone:           "Europe/Belgrade",
			MorningWeekdayHour: 10,
			TypicalWakeHour:    7,
			TypicalWakeMinute:  47,
			TypicalWakeOK:      true,
		}
		cap := cfg.MorningCapTime(enter)
		want := enter.Add(MinPromptWindow)
		if !cap.Equal(want) {
			t.Fatalf("cap=%s want=%s (floor to now+MinPromptWindow)", cap.Format("15:04"), want.Format("15:04"))
		}
	})

	t.Run("adaptive cap in the future → untouched", func(t *testing.T) {
		// Late riser: wakes at 11:00, cap = 12:00. Scheduler enters at
		// 10:00 → cap is in the future, floor must NOT fire.
		cfg := Config{
			Timezone:           "Europe/Belgrade",
			MorningWeekdayHour: 10,
			TypicalWakeHour:    11,
			TypicalWakeMinute:  0,
			TypicalWakeOK:      true,
		}
		cap := cfg.MorningCapTime(enter)
		want := time.Date(2026, 5, 19, 12, 0, 0, 0, loc)
		if !cap.Equal(want) {
			t.Fatalf("cap=%s want=%s (adaptive cap, untouched)", cap.Format("15:04"), want.Format("15:04"))
		}
	})

	t.Run("static fallback cap in the future → untouched", func(t *testing.T) {
		// No typical-wake data, no MorningCapHour override → default
		// morning_hour+4 = 14:00. Floor must NOT fire.
		cfg := Config{
			Timezone:           "Europe/Belgrade",
			MorningWeekdayHour: 10,
		}
		cap := cfg.MorningCapTime(enter)
		want := time.Date(2026, 5, 19, 14, 0, 0, 0, loc)
		if !cap.Equal(want) {
			t.Fatalf("cap=%s want=%s (static fallback)", cap.Format("15:04"), want.Format("15:04"))
		}
	})

	t.Run("explicit MorningCapHour past at entry → floored", func(t *testing.T) {
		// Operator set report_morning_cap=9 with morning_hour=10. The
		// configured cap fires before the scheduler even wakes — same
		// failure mode as the adaptive path, same floor applies.
		cfg := Config{
			Timezone:           "Europe/Belgrade",
			MorningWeekdayHour: 10,
			MorningCapHour:     9,
		}
		cap := cfg.MorningCapTime(enter)
		want := enter.Add(MinPromptWindow)
		if !cap.Equal(want) {
			t.Fatalf("cap=%s want=%s (explicit cap floored)", cap.Format("15:04"), want.Format("15:04"))
		}
	})
}

// formatMorning renders a soft expired-checkin footer only when the
// cap-path explicitly passes checkinExpired=true. Pins the contract
// promised by the PR description (and the existing checkin_expired_note
// i18n string) so future refactors don't accidentally drop the note.
func TestFormatMorning_AppendsExpiredNote(t *testing.T) {
	briefing := &health.BriefingResponse{Date: "2026-05-18"}
	loc, _ := time.LoadLocation("UTC")

	t.Run("checkinExpired=true → note appears", func(t *testing.T) {
		out := formatMorning(briefing, nil, "ru", loc, freshness{}, true)
		// The RU note text starts with "Хотите". Any future copy change
		// keeps the same key, so look for the i18n marker by checking
		// the key resolves and its prefix lands in the output.
		want := "Хотите"
		if !strings.Contains(out, want) {
			t.Errorf("expired note missing from output:\n%s\n\nwanted substring %q", out, want)
		}
	})

	t.Run("checkinExpired=false → no note", func(t *testing.T) {
		out := formatMorning(briefing, nil, "ru", loc, freshness{}, false)
		if strings.Contains(out, "Хотите") {
			t.Errorf("expired note rendered when checkinExpired=false:\n%s", out)
		}
	})

	t.Run("english locale also renders", func(t *testing.T) {
		out := formatMorning(briefing, nil, "en", loc, freshness{}, true)
		want := "Want the report"
		if !strings.Contains(out, want) {
			t.Errorf("EN expired note missing:\n%s\n\nwanted substring %q", out, want)
		}
	})
}
