package health

import "testing"

func TestBuildStatusLabel(t *testing.T) {
	ls := GetStrings("en")
	cases := map[string]string{
		"good": "Good",
		"fair": "Fair",
		"low":  "Low",
	}
	for status, want := range cases {
		if got := BuildStatusLabel(status, ls); got != want {
			t.Errorf("BuildStatusLabel(%q, en) = %q, want %q", status, got, want)
		}
	}
	// Empty / unknown stay empty so omitempty drops them on the wire.
	if got := BuildStatusLabel("", ls); got != "" {
		t.Errorf("BuildStatusLabel(empty) = %q, want \"\"", got)
	}
	if got := BuildStatusLabel("unknown_future_status", ls); got != "" {
		t.Errorf("BuildStatusLabel(unknown) = %q, want \"\"", got)
	}
}

func TestBuildVerdictLabel(t *testing.T) {
	ls := GetStrings("en")
	cases := map[string]string{
		"push_hard":       "Push hard",
		"moderate":        "Moderate day",
		"active_recovery": "Active recovery only",
		"rest":            "Rest day",
	}
	for verdict, want := range cases {
		if got := BuildVerdictLabel(verdict, ls); got != want {
			t.Errorf("BuildVerdictLabel(%q) = %q, want %q", verdict, got, want)
		}
	}
	if got := BuildVerdictLabel("", ls); got != "" {
		t.Errorf("empty verdict should return empty, got %q", got)
	}
}

func TestBuildFlagDetails_KnownFlags(t *testing.T) {
	ls := GetStrings("en")
	flags := []string{"recovery_debt", "acute_stress"}
	got := BuildFlagDetails(flags, ls)
	if len(got) != 2 {
		t.Fatalf("expected 2 details, got %d", len(got))
	}
	if got[0].Key != "recovery_debt" || got[0].Label == "" || got[0].Description == "" {
		t.Errorf("recovery_debt detail incomplete: %+v", got[0])
	}
	if got[1].Key != "acute_stress" || got[1].Label == "" || got[1].Description == "" {
		t.Errorf("acute_stress detail incomplete: %+v", got[1])
	}
}

func TestBuildFlagDetails_UnknownFlag(t *testing.T) {
	ls := GetStrings("en")
	got := BuildFlagDetails([]string{"new_signal_2027"}, ls)
	if len(got) != 1 {
		t.Fatalf("unknown flag should still emit a detail entry, got %d", len(got))
	}
	if got[0].Key != "new_signal_2027" {
		t.Errorf("Key should preserve original, got %q", got[0].Key)
	}
	// Empty Label + Description so clients can detect "no localization
	// yet" and skip rendering rather than painting a key as a label.
	if got[0].Label != "" || got[0].Description != "" {
		t.Errorf("missing i18n entries should yield empty Label/Description, got %+v", got[0])
	}
}

func TestBuildFlagDetails_NilOrEmpty(t *testing.T) {
	ls := GetStrings("en")
	if got := BuildFlagDetails(nil, ls); got != nil {
		t.Errorf("nil flags should yield nil details (omitempty), got %v", got)
	}
	if got := BuildFlagDetails([]string{}, ls); got != nil {
		t.Errorf("empty flags should yield nil details (omitempty), got %v", got)
	}
}

func TestEnrichLabels_PopulatesFields(t *testing.T) {
	resp := &BriefingResponse{
		Sections: []BriefingSection{
			{Key: "sleep", Status: "good"},
			{Key: "activity", Status: "low"},
		},
		EnergyBank: &EnergyBank{
			ActionVerdict: "moderate",
			Flags:         []string{"recovery_debt"},
		},
	}
	EnrichLabels(resp, GetStrings("en"))

	if resp.Sections[0].StatusLabel != "Good" {
		t.Errorf("section status label not enriched: %q", resp.Sections[0].StatusLabel)
	}
	if resp.Sections[1].StatusLabel != "Low" {
		t.Errorf("second section status label not enriched: %q", resp.Sections[1].StatusLabel)
	}
	if resp.EnergyBank.VerdictLabel != "Moderate day" {
		t.Errorf("verdict label not enriched: %q", resp.EnergyBank.VerdictLabel)
	}
	if len(resp.EnergyBank.FlagDetails) != 1 || resp.EnergyBank.FlagDetails[0].Key != "recovery_debt" {
		t.Errorf("flag details not enriched: %+v", resp.EnergyBank.FlagDetails)
	}
}

func TestEnrichLabels_NilResp(t *testing.T) {
	EnrichLabels(nil, GetStrings("en"))
	// no panic = pass
}

func TestEnrichLabels_RussianAndSerbian(t *testing.T) {
	for _, lang := range []string{"ru", "sr"} {
		resp := &BriefingResponse{
			Sections: []BriefingSection{{Status: "good"}},
			EnergyBank: &EnergyBank{
				ActionVerdict: "rest",
				Flags:         []string{"recovery_debt"},
			},
		}
		EnrichLabels(resp, GetStrings(lang))
		if resp.Sections[0].StatusLabel == "" {
			t.Errorf("[%s] status label empty", lang)
		}
		if resp.EnergyBank.VerdictLabel == "" {
			t.Errorf("[%s] verdict label empty", lang)
		}
		if resp.EnergyBank.FlagDetails[0].Label == "" {
			t.Errorf("[%s] flag detail label empty", lang)
		}
	}
}
