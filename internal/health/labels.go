package health

// labels.go enriches a BriefingResponse with localized labels for the
// server-side enums that downstream UIs would otherwise have to mirror
// in their own i18n tables (iOS Localizable.xcstrings, web dashboard
// template lookups, etc.). The server is already the single source of
// truth for the strings — these helpers just surface them on the wire
// next to the stable keys so clients don't drift.
//
// Issue #83 spec: every server-defined enum that produces a UI label
// (verdict, status, stress flag, readiness band, …) should ship its
// localized rendering inline. This file covers the three smallest
// items in that list:
//   - BriefingSection.StatusLabel   (good / fair / low → localized)
//   - EnergyBank.VerdictLabel       (push_hard / moderate / …)
//   - EnergyBank.FlagDetails        (stress flag chips)
//
// Readiness band + AI-insight chunking are larger architectural moves
// and ship in separate PRs.

// EnrichLabels walks a fully-built BriefingResponse and populates the
// optional *Label / *Details fields that mirror server enums in the
// caller's language. Called once at the end of the briefing path,
// after every override layer that can change verdicts / statuses /
// flags has settled. Safe to call on nil input — no-ops.
func EnrichLabels(resp *BriefingResponse, ls LangStrings) {
	if resp == nil || ls == nil {
		return
	}
	for i := range resp.Sections {
		resp.Sections[i].StatusLabel = BuildStatusLabel(resp.Sections[i].Status, ls)
	}
	if resp.EnergyBank != nil {
		resp.EnergyBank.VerdictLabel = BuildVerdictLabel(resp.EnergyBank.ActionVerdict, ls)
		resp.EnergyBank.FlagDetails = BuildFlagDetails(resp.EnergyBank.Flags, ls)
	}
}

// BuildStatusLabel looks up "section_status_<status>" in the language
// table. Empty status → empty string (caller's omitempty drops it).
// Unknown status → empty string (don't render anything; do not invent).
func BuildStatusLabel(status string, ls LangStrings) string {
	if status == "" || ls == nil {
		return ""
	}
	return ls["section_status_"+status]
}

// BuildVerdictLabel looks up "energy_verdict_<verdict>" in the language
// table. Empty verdict → empty string. Unknown verdict → empty string.
func BuildVerdictLabel(verdict string, ls LangStrings) string {
	if verdict == "" || ls == nil {
		return ""
	}
	return ls["energy_verdict_"+verdict]
}

// BuildFlagDetails returns one FlagDetail per input flag in the same
// order, preserving the stable Key so consumers can correlate with the
// raw Flags slice. Label and Description come from
// "stress_flag_<key>_label" / "_desc" lookups; if either is missing
// (imputed_* flags or future server-only flags without i18n entries)
// the corresponding field is left empty rather than rendering the raw
// key as a placeholder — clients can detect "no localization yet"
// without painting a useless tile.
//
// Empty / nil input returns nil so the omitempty JSON tag drops the
// field entirely on flag-less days.
func BuildFlagDetails(flags []string, ls LangStrings) []FlagDetail {
	if len(flags) == 0 || ls == nil {
		return nil
	}
	out := make([]FlagDetail, 0, len(flags))
	for _, key := range flags {
		out = append(out, FlagDetail{
			Key:         key,
			Label:       ls["stress_flag_"+key+"_label"],
			Description: ls["stress_flag_"+key+"_desc"],
		})
	}
	return out
}
