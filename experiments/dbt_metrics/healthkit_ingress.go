package dbtmetrics

import (
	_ "embed"
	"encoding/json"
	"maps"
	"slices"
)

//go:embed models/health_metric_catalog.yml
var metricCatalog []byte

type HealthKitQuantity struct {
	Key                          string   `json:"key"`
	Unit                         string   `json:"unit"`
	LiveGrain                    string   `json:"live_grain"`
	XMLDefaultUnit               string   `json:"xml_default_unit"`
	XMLLegacyKey                 string   `json:"xml_legacy_key"`
	XMLRequiresUnit              bool     `json:"xml_requires_unit"`
	XMLFraction                  bool     `json:"xml_fraction_to_percent"`
	LegacyIDs                    []string `json:"legacy_identifiers"`
	SeparateArchiveSamples       bool     `json:"separate_archive_samples"`
	ArchiveScaleRequiresRevision bool     `json:"archive_scale_requires_revision"`
}

type healthKitCategory struct {
	Key    string            `json:"key"`
	Unit   string            `json:"unit"`
	Mode   string            `json:"mode"`
	Values map[string]string `json:"values"`
}

type HealthKitUnitRule struct {
	Unit       string  `json:"unit"`
	Divisor    float64 `json:"divisor"`
	Multiplier float64 `json:"multiplier"`
}

type healthKitSpec struct {
	observationKeys map[string]json.RawMessage
	Version         int                                     `json:"version"`
	Quantities      map[string]HealthKitQuantity            `json:"quantities"`
	Categories      map[string]healthKitCategory            `json:"categories"`
	Aliases         map[string]string                       `json:"aliases"`
	UnitRules       map[string]map[string]HealthKitUnitRule `json:"unit_rules"`
}

var healthKit = loadHealthKitSpec()
var healthKitReservedKeys = reservedHealthKitKeys()

func reservedHealthKitKeys() map[string]bool {
	result := make(map[string]bool, len(healthKit.observationKeys))
	for key := range healthKit.observationKeys {
		result[key] = true
	}
	for legacy, canonical := range healthKit.Aliases {
		result[legacy], result[canonical] = true, true
	}
	for _, row := range healthKit.Quantities {
		result[row.Key] = true
	}
	for _, row := range healthKit.Categories {
		result[row.Key] = true
		if row.Mode == "sleep_stages" {
			for _, key := range row.Values {
				result[key] = true
			}
		}
	}
	return result
}

func loadHealthKitSpec() healthKitSpec {
	var document struct {
		Models []struct {
			Name   string
			Config struct {
				Meta struct {
					Catalog struct {
						Ingress      healthKitSpec              `json:"healthkit_ingress"`
						Observations map[string]json.RawMessage `json:"observations"`
					} `json:"health_catalog"`
				}
			}
		}
	}
	if err := json.Unmarshal(metricCatalog, &document); err != nil {
		panic("invalid embedded HealthKit catalog: " + err.Error())
	}
	for _, model := range document.Models {
		if model.Name != "semantic_metric_catalog_v1" {
			continue
		}
		spec := model.Config.Meta.Catalog.Ingress
		spec.observationKeys = model.Config.Meta.Catalog.Observations
		if spec.Version != 1 || len(spec.Quantities) == 0 || len(spec.Categories) == 0 {
			break
		}
		for _, id := range slices.Sorted(maps.Keys(spec.Quantities)) {
			row := spec.Quantities[id]
			for _, legacy := range row.LegacyIDs {
				if _, exists := spec.Quantities[legacy]; exists {
					panic("duplicate HealthKit identifier: " + legacy)
				}
				spec.Quantities[legacy] = row
			}
		}
		return spec
	}
	panic("missing embedded HealthKit ingress catalog version 1")
}

// ResolveHealthKitQuantity matches the complete HealthKit type identifier.
func ResolveHealthKitQuantity(identifier string) (HealthKitQuantity, bool) {
	row, ok := healthKit.Quantities[identifier]
	return row, ok
}

// ResolveHealthKitCategory uses the category value only for value-specific types.
func ResolveHealthKitCategory(identifier, value string) (key, unit, mode string) {
	row := healthKit.Categories[identifier]
	if row.Mode == "sleep_stages" {
		return row.Values[value], row.Unit, row.Mode
	}
	if row.Mode == "count" {
		if _, allowed := row.Values[value]; !allowed {
			return "", row.Unit, row.Mode
		}
	}
	return row.Key, row.Unit, row.Mode
}

func HealthKitFallbackKeyAllowed(key string) bool {
	return key != "" && !healthKitReservedKeys[key]
}

func CanonicalHealthKitKey(key string) string {
	if canonical, ok := healthKit.Aliases[key]; ok {
		return canonical
	}
	return key
}

func ResolveHealthKitUnit(key, unit string) (HealthKitUnitRule, bool) {
	rule, ok := healthKit.UnitRules[key][unit]
	return rule, ok
}

func HealthKitAliases() map[string]string {
	return maps.Clone(healthKit.Aliases)
}

func HealthKitUnitRules() map[string]map[string]HealthKitUnitRule {
	result := make(map[string]map[string]HealthKitUnitRule, len(healthKit.UnitRules))
	for metric, rules := range healthKit.UnitRules {
		result[metric] = maps.Clone(rules)
	}
	return result
}

// HealthKitLiveQuantities returns definitions indexed by the canonical wire key.
func HealthKitLiveQuantities() map[string]HealthKitQuantity {
	result := make(map[string]HealthKitQuantity)
	for _, row := range healthKit.Quantities {
		if row.LiveGrain != "" {
			result[row.Key] = row
		}
	}
	return result
}
