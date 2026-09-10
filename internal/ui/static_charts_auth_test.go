package ui

import (
	"strings"
	"testing"
)

func TestLegacyChartsRecoverUnauthorizedAPIResponses(t *testing.T) {
	asset, err := staticFS.ReadFile("static/charts.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(asset)

	if !strings.Contains(source, "function healthAPIResponse(response)") {
		t.Fatal("legacy charts must centralize API response handling")
	}
	if !strings.Contains(source, "'/auth/session?next=' + encodeURIComponent(next)") {
		t.Fatal("legacy charts must navigate through the protected session recovery endpoint")
	}
	if strings.Contains(source, "return r.json()") {
		t.Fatal("legacy charts must not parse an unauthorized response as chart data")
	}
}
