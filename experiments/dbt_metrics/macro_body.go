package dbtmetrics

import "strings"

// Runtime adapters use single-macro files with a one-line declaration.
func macroBody(macro string) string {
	header, body, found := strings.Cut(strings.TrimSpace(macro), "\n")
	body, closed := strings.CutSuffix(strings.TrimSpace(body), "{% endmacro %}")
	if !found || !closed || !strings.HasPrefix(header, "{% macro ") || !strings.HasSuffix(header, "%}") {
		panic("invalid embedded dbt macro declaration")
	}
	return strings.TrimSpace(body)
}
