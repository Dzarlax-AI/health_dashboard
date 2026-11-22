package storage

import "testing"

func TestSleepSourceTieFallback(t *testing.T) {
	for _, tc := range []struct {
		name  string
		hours map[string]float64
		wake  string
		sri   string
	}{
		{"watch tie", map[string]float64{"Ä Ultra": 2, "Z Ultra": 2, "RingConn": 8}, "Z Ultra", "Z Ultra"},
		{"ring tie", map[string]float64{"Ä RingConn": 3, "Z RingConn": 3, "Other": 8}, "Z RingConn", "Other"},
		{"fallback tie", map[string]float64{"Ä": 3, "Z": 3}, "Z", "Z"},
		{"higher total", map[string]float64{"A Ultra": 2, "Z Ultra": 3}, "Z Ultra", "Z Ultra"},
		{"one hour threshold", map[string]float64{"A Ultra": 1, "RingConn": 2}, "RingConn", "RingConn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickWinningSource(tc.hours); got != tc.wake {
				t.Errorf("wake chose %q, want %q", got, tc.wake)
			}
			if got := pickSleepSource(tc.hours, false); got != tc.sri {
				t.Errorf("SRI chose %q, want %q", got, tc.sri)
			}
		})
	}
}
