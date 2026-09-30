package storage

import (
	"fmt"
	"time"
)

type sourceEpochWindow struct {
	epochID   string
	startDate string
	endDate   *string
}

type sourceEpochResolution struct {
	epochID   string
	startDate string
}

// sourceEpochRunCache snapshots the tenant's confirmed ingest epochs once for
// one sequential readiness backfill. The instance is passed only through that
// run, so it cannot leak data between tenants or outlive catalogue changes.
type sourceEpochRunCache struct {
	windows []sourceEpochWindow
	byDate  map[string]sourceEpochResolution
	db      *DB
}

func (s *DB) loadSourceEpochRunCache() (*sourceEpochRunCache, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT epoch_id, start_date, end_date
		  FROM source_epochs
		 WHERE kind = $1 AND confirmed = TRUE
		 ORDER BY start_date DESC
	`, SourceEpochKindIngest)
	if err != nil {
		return &sourceEpochRunCache{db: s}, nil
	}
	defer rows.Close()

	cache := &sourceEpochRunCache{
		windows: make([]sourceEpochWindow, 0),
		byDate:  make(map[string]sourceEpochResolution),
	}
	for rows.Next() {
		var window sourceEpochWindow
		if err := rows.Scan(&window.epochID, &window.startDate, &window.endDate); err != nil {
			return &sourceEpochRunCache{db: s}, nil
		}
		cache.windows = append(cache.windows, window)
	}
	if err := rows.Err(); err != nil {
		return &sourceEpochRunCache{db: s}, nil
	}
	return cache, nil
}

func (c *sourceEpochRunCache) resolve(date string) (string, string) {
	if c.db != nil {
		// Preserve the historical failure behavior: the public resolver maps
		// query errors to the sentinel and lookupEpochStart maps failures to
		// no clipping. Retry these calls per date rather than caching a failed
		// catalogue read for the rest of the run.
		epoch, _ := c.db.ResolveSourceEpoch(date)
		if epoch == SentinelSourceEpoch {
			return epoch, ""
		}
		return epoch, c.db.lookupEpochStart(epoch)
	}
	if result, ok := c.byDate[date]; ok {
		return result.epochID, result.startDate
	}
	result := sourceEpochResolution{epochID: SentinelSourceEpoch}
	for _, window := range c.windows {
		if window.startDate > date {
			continue
		}
		if window.endDate != nil && *window.endDate < date {
			continue
		}
		result = sourceEpochResolution{epochID: window.epochID}
		if window.epochID != SentinelSourceEpoch {
			result.startDate = window.startDate
		}
		break
	}
	c.byDate[date] = result
	return result.epochID, result.startDate
}

func validateReadinessDateRange(from, to, writer string) error {
	fromT, err := time.Parse(isoDate, from)
	if err != nil {
		return fmt.Errorf("%s: parse from: %w", writer, err)
	}
	toT, err := time.Parse(isoDate, to)
	if err != nil {
		return fmt.Errorf("%s: parse to: %w", writer, err)
	}
	if toT.Before(fromT) {
		return fmt.Errorf("%s: to %q before from %q", writer, to, from)
	}
	return nil
}
