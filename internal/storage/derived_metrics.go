package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	DerivedMetricWakeTime = "wake_time"

	DerivedValueNumber    = "number"
	DerivedValueText      = "text"
	DerivedValueTimestamp = "timestamp"
	DerivedValueJSON      = "json"

	DerivedMetricStateProvisional = "provisional"
	DerivedMetricStateFinal       = "final"

	DerivedMetricFeedbackTelegram = "telegram"
	SettingWakeFeedbackEnabled    = "wake_feedback_enabled"

	WakeFeedbackConfirmed     = "confirmed"
	WakeFeedbackEarlier       = "earlier"
	WakeFeedbackLater         = "later"
	WakeFeedbackReturnedSleep = "returned_to_sleep"
)

type DerivedMetricDefinition struct {
	ValueType string
	Unit      string
}

var derivedMetricDefinitions = map[string]DerivedMetricDefinition{
	DerivedMetricWakeTime: {
		ValueType: DerivedValueTimestamp,
		Unit:      "timestamp",
	},
}

type DerivedMetric struct {
	MetricName     string
	MetricDate     string
	ValueType      string
	ValueNumeric   *float64
	ValueText      *string
	ValueTimestamp *time.Time
	ValueJSON      json.RawMessage
	Unit           string
	State          string
	FormulaVersion string
	InputsHash     string
	CalculatedAt   time.Time
	FinalizedAt    *time.Time
	Metadata       json.RawMessage
	MergeMetadata  bool
}

type DerivedMetricFeedback struct {
	MetricName      string
	MetricDate      string
	Channel         string
	ProposedValue   json.RawMessage
	Response        string
	CorrectedValue  json.RawMessage
	PromptMessageID *int64
	PromptedAt      time.Time
	AnsweredAt      *time.Time
	Metadata        json.RawMessage
}

func derivedMetricsTableDDL() string {
	return `CREATE TABLE IF NOT EXISTS derived_metrics (
		metric_name     TEXT NOT NULL,
		metric_date     DATE NOT NULL,
		value_type      TEXT NOT NULL,
		value_numeric   DOUBLE PRECISION,
		value_text      TEXT,
		value_timestamp TIMESTAMPTZ,
		value_json      JSONB,
		unit            TEXT NOT NULL DEFAULT '',
		state           TEXT NOT NULL,
		formula_version TEXT NOT NULL,
		inputs_hash     TEXT NOT NULL,
		calculated_at   TIMESTAMPTZ NOT NULL,
		finalized_at    TIMESTAMPTZ,
		metadata        JSONB NOT NULL DEFAULT '{}'::jsonb,
		PRIMARY KEY (metric_name, metric_date),
		CHECK (
			(value_numeric IS NOT NULL)::integer +
			(value_text IS NOT NULL)::integer +
			(value_timestamp IS NOT NULL)::integer +
			(value_json IS NOT NULL)::integer = 1
		),
		CHECK (
			(value_type = 'number' AND value_numeric IS NOT NULL) OR
			(value_type = 'text' AND value_text IS NOT NULL) OR
			(value_type = 'timestamp' AND value_timestamp IS NOT NULL) OR
			(value_type = 'json' AND value_json IS NOT NULL)
		)
	)`
}

func derivedMetricFeedbackTableDDL() string {
	return `CREATE TABLE IF NOT EXISTS derived_metric_feedback (
		metric_name       TEXT NOT NULL,
		metric_date       DATE NOT NULL,
		channel           TEXT NOT NULL,
		proposed_value    JSONB NOT NULL,
		response          TEXT,
		corrected_value   JSONB,
		prompt_message_id BIGINT,
		prompted_at       TIMESTAMPTZ NOT NULL,
		answered_at       TIMESTAMPTZ,
		metadata          JSONB NOT NULL DEFAULT '{}'::jsonb,
		PRIMARY KEY (metric_name, metric_date, channel)
	)`
}

func (s *DB) EnsureDerivedMetricsTables() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.EnsureDerivedMetricsTablesContext(ctx); err != nil {
		log.Printf("EnsureDerivedMetricsTables: %v", err)
	}
}

func (s *DB) EnsureDerivedMetricsTablesContext(ctx context.Context) error {
	for _, ddl := range []string{derivedMetricsTableDDL(), derivedMetricFeedbackTableDDL()} {
		if _, err := s.pool.Exec(ctx, ddl); err != nil {
			return err
		}
	}
	return nil
}

