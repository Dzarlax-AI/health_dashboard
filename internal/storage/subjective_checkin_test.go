package storage

import (
	"strings"
	"testing"
	"time"
)

// Structural test: the DDL the Ensure function emits must declare the
// columns the rest of the code expects. Reading the SQL is cheap; a
// live DB test would require Postgres in CI which the repo currently
// avoids.
func TestSubjectiveCheckinDDL_HasRequiredColumns(t *testing.T) {
	ddl := subjectiveCheckinTableDDL()
	for _, col := range []string{
		"date              TEXT NOT NULL",
		"source            TEXT NOT NULL",
		"status            TEXT NOT NULL",
		"answer            TEXT",
		"prompt_message_id BIGINT",
		"prompted_at       TIMESTAMPTZ NOT NULL",
		"answered_at       TIMESTAMPTZ",
		"expires_at        TIMESTAMPTZ NOT NULL",
		"metadata          JSONB NOT NULL DEFAULT '{}'::jsonb",
		"PRIMARY KEY (date, source)",
	} {
		if !strings.Contains(ddl, col) {
			t.Errorf("DDL missing %q\n\nfull DDL:\n%s", col, ddl)
		}
	}
}

func TestNextCheckinStatus(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	expiresFuture := now.Add(1 * time.Hour)
	expiresPast := now.Add(-1 * time.Hour)

	cases := []struct {
		name    string
		current string
		action  string
		expires time.Time
		now     time.Time
		want    string
		wantErr bool
	}{
		{"prompted+tap before cap", CheckinStatusPrompted, "tap", expiresFuture, now, CheckinStatusAnswered, false},
		{"prompted+tap after cap", CheckinStatusPrompted, "tap", expiresPast, now, CheckinStatusLateAnswered, false},
		{"expired+tap (very late)", CheckinStatusExpired, "tap", expiresPast, now, CheckinStatusLateAnswered, false},
		{"prompted+expire after cap", CheckinStatusPrompted, "expire", expiresPast, now, CheckinStatusExpired, false},
		{"prompted+expire before cap", CheckinStatusPrompted, "expire", expiresFuture, now, "", true},
		{"answered+tap (idempotent re-tap)", CheckinStatusAnswered, "tap", expiresFuture, now, CheckinStatusAnswered, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nextCheckinStatus(tc.current, tc.action, tc.expires, tc.now)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got status=%q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidateAnswer(t *testing.T) {
	for _, ans := range []string{CheckinAnswerGreat, CheckinAnswerOK, CheckinAnswerMeh, CheckinAnswerSick} {
		if err := ValidateCheckinAnswer(ans); err != nil {
			t.Errorf("answer %q should be valid: %v", ans, err)
		}
	}
	for _, ans := range []string{"", "GREAT", "good", "fine", "amazing"} {
		if err := ValidateCheckinAnswer(ans); err == nil {
			t.Errorf("answer %q should be rejected", ans)
		}
	}
}
