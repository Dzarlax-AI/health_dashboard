// UI translation glue. The per-language string tables live in
// i18n_en.go / i18n_ru.go / i18n_sr.go — split per file so each
// language stays readable on its own and diffs don't drag in the
// other languages. New keys land in en (the source of truth) and
// optionally in ru/sr; missing keys fall back to en via T().

package ui

import "net/http"

// translations is the top-level lookup table consumed by T().
// Composed from the per-language maps so each language's strings
// live in its own file.
var translations = map[string]map[string]string{
	"en": translationsEn,
	"ru": translationsRu,
	"sr": translationsSr,
}

// T returns the translation for key in the given language, falling
// back to English when the key is absent (or the language is
// unknown). When even English is missing, returns the key itself —
// surfaces typos quickly in the UI rather than rendering an empty
// span.
func T(lang, key string) string {
	if m, ok := translations[lang]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}
	if m, ok := translations["en"]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}
	return key
}

// MetricName returns a human-friendly metric name in the given
// language. Looks up "metric_<key>"; falls back to the raw key when
// neither the requested language nor English has it.
func MetricName(lang, key string) string {
	v := T(lang, "metric_"+key)
	if v != "metric_"+key {
		return v
	}
	return key
}

// langFromRequest determines the UI language from the request:
// `?lang=` query param wins, then the `lang` cookie, then default
// "en". Values outside the en/ru/sr whitelist are ignored so junk
// inputs can't poison the AI cache or template rendering.
func langFromRequest(r *http.Request) string {
	if q := r.URL.Query().Get("lang"); q == "en" || q == "ru" || q == "sr" {
		return q
	}
	if c, err := r.Cookie("lang"); err == nil {
		if c.Value == "en" || c.Value == "ru" || c.Value == "sr" {
			return c.Value
		}
	}
	return "en"
}