func DerivedMetricDefinitionFor(name string) (DerivedMetricDefinition, bool) {
	def, ok := derivedMetricDefinitions[name]
	return def, ok
}

func ValidateDerivedMetric(metric DerivedMetric) error {
	def, ok := DerivedMetricDefinitionFor(metric.MetricName)
	if !ok {
		return fmt.Errorf("unknown derived metric %q", metric.MetricName)
	}
	if _, err := time.Parse("2006-01-02", metric.MetricDate); err != nil {
		return fmt.Errorf("invalid derived metric date %q: %w", metric.MetricDate, err)
	}
	if metric.ValueType != def.ValueType {
		return fmt.Errorf("derived metric %q value type %q, want %q", metric.MetricName, metric.ValueType, def.ValueType)
	}
	if metric.Unit != def.Unit {
		return fmt.Errorf("derived metric %q unit %q, want %q", metric.MetricName, metric.Unit, def.Unit)
	}
	if metric.State != DerivedMetricStateProvisional && metric.State != DerivedMetricStateFinal {
		return fmt.Errorf("invalid derived metric state %q", metric.State)
	}
	if metric.FormulaVersion == "" || metric.InputsHash == "" {
		return errors.New("derived metric formula_version and inputs_hash are required")
	}
	values := 0
	if metric.ValueNumeric != nil {
		values++
	}
	if metric.ValueText != nil {
		values++
	}
	if metric.ValueTimestamp != nil {
		values++
	}
	if len(metric.ValueJSON) != 0 && string(metric.ValueJSON) != "null" {
		values++
	}
	if values != 1 {
		return fmt.Errorf("derived metric must contain exactly one typed value, got %d", values)
	}
	switch metric.ValueType {
	case DerivedValueNumber:
		if metric.ValueNumeric == nil {
			return errors.New("number derived metric requires value_numeric")
		}
	case DerivedValueText:
		if metric.ValueText == nil {
			return errors.New("text derived metric requires value_text")
		}
	case DerivedValueTimestamp:
		if metric.ValueTimestamp == nil {
			return errors.New("timestamp derived metric requires value_timestamp")
		}
	case DerivedValueJSON:
		if len(metric.ValueJSON) == 0 || string(metric.ValueJSON) == "null" || !json.Valid(metric.ValueJSON) {
			return errors.New("json derived metric requires valid non-null value_json")
		}
	default:
		return fmt.Errorf("invalid derived value type %q", metric.ValueType)
	}
	if len(metric.Metadata) != 0 && !json.Valid(metric.Metadata) {
		return errors.New("derived metric metadata must be valid JSON")
	}
	return nil
}

