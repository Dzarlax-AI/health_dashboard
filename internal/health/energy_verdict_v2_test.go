package health

import "testing"

// Tests for ChooseVerdictV2 and BuildVerdictReasonV2 — the v1→v2
// verdict cutover surface. Coverage targets:
//
//   1. HRV gate authority — when HRV crashes, verdict goes to rest
//      regardless of bank.
//   2. HRV moderate gate — recovery when HRV is mildly low, regardless
//      of bank.
//   3. Bank-derived rest — when HRV is fine but bank is below the
//      rest band.
//   4. Bank-derived recovery — between rest and recovery thresholds.
//   5. push_hard — requires BOTH a high bank AND a green-light HRV;
//      missing either drops to moderate.
//   6. Reason templating routes through the right i18n key per verdict
//      and substitutes the v2 bank value (not v1 current).

func TestChooseVerdictV2_HRVGateAuthority(t *testing.T) {
	// Even with a "push_hard-eligible" bank, HRV ≤ -1.0 SD forces rest.
	bands := VerdictBands{Rest: 15, Recovery: 41, PushHard: 55}
	got := ChooseVerdictV2(-1.2, 90, bands)
	if got != "rest" {
		t.Errorf("HRV crash should force rest regardless of bank=90; got %s", got)
	}
}

func TestChooseVerdictV2_HRVRecoveryGate(t *testing.T) {
	bands := VerdictBands{Rest: 15, Recovery: 41, PushHard: 55}
	// HRV in the lower band but not the rest band, with a healthy bank.
	got := ChooseVerdictV2(-0.7, 80, bands)
	if got != "active_recovery" {
		t.Errorf("HRV in lower band should downgrade healthy bank to recovery; got %s", got)
	}
}

func TestChooseVerdictV2_BankRest(t *testing.T) {
	bands := VerdictBands{Rest: 15, Recovery: 41, PushHard: 55}
	// HRV neutral, bank below rest cutoff.
	got := ChooseVerdictV2(0.0, 10, bands)
	if got != "rest" {
		t.Errorf("bank ≤ rest cutoff should produce rest; got %s", got)
	}
}

func TestChooseVerdictV2_BankRecovery(t *testing.T) {
	bands := VerdictBands{Rest: 15, Recovery: 41, PushHard: 55}
	got := ChooseVerdictV2(0.0, 30, bands)
	if got != "active_recovery" {
		t.Errorf("bank between rest and recovery cutoff should produce recovery; got %s", got)
	}
}

func TestChooseVerdictV2_PushHardRequiresBoth(t *testing.T) {
	bands := VerdictBands{Rest: 15, Recovery: 41, PushHard: 55}
	cases := []struct {
		name      string
		hrvZ      float64
		bank      int
		wantPush  bool
	}{
		{"high bank + green HRV → push", 0.7, 70, true},
		{"high bank + neutral HRV → moderate", 0.0, 70, false},
		{"low bank + green HRV → moderate", 0.7, 50, false},
		{"both at threshold → push", 0.5, 55, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ChooseVerdictV2(c.hrvZ, c.bank, bands)
			isPush := got == "push_hard"
			if isPush != c.wantPush {
				t.Errorf("push_hard=%v, got verdict=%q (hrvZ=%.2f bank=%d)", c.wantPush, got, c.hrvZ, c.bank)
			}
		})
	}
}

func TestChooseVerdictV2_PersonalBandsShiftOutcome(t *testing.T) {
	// A bank of 50 lands in "moderate" under default bands (rest=15, recovery=41)
	// but in "push_hard" under tighter personal bands derived from a sedentary
	// user whose distribution maxes out at ~60. Sanity-check that personal
	// calibration actually changes the output.
	defaults := DefaultV2VerdictBands()
	sedentaryBands := VerdictBands{Rest: 5, Recovery: 25, PushHard: 45}
	if got := ChooseVerdictV2(0.6, 50, defaults); got != "moderate" {
		t.Errorf("default bands at bank=50: want moderate, got %s", got)
	}
	if got := ChooseVerdictV2(0.6, 50, sedentaryBands); got != "push_hard" {
		t.Errorf("sedentary bands at bank=50: want push_hard, got %s", got)
	}
}

func TestBuildVerdictReasonV2_EmbedsBankNotV1Current(t *testing.T) {
	ls := GetStrings("en")
	reason := BuildVerdictReasonV2("active_recovery", 47, 0.0, ls)
	// "47%" must appear since active_recovery uses energy_reason_low_capacity
	// which is "Only %d%% capacity left after today's load".
	if !contains(reason, "47") {
		t.Errorf("reason should embed v2 bank value 47, got: %q", reason)
	}
}

func TestBuildVerdictReasonV2_HighStressBranch(t *testing.T) {
	ls := GetStrings("en")
	// Rest verdict with HRV ≤ rest band → high_stress template branch.
	reason := BuildVerdictReasonV2("rest", 30, -1.2, ls)
	if reason == "" {
		t.Errorf("rest+HRV-crash should produce non-empty reason")
	}
}

func TestDefaultV2VerdictBands_Sane(t *testing.T) {
	b := DefaultV2VerdictBands()
	if !(b.Rest < b.Recovery && b.Recovery < b.PushHard) {
		t.Errorf("default bands must be monotonic; got rest=%d recovery=%d push=%d",
			b.Rest, b.Recovery, b.PushHard)
	}
	if b.Source != "default" {
		t.Errorf("default Source should be 'default', got %q", b.Source)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
