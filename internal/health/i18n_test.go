package health

import (
	"slices"
	"strings"
	"testing"
)

func TestI18nNonEnglishLocalesCoverEnglishKeys(t *testing.T) {
	locales := map[string]LangStrings{
		"ru": ru,
		"sr": sr,
	}

	for lang, stringsByKey := range locales {
		t.Run(lang, func(t *testing.T) {
			var missing []string
			for key := range en {
				if _, ok := stringsByKey[key]; !ok {
					missing = append(missing, key)
				}
			}

			slices.Sort(missing)
			if len(missing) > 0 {
				t.Fatalf("%s is missing %d English i18n key(s): %s", lang, len(missing), strings.Join(missing, ", "))
			}
		})
	}
}
