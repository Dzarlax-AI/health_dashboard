package health

import "strings"

// Health Auto Export normalises units to whatever the user has configured in
// the Apple Health app on iOS — for metric users this is already km / kcal /
// degC / m / count·min⁻¹. The helpers below make ingest robust to other
// configurations (US units, kJ energy, mi/h speed) without forcing users to
// switch their app preference.
//
// The "units" string in a HAE qty/units pair follows HealthKit unit
// conventions, e.g. "km", "mi", "degC", "degF", "kcal", "kJ", "km/hr",
// "mi/hr", "m/s", "m", "ft", "%", "count/min". We canonicalise to lowercase
// and trim whitespace before matching.

// NormalizeDistanceKm returns distance in kilometres regardless of input
// unit. Unknown / empty units are assumed to be km (matches the observed
// HAE-on-metric-iOS shape).
func NormalizeDistanceKm(qty float64, units string) float64 {
	switch normUnit(units) {
	case "mi", "mile", "miles":
		return qty * 1.609344
	case "m", "meter", "meters", "metre", "metres":
		return qty / 1000
	case "ft", "foot", "feet":
		return qty * 0.0003048
	default: // "km", ""
		return qty
	}
}

// NormalizeMeters returns elevation / altitude in metres.
func NormalizeMeters(qty float64, units string) float64 {
	switch normUnit(units) {
	case "ft", "foot", "feet":
		return qty * 0.3048
	default: // "m", ""
		return qty
	}
}

// NormalizeSpeedKmh returns speed in km/h. HAE has been observed to emit a
// distance unit ("km") even on speed fields — when we can't pin it down we
// fall back to assuming km/h.
func NormalizeSpeedKmh(qty float64, units string) float64 {
	switch normUnit(units) {
	case "mi/hr", "mph", "mi/h":
		return qty * 1.609344
	case "m/s":
		return qty * 3.6
	default: // "km/hr", "km/h", "km", ""
		return qty
	}
}

// NormalizeEnergyKcal returns energy in kcal.
func NormalizeEnergyKcal(qty float64, units string) float64 {
	switch normUnit(units) {
	case "kj", "kilojoule", "kilojoules":
		return qty * 0.239006
	case "j", "joule", "joules":
		return qty * 0.000239006
	default: // "kcal", "cal", ""
		return qty
	}
}

// NormalizeTempC returns temperature in degrees Celsius.
func NormalizeTempC(qty float64, units string) float64 {
	switch normUnit(units) {
	case "degf", "f":
		return (qty - 32) * 5.0 / 9.0
	default: // "degc", "c", ""
		return qty
	}
}

func normUnit(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
