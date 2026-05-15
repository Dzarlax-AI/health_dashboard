package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// callCatalogue runs sectionsCatalogue handler against a synthetic
// request with the given lang query — no DB needed (the handler is
// pure config + i18n lookup).
func callCatalogue(t *testing.T, lang string) map[string]any {
	t.Helper()
	url := "/api/sections"
	if lang != "" {
		url += "?lang=" + lang
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	(&Handler{}).sectionsCatalogue(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	return body
}

func TestSectionsCatalogue_EnglishHappyPath(t *testing.T) {
	body := callCatalogue(t, "en")
	sections, ok := body["sections"].([]any)
	if !ok || len(sections) != 3 {
		t.Fatalf("want 3 sections, got %v", body["sections"])
	}
	wantKeys := []string{"cardio", "activity", "recovery"}
	for i, raw := range sections {
		s := raw.(map[string]any)
		if s["key"] != wantKeys[i] {
			t.Errorf("entry %d key = %q, want %q", i, s["key"], wantKeys[i])
		}
		if s["title"] == "" || s["subtitle"] == "" {
			t.Errorf("entry %d has empty title/subtitle: %+v", i, s)
		}
		if s["icon"] == "" {
			t.Errorf("entry %d has empty icon: %+v", i, s)
		}
	}
}

func TestSectionsCatalogue_LocalizedRussian(t *testing.T) {
	body := callCatalogue(t, "ru")
	sections := body["sections"].([]any)
	cardio := sections[0].(map[string]any)
	// Russian title for the cardio section.
	if cardio["title"] != "Сердце и лёгкие" {
		t.Errorf("ru cardio title = %q, want \"Сердце и лёгкие\"", cardio["title"])
	}
}

func TestSectionsCatalogue_LocalizedSerbian(t *testing.T) {
	body := callCatalogue(t, "sr")
	sections := body["sections"].([]any)
	activity := sections[1].(map[string]any)
	if activity["title"] != "Aktivnost" {
		t.Errorf("sr activity title = %q, want \"Aktivnost\"", activity["title"])
	}
}

func TestSectionsCatalogue_MissingLangFallsBackToEnglish(t *testing.T) {
	body := callCatalogue(t, "")
	sections := body["sections"].([]any)
	cardio := sections[0].(map[string]any)
	// supportedLang clamps empty to "en".
	if cardio["title"] != "Cardio" {
		t.Errorf("empty lang should fall back to en (Cardio), got %q", cardio["title"])
	}
}

func TestSectionsCatalogue_UnknownLangFallsBackToEnglish(t *testing.T) {
	body := callCatalogue(t, "fr_FR_invalid")
	sections := body["sections"].([]any)
	cardio := sections[0].(map[string]any)
	if cardio["title"] != "Cardio" {
		t.Errorf("unknown lang should fall back to en (Cardio), got %q", cardio["title"])
	}
}

func TestSectionsCatalogue_IconStaysStable(t *testing.T) {
	// Icon is design semantics, not translation — must be identical
	// across all languages. Catches future drift where someone
	// accidentally moves an icon mapping into the i18n table.
	en := callCatalogue(t, "en")["sections"].([]any)
	ru := callCatalogue(t, "ru")["sections"].([]any)
	sr := callCatalogue(t, "sr")["sections"].([]any)
	for i := range en {
		ei := en[i].(map[string]any)["icon"]
		ri := ru[i].(map[string]any)["icon"]
		si := sr[i].(map[string]any)["icon"]
		if ei != ri || ei != si {
			t.Errorf("icon[%d] drifts across languages: en=%v ru=%v sr=%v", i, ei, ri, si)
		}
	}
}
