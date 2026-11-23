package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// CatalogMetricCursor identifies the last producer sample returned by a page.
// Continue on the same graph build, metric and filters.
type CatalogMetricCursor struct {
	Source    string          `json:"source"`
	SampleKey json.RawMessage `json:"sample_key"`
}

type CatalogMetricRequest struct {
	MetricID string
	// Source restricts the producer. SampleKey matches the supplied fields of
	// a native key; dates and timestamps retain the original grain.
	Source string
	// DateField selects a native calendar key, defaulting to date. For
	// original_date the calendar prefix is the date recorded by the source.
	DateField string
	FromDate  string
	ToDate    string
	SampleKey json.RawMessage
	After     *CatalogMetricCursor
	Limit     int
}

// CatalogMetricSample retains non-scalar values and provenance. Number uses an
// exact decimal representation; a measured zero/false is distinct from absence.
type CatalogMetricSample struct {
	MetricID       string          `json:"metric_id"`
	Status         string          `json:"value_status"`
	Disposition    string          `json:"disposition"`
	Reason         *string         `json:"reason,omitempty"`
	Definition     json.RawMessage `json:"definition"`
	Binding        json.RawMessage `json:"dbt_binding,omitempty"`
	ReadSources    json.RawMessage `json:"read_sources"`
	Source         *string         `json:"source,omitempty"`
	SampleKey      json.RawMessage `json:"sample_key,omitempty"`
	SourceMetadata json.RawMessage `json:"source_metadata,omitempty"`
	ObservedAt     *string         `json:"original_observation_time,omitempty"`
	AvailableAt    *string         `json:"available_at,omitempty"`
	SourceVersion  *string         `json:"source_version,omitempty"`
	Value          json.RawMessage `json:"value,omitempty"`
	Number         *json.Number    `json:"numeric_value,omitempty"`
	Boolean        *bool           `json:"boolean_value,omitempty"`
	Text           *string         `json:"text_value,omitempty"`
}

type CatalogMetricPage struct {
	Samples []CatalogMetricSample `json:"samples"`
	Next    *CatalogMetricCursor  `json:"next,omitempty"`
}

func catalogSampleKey(key json.RawMessage) bool {
	var obj map[string]json.RawMessage
	return len(key) <= 8192 && json.Unmarshal(key, &obj) == nil && obj != nil
}

// ReadCatalogMetricPage reads the common metric_read_v1 contract for any
// catalog ID installed in an isolated local graph. An unfiltered lookup of a
// known definition with no facts returns its explicit status. A filter with
// no matching sample returns an empty page. The caller cannot select a schema.
func (s *DB) ReadCatalogMetricPage(ctx context.Context, request CatalogMetricRequest) (CatalogMetricPage, error) {
	var page CatalogMetricPage
	if s.profileGraphSchema == "" {
		return page, errors.New("catalog value reads require an isolated local profile graph")
	}
	if request.MetricID == "" || len(request.MetricID) > 256 || len(request.Source) > 256 ||
		request.Limit < 1 || request.Limit > 256 {
		return page, errors.New("catalog read requires a metric ID and a page size of 1 to 256")
	}
	query := `select metric_id,value_status,disposition,reason,definition,dbt_binding,read_sources,
		source,sample_key,source_metadata,original_observation_time,available_at,source_version,
		value,numeric_value::text,boolean_value,text_value from ` + s.profileGraphRelation("metric_read_v1") +
		` where metric_id=$1`
	args := []any{request.MetricID}
	if request.Source != "" {
		args = append(args, request.Source)
		query += fmt.Sprintf(" and source=$%d", len(args))
	}
	dateField := request.DateField
	switch dateField {
	case "":
		dateField = "date"
	case "date", "day", "wake_date", "target_date", "target_day", "effective_date", "anchor", "anchor_date", "anchor_day", "period_day", "original_date", "metric_date", "local_date", "through_day", "start_date", "end_date", "observed_date", "as_of_date", "from_date", "to_date":
	default:
		return page, errors.New("unknown native calendar field")
	}
	dateExpr := ""
	if request.FromDate != "" || request.ToDate != "" {
		args = append(args, dateField)
		field := fmt.Sprintf("(sample_key->>$%d::text)", len(args))
		dateExpr = field
		if dateField == "original_date" {
			// Preserve the producer's day, including an explicit offset.
			// Never normalize this stored key through the database timezone.
			dateExpr = "substring(" + field + " from '^([0-9]{4}-[0-9]{2}-[0-9]{2})(?:$|[ T])')"
		}
	}
	for _, bound := range []struct{ value, operator string }{{request.FromDate, ">="}, {request.ToDate, "<="}} {
		if bound.value == "" {
			continue
		}
		if _, err := time.Parse(time.DateOnly, bound.value); err != nil {
			return page, errors.New("invalid catalog calendar date")
		}
		args = append(args, bound.value)
		query += fmt.Sprintf(" and %s %s $%d", dateExpr, bound.operator, len(args))
	}
	if request.ToDate != "" && request.FromDate > request.ToDate {
		return page, errors.New("invalid catalog date range")
	}
	if request.SampleKey != nil {
		if !catalogSampleKey(request.SampleKey) {
			return page, errors.New("invalid catalog sample key")
		}
		args = append(args, request.SampleKey)
		query += fmt.Sprintf(" and sample_key @> $%d::jsonb", len(args))
	}
	if after := request.After; after != nil {
		if after.Source == "" || len(after.Source) > 256 || !catalogSampleKey(after.SampleKey) {
			return page, errors.New("invalid catalog continuation")
		}
		args = append(args, after.Source, after.SampleKey)
		query += fmt.Sprintf(" and (source,sample_key)>($%d,$%d::jsonb)", len(args)-1, len(args))
	}
	args = append(args, request.Limit+1)
	query += fmt.Sprintf(" order by source,sample_key limit $%d", len(args))
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return page, err
	}
	page.Samples, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (CatalogMetricSample, error) {
		var item CatalogMetricSample
		var number *string
		err := r.Scan(&item.MetricID, &item.Status, &item.Disposition, &item.Reason, &item.Definition,
			&item.Binding, &item.ReadSources, &item.Source, &item.SampleKey, &item.SourceMetadata,
			&item.ObservedAt, &item.AvailableAt, &item.SourceVersion, &item.Value, &number, &item.Boolean, &item.Text)
		if number != nil {
			n := json.Number(*number)
			item.Number = &n
		}
		return item, err
	})
	if err != nil {
		return CatalogMetricPage{}, err
	}
	if len(page.Samples) > request.Limit {
		page.Samples = page.Samples[:request.Limit]
		last := page.Samples[len(page.Samples)-1]
		if last.Source == nil || last.SampleKey == nil {
			return CatalogMetricPage{}, errors.New("invalid sample identity in catalog publication")
		}
		page.Next = &CatalogMetricCursor{Source: *last.Source, SampleKey: last.SampleKey}
	}
	return page, nil
}
