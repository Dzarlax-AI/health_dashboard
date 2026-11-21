package main

import (
	"bytes"
	"strings"
	"testing"

	"health-receiver/internal/storage"
)

func TestNapEnergyPreview(t *testing.T) {
	for _, tt := range []struct {
		name, status, text, score string
		before, after, delta      int
		reason                    storage.EnergyNapDisplayReason
	}{
		{name: "visible increase", status: "compared", before: 44, after: 47, delta: 3, reason: storage.NapCreditShown,
			text: "повысил показание банка на 3 балла", score: "Банк без прибавки 44 · с прибавкой 47"},
		{name: "already at maximum", status: "compared", before: 100, after: 100, reason: storage.NapAtCeiling,
			text: "На экране уже максимальное значение", score: "Банк без прибавки 100 · с прибавкой 100"},
		{name: "recovery stays below zero", status: "compared", before: 0, after: 0, reason: storage.NapBelowDisplayFloor,
			text: "На экране ещё 0", score: "Банк без прибавки 0 · с прибавкой 0"},
		{name: "confirmation too late", status: "source_available_after_window", text: "после окончания окна восстановления"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result := storage.EnergyNapShadowResult{Status: tt.status}
			if tt.status == "compared" {
				result.DisplayBefore = &tt.before
				result.Gradual = &storage.EnergyNapShadowVariant{
					DisplayAfter: tt.after, VisibleDelta: tt.delta, Reason: tt.reason, PointsBeforeLimit: 3.2,
				}
			}
			var out bytes.Buffer
			if err := renderNapEnergyPreview(&out, result); err != nil {
				t.Fatal(err)
			}
			page := out.String()
			if !strings.Contains(page, tt.text) ||
				(tt.score != "" && !strings.Contains(page, tt.score)) ||
				(tt.score == "" && strings.Contains(page, "Банк без прибавки")) {
				t.Fatal("preview did not distinguish the outcome")
			}
		})
	}
}

func TestNapEnergyPreviewFlagRequiresComparison(t *testing.T) {
	if err := run([]string{"--nap-energy-html"}, nil); err == nil ||
		!strings.Contains(err.Error(), "--compare-nap-energy-at") {
		t.Fatal("preview flag must be scoped to the local nap comparison")
	}
}
