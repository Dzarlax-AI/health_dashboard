package health

import (
	"fmt"
	"math"
)

// stddev returns the population standard deviation of vals.
// Returns 0 if fewer than 2 values are provided.
func stddev(vals []float64) float64 {
	if len(vals) < 2 {
		return 0
	}
	m := avg(vals)
	sum := 0.0
	for _, v := range vals {
		d := v - m
		sum += d * d
	}
	return math.Sqrt(sum / float64(len(vals)))
}

func avg(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

func pctChange(recent, baseline float64) float64 {
	if baseline == 0 {
		return 0
	}
	return (recent - baseline) / baseline * 100
}

func trend(pct float64, invertBetter bool) string {
	if invertBetter {
		pct = -pct
	}
	if pct > 3 {
		return "up"
	}
	if pct < -3 {
		return "down"
	}
	return "stable"
}

func fmtFloat(v float64, decimals int) string {
	if decimals == 0 {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.*f", decimals, v)
}

func roundTo1(v float64) float64 {
	return math.Round(v*10) / 10
}

// clamp01 clamps a float to the [0, 1] interval. Used by Energy Bank when
// converting ratios (e.g. ACWR) into bounded scores.
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// clampScore rounds a float and clamps to the [0, 100] integer range —
// the canonical 0..100 score representation used across the briefing.
func clampScore(v float64) int {
	r := int(math.Round(v))
	if r < 0 {
		return 0
	}
	if r > 100 {
		return 100
	}
	return r
}

func formatNumber(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%d,%03d", n/1000, n%1000)
}