func (s *DB) SaveDerivedMetric(metric DerivedMetric) error {
	if err := ValidateDerivedMetric(metric); err != nil {
		return err
	}
	if metric.CalculatedAt.IsZero() {
		metric.CalculatedAt = time.Now()
	}
	metadata := metric.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	var valueJSON any
	if len(metric.ValueJSON) != 0 {
		valueJSON = json.RawMessage(metric.ValueJSON)
	}
	ctx, cancel := queryCtx()
	defer cancel()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO derived_metrics (
			metric_name, metric_date, value_type,
			value_numeric, value_text, value_timestamp, value_json,
			unit, state, formula_version, inputs_hash,
			calculated_at, finalized_at, metadata
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (metric_name, metric_date) DO UPDATE SET
			value_type = EXCLUDED.value_type,
			value_numeric = EXCLUDED.value_numeric,
			value_text = EXCLUDED.value_text,
			value_timestamp = EXCLUDED.value_timestamp,
			value_json = EXCLUDED.value_json,
			unit = EXCLUDED.unit,
			state = CASE
				WHEN derived_metrics.state = 'final' THEN derived_metrics.state
				ELSE EXCLUDED.state
			END,
			formula_version = EXCLUDED.formula_version,
			inputs_hash = EXCLUDED.inputs_hash,
			calculated_at = EXCLUDED.calculated_at,
			finalized_at = COALESCE(derived_metrics.finalized_at, EXCLUDED.finalized_at),
			metadata = CASE
				WHEN $15 THEN derived_metrics.metadata || EXCLUDED.metadata
				ELSE EXCLUDED.metadata
			END
	`, metric.MetricName, metric.MetricDate, metric.ValueType,
		metric.ValueNumeric, metric.ValueText, metric.ValueTimestamp, valueJSON,
		metric.Unit, metric.State, metric.FormulaVersion, metric.InputsHash,
		metric.CalculatedAt, metric.FinalizedAt, json.RawMessage(metadata), metric.MergeMetadata)
	return err
}

func (s *DB) GetDerivedMetric(metricName, metricDate string) (*DerivedMetric, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	var metric DerivedMetric
	var metadata, valueJSON []byte
	err := s.pool.QueryRow(ctx, `
		SELECT metric_name, TO_CHAR(metric_date, 'YYYY-MM-DD'), value_type,
		       value_numeric, value_text, value_timestamp, value_json,
		       unit, state, formula_version, inputs_hash,
		       calculated_at, finalized_at, metadata
		  FROM derived_metrics
		 WHERE metric_name=$1 AND metric_date=$2
	`, metricName, metricDate).Scan(
		&metric.MetricName, &metric.MetricDate, &metric.ValueType,
		&metric.ValueNumeric, &metric.ValueText, &metric.ValueTimestamp, &valueJSON,
		&metric.Unit, &metric.State, &metric.FormulaVersion, &metric.InputsHash,
		&metric.CalculatedAt, &metric.FinalizedAt, &metadata,
	)
	if err != nil {
		return nil, err
	}
	metric.ValueJSON = json.RawMessage(valueJSON)
	metric.Metadata = json.RawMessage(metadata)
	return &metric, nil
}

func (s *DB) ListDerivedMetrics(metricName, from, to string) ([]DerivedMetric, error) {
	if _, ok := DerivedMetricDefinitionFor(metricName); !ok {
		return nil, fmt.Errorf("unknown derived metric %q", metricName)
	}
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT metric_name, TO_CHAR(metric_date, 'YYYY-MM-DD'), value_type,
		       value_numeric, value_text, value_timestamp, value_json,
		       unit, state, formula_version, inputs_hash,
		       calculated_at, finalized_at, metadata
		  FROM derived_metrics
		 WHERE metric_name=$1 AND metric_date BETWEEN $2 AND $3
		 ORDER BY metric_date
	`, metricName, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DerivedMetric
	for rows.Next() {
		var metric DerivedMetric
		var metadata, valueJSON []byte
		if err := rows.Scan(
			&metric.MetricName, &metric.MetricDate, &metric.ValueType,
			&metric.ValueNumeric, &metric.ValueText, &metric.ValueTimestamp, &valueJSON,
			&metric.Unit, &metric.State, &metric.FormulaVersion, &metric.InputsHash,
			&metric.CalculatedAt, &metric.FinalizedAt, &metadata,
		); err != nil {
			return nil, err
		}
		metric.ValueJSON = json.RawMessage(valueJSON)
		metric.Metadata = json.RawMessage(metadata)
		out = append(out, metric)
	}
	return out, rows.Err()
}

func ValidateWakeFeedbackResponse(response string) error {
	switch response {
	case WakeFeedbackConfirmed, WakeFeedbackEarlier, WakeFeedbackLater, WakeFeedbackReturnedSleep:
		return nil
	default:
		return fmt.Errorf("invalid wake feedback response %q", response)
	}
}

func ValidateDerivedMetricFeedbackResponse(metricName, response string) error {
	switch metricName {
	case DerivedMetricWakeTime:
		return ValidateWakeFeedbackResponse(response)
	default:
		return fmt.Errorf("unknown derived metric %q", metricName)
	}
}

func IsWakeFeedbackEnabled(s *DB) bool {
	return getSettingBool(s, SettingWakeFeedbackEnabled, true)
}

func validateDerivedMetricFeedbackIdentity(metricName, metricDate, channel string) error {
	if _, ok := DerivedMetricDefinitionFor(metricName); !ok {
		return fmt.Errorf("unknown derived metric %q", metricName)
	}
	if _, err := time.Parse("2006-01-02", metricDate); err != nil {
		return fmt.Errorf("invalid feedback date %q: %w", metricDate, err)
	}
	if channel != DerivedMetricFeedbackTelegram {
		return fmt.Errorf("invalid derived metric feedback channel %q", channel)
	}
	return nil
}

// SaveDerivedMetricFeedbackPrompted records one delivered prompt per
// metric/date/channel. It returns false when another sender already persisted
// the same prompt.
func (s *DB) SaveDerivedMetricFeedbackPrompted(feedback DerivedMetricFeedback) (bool, error) {
	if err := validateDerivedMetricFeedbackIdentity(feedback.MetricName, feedback.MetricDate, feedback.Channel); err != nil {
		return false, err
	}
	if len(feedback.ProposedValue) == 0 || !json.Valid(feedback.ProposedValue) {
		return false, errors.New("feedback proposed_value must be valid JSON")
	}
	if feedback.PromptedAt.IsZero() {
		feedback.PromptedAt = time.Now()
	}
	metadata := feedback.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	if !json.Valid(metadata) {
		return false, errors.New("feedback metadata must be valid JSON")
	}
	ctx, cancel := queryCtx()
	defer cancel()
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO derived_metric_feedback (
			metric_name, metric_date, channel, proposed_value,
			prompt_message_id, prompted_at, metadata
		) VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (metric_name, metric_date, channel) DO NOTHING
	`, feedback.MetricName, feedback.MetricDate, feedback.Channel,
		json.RawMessage(feedback.ProposedValue), feedback.PromptMessageID,
		feedback.PromptedAt, json.RawMessage(metadata))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// SaveDerivedMetricFeedbackAnswer preserves the first answer. Repeated taps
// return the stored response without changing answered_at.
func (s *DB) SaveDerivedMetricFeedbackAnswer(metricName, metricDate, channel, response string, correctedValue json.RawMessage, answeredAt time.Time) (string, error) {
	if err := validateDerivedMetricFeedbackIdentity(metricName, metricDate, channel); err != nil {
		return "", err
	}
	if err := ValidateDerivedMetricFeedbackResponse(metricName, response); err != nil {
		return "", err
	}
	if len(correctedValue) != 0 && !json.Valid(correctedValue) {
		return "", errors.New("feedback corrected_value must be valid JSON")
	}
	if answeredAt.IsZero() {
		answeredAt = time.Now()
	}
	var corrected any
	if len(correctedValue) != 0 {
		corrected = json.RawMessage(correctedValue)
	}
	ctx, cancel := queryCtx()
	defer cancel()
	var stored string
	err := s.pool.QueryRow(ctx, `
		UPDATE derived_metric_feedback
		   SET response=$4, corrected_value=$5, answered_at=$6
		 WHERE metric_name=$1 AND metric_date=$2 AND channel=$3
		   AND response IS NULL
		 RETURNING response
	`, metricName, metricDate, channel, response, corrected, answeredAt).Scan(&stored)
	if err == nil {
		return stored, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	err = s.pool.QueryRow(ctx, `
		SELECT response
		  FROM derived_metric_feedback
		 WHERE metric_name=$1 AND metric_date=$2 AND channel=$3
	`, metricName, metricDate, channel).Scan(&stored)
	return stored, err
}

func (s *DB) GetDerivedMetricFeedback(metricName, metricDate, channel string) (*DerivedMetricFeedback, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	var feedback DerivedMetricFeedback
	var proposed, corrected, metadata []byte
	var response *string
	err := s.pool.QueryRow(ctx, `
		SELECT metric_name, TO_CHAR(metric_date, 'YYYY-MM-DD'), channel,
		       proposed_value, response, corrected_value, prompt_message_id,
		       prompted_at, answered_at, metadata
		  FROM derived_metric_feedback
		 WHERE metric_name=$1 AND metric_date=$2 AND channel=$3
	`, metricName, metricDate, channel).Scan(
		&feedback.MetricName, &feedback.MetricDate, &feedback.Channel,
		&proposed, &response, &corrected, &feedback.PromptMessageID,
		&feedback.PromptedAt, &feedback.AnsweredAt, &metadata,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	feedback.ProposedValue = json.RawMessage(proposed)
	feedback.CorrectedValue = json.RawMessage(corrected)
	feedback.Metadata = json.RawMessage(metadata)
	if response != nil {
		feedback.Response = *response
	}
	return &feedback, nil
}

func (s *DB) HasRecentDerivedMetricFeedback(metricName, channel, beforeDate string, days int) (bool, error) {
	if days < 1 {
		days = 1
	}
	ctx, cancel := queryCtx()
	defer cancel()
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			  FROM derived_metric_feedback
			 WHERE metric_name=$1 AND channel=$2
			   AND metric_date < $3::date
			   AND metric_date >= $3::date - ($4 * INTERVAL '1 day')
		)
	`, metricName, channel, beforeDate, days).Scan(&exists)
	return exists, err
}
