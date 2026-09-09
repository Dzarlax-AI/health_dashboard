package storage

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func validWakeMetric() DerivedMetric {
	wake := time.Date(2026, 8, 5, 8, 3, 0, 0, time.FixedZone("CEST", 2*60*60))
	return DerivedMetric{
		MetricName:     DerivedMetricWakeTime,
		MetricDate:     "2026-08-05",
		ValueType:      DerivedValueTimestamp,
		ValueTimestamp: &wake,
		Unit:           "timestamp",
		State:          DerivedMetricStateProvisional,
		FormulaVersion: "wake-v1",
		InputsHash:     strings.Repeat("a", 64),
		Metadata:       json.RawMessage(`{"confidence":"medium"}`),
	}
}

func TestValidateDerivedMetricAcceptsRegisteredTypedValue(t *testing.T) {
	if err := ValidateDerivedMetric(validWakeMetric()); err != nil {
		t.Fatalf("valid wake metric: %v", err)
	}
}

func TestValidateDerivedMetricRejectsUnknownMetric(t *testing.T) {
	metric := validWakeMetric()
	metric.MetricName = "typo_wake"
	if err := ValidateDerivedMetric(metric); err == nil || !strings.Contains(err.Error(), "unknown derived metric") {
		t.Fatalf("unknown metric error=%v", err)
	}
}

func TestValidateDerivedMetricRejectsWrongTypeOrMultipleValues(t *testing.T) {
	t.Run("wrong registry type", func(t *testing.T) {
		metric := validWakeMetric()
		metric.ValueType = DerivedValueNumber
		if err := ValidateDerivedMetric(metric); err == nil || !strings.Contains(err.Error(), "value type") {
			t.Fatalf("wrong type error=%v", err)
		}
	})
	t.Run("multiple values", func(t *testing.T) {
		metric := validWakeMetric()
		n := 8.0
		metric.ValueNumeric = &n
		if err := ValidateDerivedMetric(metric); err == nil || !strings.Contains(err.Error(), "exactly one") {
			t.Fatalf("multiple values error=%v", err)
		}
	})
}

