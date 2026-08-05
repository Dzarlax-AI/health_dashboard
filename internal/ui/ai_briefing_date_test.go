package ui

import "testing"

func TestResolveAIBriefingDate(t *testing.T) {
	today := "2026-08-05"
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "today default", want: today},
		{name: "trimmed historical", raw: " 2026-08-01 ", want: "2026-08-01"},
		{name: "future", raw: "2026-08-06", wantErr: true},
		{name: "invalid format", raw: "01-08-2026", wantErr: true},
		{name: "invalid calendar date", raw: "2026-02-30", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveAIBriefingDate(tc.raw, today)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveAIBriefingDate(%q) = %q, want error", tc.raw, got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("resolveAIBriefingDate(%q) = %q, %v; want %q", tc.raw, got, err, tc.want)
			}
		})
	}
}
