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

func TestBuildCheckinCoverage(t *testing.T) {
	prompted := time.Date(2026, 5, 23, 8, 0, 0, 0, time.UTC)
	answered := prompted.Add(45 * time.Second)
	coverage, err := buildCheckinCoverage("2026-05-23", CheckinSourceTelegram, 4, []CheckinRow{
		{
			Date:       "2026-05-23",
			Source:     CheckinSourceTelegram,
			Status:     CheckinStatusAnswered,
			Answer:     CheckinAnswerGreat,
			PromptedAt: prompted,
			AnsweredAt: answered,
			ExpiresAt:  prompted.Add(time.Hour),
		},
		{
			Date:       "2026-05-21",
			Source:     CheckinSourceTelegram,
			Status:     CheckinStatusExpired,
			PromptedAt: prompted.AddDate(0, 0, -2),
			ExpiresAt:  prompted.AddDate(0, 0, -2).Add(time.Hour),
		},
	})
	if err != nil {
		t.Fatalf("buildCheckinCoverage: %v", err)
	}
	if coverage.From != "2026-05-21" || coverage.To != "2026-05-23" {
		t.Fatalf("window = %s..%s, want 2026-05-21..2026-05-23", coverage.From, coverage.To)
	}
	if len(coverage.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(coverage.Rows))
	}
	if coverage.Rows[0].Date != "2026-05-23" || coverage.Rows[0].Status != CheckinStatusAnswered {
		t.Fatalf("row0 = %+v, want 2026-05-23 answered", coverage.Rows[0])
	}
	if coverage.Rows[1].Date != "2026-05-21" || coverage.Rows[1].Status != CheckinStatusExpired {
		t.Fatalf("row1 = %+v, want 2026-05-21 expired", coverage.Rows[1])
	}
	if coverage.Summary.TotalDays != 2 ||
		coverage.Summary.Answered != 1 ||
		coverage.Summary.Expired != 1 ||
		coverage.Summary.Missing != 0 {
		t.Fatalf("summary = %+v, want answered=1 expired=1 missing=0 total=2", coverage.Summary)
	}
	if coverage.Summary.AnswerCounts[CheckinAnswerGreat] != 1 {
		t.Fatalf("answer_counts = %+v, want great=1", coverage.Summary.AnswerCounts)
	}
	if coverage.Summary.AverageResponseSeconds == nil || *coverage.Summary.AverageResponseSeconds != 45 {
		t.Fatalf("avg latency = %v, want 45", coverage.Summary.AverageResponseSeconds)
	}
	if coverage.Summary.AnsweredCoveragePercent != 50 {
		t.Fatalf("answered coverage = %d, want 50", coverage.Summary.AnsweredCoveragePercent)
	}
	if coverage.Summary.PromptedCoveragePercent != 100 {
		t.Fatalf("prompted coverage = %d, want 100", coverage.Summary.PromptedCoveragePercent)
	}
}

func TestBuildCheckinCoverage_LimitsToLatestRows(t *testing.T) {
	prompted := time.Date(2026, 5, 23, 8, 0, 0, 0, time.UTC)
	coverage, err := buildCheckinCoverage("2026-05-23", CheckinSourceTelegram, 2, []CheckinRow{
		{Date: "2026-05-20", Source: CheckinSourceTelegram, Status: CheckinStatusAnswered, Answer: CheckinAnswerGreat, PromptedAt: prompted, AnsweredAt: prompted.Add(time.Second), ExpiresAt: prompted.Add(time.Hour)},
		{Date: "2026-05-23", Source: CheckinSourceTelegram, Status: CheckinStatusAnswered, Answer: CheckinAnswerOK, PromptedAt: prompted, AnsweredAt: prompted.Add(time.Second), ExpiresAt: prompted.Add(time.Hour)},
		{Date: "2026-05-21", Source: CheckinSourceTelegram, Status: CheckinStatusAnswered, Answer: CheckinAnswerMeh, PromptedAt: prompted, AnsweredAt: prompted.Add(time.Second), ExpiresAt: prompted.Add(time.Hour)},
	})
	if err != nil {
		t.Fatalf("buildCheckinCoverage: %v", err)
	}
	if got := []string{coverage.Rows[0].Date, coverage.Rows[1].Date}; got[0] != "2026-05-23" || got[1] != "2026-05-21" {
		t.Fatalf("row dates = %v, want latest two [2026-05-23 2026-05-21]", got)
	}
	if coverage.Summary.TotalDays != 2 || coverage.Summary.Answered != 2 {
		t.Fatalf("summary = %+v, want total=2 answered=2", coverage.Summary)
	}
}