func TestValidateDerivedMetricRejectsInvalidDateStateAndMetadata(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*DerivedMetric)
	}{
		{"date", func(metric *DerivedMetric) { metric.MetricDate = "05-08-2026" }},
		{"state", func(metric *DerivedMetric) { metric.State = "done" }},
		{"metadata", func(metric *DerivedMetric) { metric.Metadata = json.RawMessage(`{`) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			metric := validWakeMetric()
			tc.mutate(&metric)
			if err := ValidateDerivedMetric(metric); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestDerivedMetricsDDLHasCanonicalKeysAndTypedValueChecks(t *testing.T) {
	ddl := strings.Join(strings.Fields(derivedMetricsTableDDL()), " ")
	for _, fragment := range []string{
		"PRIMARY KEY (metric_name, metric_date)",
		"value_timestamp TIMESTAMPTZ",
		"value_json JSONB",
		"= 1",
		"value_type = 'timestamp'",
	} {
		if !strings.Contains(ddl, fragment) {
			t.Errorf("derived metrics DDL missing %q", fragment)
		}
	}
	feedbackDDL := strings.Join(strings.Fields(derivedMetricFeedbackTableDDL()), " ")
	if !strings.Contains(feedbackDDL, "PRIMARY KEY (metric_name, metric_date, channel)") {
		t.Fatal("feedback DDL does not enforce one prompt/answer per metric date and channel")
	}
}

func TestSaveDerivedMetricCanMergeMetadataAtomically(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	first := validWakeMetric()
	first.Metadata = json.RawMessage(`{"subjective_checkin_answered_at":"2026-08-05T06:40:00Z","confidence":"low"}`)
	if err := db.SaveDerivedMetric(first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Metadata = json.RawMessage(`{"confidence":"high","reason":"post_wake_activity"}`)
	second.MergeMetadata = true
	if err := db.SaveDerivedMetric(second); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetDerivedMetric(first.MetricName, first.MetricDate)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(got.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["subjective_checkin_answered_at"] != "2026-08-05T06:40:00Z" ||
		metadata["confidence"] != "high" || metadata["reason"] != "post_wake_activity" {
		t.Fatalf("merged metadata=%v", metadata)
	}
}

func TestValidateDerivedMetricFeedbackResponseDispatchesByMetric(t *testing.T) {
	if err := ValidateDerivedMetricFeedbackResponse(DerivedMetricWakeTime, WakeFeedbackConfirmed); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDerivedMetricFeedbackResponse(DerivedMetricWakeTime, "bogus"); err == nil {
		t.Fatal("accepted unsupported wake feedback")
	}
	if err := ValidateDerivedMetricFeedbackResponse("future_metric", WakeFeedbackConfirmed); err == nil {
		t.Fatal("accepted unknown derived metric")
	}
}

func TestSaveDerivedMetricUpsertsCanonicalValueAndPreservesFirstFinalization(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	first := validWakeMetric()
	first.State = DerivedMetricStateFinal
	finalized := time.Date(2026, 8, 5, 8, 20, 0, 0, time.UTC)
	first.FinalizedAt = &finalized
	if err := db.SaveDerivedMetric(first); err != nil {
		t.Fatal(err)
	}

	laterWake := first.ValueTimestamp.Add(45 * time.Minute)
	second := first
	second.ValueTimestamp = &laterWake
	second.State = DerivedMetricStateProvisional
	second.InputsHash = strings.Repeat("b", 64)
	secondFinalized := finalized.Add(time.Hour)
	second.FinalizedAt = &secondFinalized
	if err := db.SaveDerivedMetric(second); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetDerivedMetric(DerivedMetricWakeTime, first.MetricDate)
	if err != nil {
		t.Fatal(err)
	}
	if got.ValueTimestamp == nil || !got.ValueTimestamp.Equal(laterWake) {
		t.Fatalf("wake value=%v, want %v", got.ValueTimestamp, laterWake)
	}
	if got.State != DerivedMetricStateFinal {
		t.Fatalf("state=%q, want final", got.State)
	}
	if got.FinalizedAt == nil || !got.FinalizedAt.Equal(finalized) {
		t.Fatalf("finalized_at=%v, want first %v", got.FinalizedAt, finalized)
	}
}

func TestDerivedMetricFeedbackPreservesFirstAnswer(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	messageID := int64(42)
	inserted, err := db.SaveDerivedMetricFeedbackPrompted(DerivedMetricFeedback{
		MetricName:      DerivedMetricWakeTime,
		MetricDate:      "2026-08-05",
		Channel:         DerivedMetricFeedbackTelegram,
		ProposedValue:   json.RawMessage(`"2026-08-05T08:03:00+02:00"`),
		PromptMessageID: &messageID,
		PromptedAt:      time.Date(2026, 8, 5, 8, 30, 0, 0, time.UTC),
	})
	if err != nil || !inserted {
		t.Fatalf("save prompt inserted=%v err=%v", inserted, err)
	}
	if inserted, err := db.SaveDerivedMetricFeedbackPrompted(DerivedMetricFeedback{
		MetricName:    DerivedMetricWakeTime,
		MetricDate:    "2026-08-05",
		Channel:       DerivedMetricFeedbackTelegram,
		ProposedValue: json.RawMessage(`"different"`),
		PromptedAt:    time.Now(),
	}); err != nil || inserted {
		t.Fatalf("duplicate prompt inserted=%v err=%v", inserted, err)
	}

	first, err := db.SaveDerivedMetricFeedbackAnswer(
		DerivedMetricWakeTime, "2026-08-05", DerivedMetricFeedbackTelegram,
		WakeFeedbackConfirmed, nil, time.Date(2026, 8, 5, 8, 31, 0, 0, time.UTC),
	)
	if err != nil || first != WakeFeedbackConfirmed {
		t.Fatalf("first answer=%q err=%v", first, err)
	}
	second, err := db.SaveDerivedMetricFeedbackAnswer(
		DerivedMetricWakeTime, "2026-08-05", DerivedMetricFeedbackTelegram,
		WakeFeedbackLater, nil, time.Date(2026, 8, 5, 8, 32, 0, 0, time.UTC),
	)
	if err != nil || second != WakeFeedbackConfirmed {
		t.Fatalf("repeat answer=%q err=%v, want first answer", second, err)
	}
}
