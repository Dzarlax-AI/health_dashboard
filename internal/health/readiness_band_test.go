package health

import "testing"

func TestReadinessBand_Thresholds(t *testing.T) {
	cases := []struct {
		score int
		want  string
	}{
		// "optimal" — boundary checks at 80
		{100, "optimal"},
		{85, "optimal"},
		{80, "optimal"},

		// "fair" — boundary checks at 80/50
		{79, "fair"},
		{65, "fair"},
		{50, "fair"},

		// "low" — boundary checks at 50 and zero/negative defensively
		{49, "low"},
		{25, "low"},
		{0, "low"},
		{-5, "low"}, // defensive — should not happen in practice
	}
	for _, c := range cases {
		if got := ReadinessBand(c.score); got != c.want {
			t.Errorf("ReadinessBand(%d) = %q, want %q", c.score, got, c.want)
		}
	}
}

// readinessLabelTip is the localized rendering of the same band — verify
// they don't drift. If someone updates ReadinessBand thresholds without
// updating readinessLabelTip (or vice versa), label and band would
// disagree and iOS would render conflicting chip + headline text.
func TestReadinessBand_AlignsWithLabel(t *testing.T) {
	ls := GetStrings("en")
	cases := []struct {
		score     int
		wantBand  string
		wantLabel string
	}{
		{90, "optimal", "Optimal"},
		{60, "fair", "Fair"},
		{30, "low", "Low"},
	}
	for _, c := range cases {
		band := ReadinessBand(c.score)
		label, _ := readinessLabelTip(c.score, ls)
		if band != c.wantBand {
			t.Errorf("score=%d band=%q want %q", c.score, band, c.wantBand)
		}
		if label != c.wantLabel {
			t.Errorf("score=%d label=%q want %q", c.score, label, c.wantLabel)
		}
	}
}
