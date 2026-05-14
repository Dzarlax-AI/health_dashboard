package health

import (
	"strings"
	"testing"
)

func testLS() LangStrings {
	return LangStrings{
		"energy_reason_illness_signature": "illness reason",
		"energy_reason_recovery_debt":     "recovery debt reason",
		"energy_reason_rebound_addon":     "rebound addon",
	}
}

func TestApplyStressFlagVerdictOverride_IllnessForcesRest(t *testing.T) {
	v, r, over := ApplyStressFlagVerdictOverride(
		"push_hard", "original",
		[]string{"illness_signature", "sustained_load"},
		testLS(),
	)
	if !over {
		t.Fatal("expected override=true")
	}
	if v != "rest" {
		t.Errorf("verdict = %q, want rest", v)
	}
	if r != "illness reason" {
		t.Errorf("reason = %q, want illness reason", r)
	}
}

func TestApplyStressFlagVerdictOverride_IllnessTrumpsRecoveryDebt(t *testing.T) {
	// Both flags present — illness wins (safety-critical authority order).
	v, _, over := ApplyStressFlagVerdictOverride(
		"moderate", "original",
		[]string{"recovery_debt", "illness_signature"},
		testLS(),
	)
	if !over || v != "rest" {
		t.Fatalf("expected rest override, got %q over=%v", v, over)
	}
}

func TestApplyStressFlagVerdictOverride_RecoveryDebtDowngradesPushHard(t *testing.T) {
	v, r, over := ApplyStressFlagVerdictOverride(
		"push_hard", "original",
		[]string{"recovery_debt"},
		testLS(),
	)
	if !over || v != "active_recovery" || r != "recovery debt reason" {
		t.Fatalf("got v=%q r=%q over=%v", v, r, over)
	}
}

func TestApplyStressFlagVerdictOverride_RecoveryDebtDowngradesModerate(t *testing.T) {
	v, _, over := ApplyStressFlagVerdictOverride(
		"moderate", "original",
		[]string{"recovery_debt"},
		testLS(),
	)
	if !over || v != "active_recovery" {
		t.Fatalf("expected active_recovery, got %q", v)
	}
}

func TestApplyStressFlagVerdictOverride_RecoveryDebtLeavesRestAlone(t *testing.T) {
	v, r, over := ApplyStressFlagVerdictOverride(
		"rest", "original",
		[]string{"recovery_debt"},
		testLS(),
	)
	if over {
		t.Errorf("recovery_debt should not override rest, got over=%v", over)
	}
	if v != "rest" || r != "original" {
		t.Errorf("expected verdict+reason unchanged, got %q / %q", v, r)
	}
}

func TestApplyStressFlagVerdictOverride_ReboundEnriches(t *testing.T) {
	v, r, over := ApplyStressFlagVerdictOverride(
		"moderate", "original reason",
		[]string{"parasympathetic_rebound"},
		testLS(),
	)
	if !over {
		t.Fatal("expected override=true (suffix appended)")
	}
	if v != "moderate" {
		t.Errorf("verdict should stay moderate, got %q", v)
	}
	if !strings.Contains(r, "rebound addon") {
		t.Errorf("reason should contain addon, got %q", r)
	}
	if !strings.Contains(r, "original reason") {
		t.Errorf("reason should preserve original, got %q", r)
	}
}

func TestApplyStressFlagVerdictOverride_ReboundIdempotent(t *testing.T) {
	// Calling twice must not double-append (briefing pipeline may
	// recompute after a snapshot reload).
	_, r1, _ := ApplyStressFlagVerdictOverride(
		"moderate", "original",
		[]string{"parasympathetic_rebound"},
		testLS(),
	)
	_, r2, over := ApplyStressFlagVerdictOverride(
		"moderate", r1,
		[]string{"parasympathetic_rebound"},
		testLS(),
	)
	if over {
		t.Errorf("second call should not re-override, got over=%v", over)
	}
	if r1 != r2 {
		t.Errorf("reason changed on second call: %q -> %q", r1, r2)
	}
}

func TestApplyStressFlagVerdictOverride_NoFlags(t *testing.T) {
	v, r, over := ApplyStressFlagVerdictOverride(
		"moderate", "original", nil, testLS(),
	)
	if over || v != "moderate" || r != "original" {
		t.Errorf("expected no-op on empty flags, got %q / %q / over=%v", v, r, over)
	}
}

func TestAISuppressPushHard(t *testing.T) {
	tests := []struct {
		name  string
		flags []string
		want  bool
	}{
		{"nil flags", nil, false},
		{"empty flags", []string{}, false},
		{"only acute_stress", []string{"acute_stress"}, false},
		{"only sustained_load", []string{"sustained_load"}, false},
		{"only rebound", []string{"parasympathetic_rebound"}, false},
		{"illness suppresses", []string{"illness_signature"}, true},
		{"recovery_debt suppresses", []string{"recovery_debt"}, true},
		{"mix with illness", []string{"acute_stress", "illness_signature"}, true},
		{"mix with debt", []string{"sustained_load", "recovery_debt"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AISuppressPushHard(tt.flags); got != tt.want {
				t.Errorf("AISuppressPushHard(%v) = %v, want %v", tt.flags, got, tt.want)
			}
		})
	}
}
