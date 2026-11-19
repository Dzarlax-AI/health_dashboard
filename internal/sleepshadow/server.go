package sleepshadow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type legacyAlias struct {
	SourceEpoch string `json:"source_epoch"`
	Source      string `json:"source"`
	Generation  string `json:"sync_generation"`
}

func (a legacyAlias) valid() bool {
	return a.SourceEpoch != "" && a.Source != "" && a.Generation != ""
}

type stage struct {
	ID    string    `json:"id"`
	Kind  string    `json:"kind"`
	Start time.Time `json:"start_at"`
	End   time.Time `json:"end_at"`
}

type candidate struct {
	ID            string        `json:"id"`
	Revision      int64         `json:"revision"`
	Priority      int           `json:"priority"`
	Zone          string        `json:"wake_zone"`
	Stages        []stage       `json:"stages"`
	ZeroStages    []string      `json:"zero_stages,omitempty"`
	LegacyAliases []legacyAlias `json:"legacy_aliases"`
}

type legacyPacket struct {
	Data *struct {
		Coverage []struct {
			legacyAlias
			MetricDate string `json:"metric_date"`
		} `json:"night_sleep_coverage"`
		Metrics []struct {
			Name string `json:"name"`
			Data []struct {
				Date   string  `json:"date"`
				Source string  `json:"source"`
				Qty    float64 `json:"qty"`
			} `json:"data"`
		} `json:"metrics"`
	} `json:"data"`
}

func (p legacyPacket) boundAliases() []legacyAlias {
	type pointKey struct{ source, date string }
	totals := make(map[pointKey]int)
	for _, metric := range p.Data.Metrics {
		if metric.Name != "night_sleep_total" {
			continue
		}
		for _, point := range metric.Data {
			if point.Qty > 0 {
				totals[pointKey{point.Source, point.Date}]++
			}
		}
	}
	var aliases []legacyAlias
	for _, coverage := range p.Data.Coverage {
		if coverage.valid() && coverage.MetricDate != "" && totals[pointKey{coverage.Source, coverage.MetricDate}] == 1 {
			aliases = append(aliases, coverage.legacyAlias)
		}
	}
	return aliases
}

func (c *candidate) wakeDate() (string, error) {
	if strings.TrimSpace(c.ID) == "" || c.Revision < 1 || c.Priority < 0 || c.Zone == "" || c.Zone == "Local" {
		return "", errors.New("candidate requires id, positive revision, nonnegative priority and explicit wake zone")
	}
	zone, err := time.LoadLocation(c.Zone)
	if err != nil {
		return "", errors.New("invalid wake zone")
	}
	var lastAsleep time.Time
	seen := make(map[string]bool, len(c.Stages))
	observed := make(map[string]bool, 5)
	for i := range c.Stages {
		point := &c.Stages[i]
		if point.ID == "" || seen[point.ID] || point.Start.IsZero() || !point.End.After(point.Start) {
			return "", errors.New("stages require distinct ids and increasing absolute bounds")
		}
		seen[point.ID] = true
		observed[point.Kind] = true
		point.Start, point.End = point.Start.UTC(), point.End.UTC()
		switch point.Kind {
		case "core", "deep", "rem", "asleep_unspecified":
			if point.End.After(lastAsleep) {
				lastAsleep = point.End
			}
		case "awake", "in_bed":
		default:
			return "", errors.New("unknown stage kind")
		}
	}
	if lastAsleep.IsZero() {
		return "", errors.New("candidate requires at least one asleep interval")
	}
	for _, kind := range c.ZeroStages {
		switch kind {
		case "core", "deep", "rem", "asleep_unspecified", "awake":
		default:
			return "", errors.New("unknown zero stage kind")
		}
		if observed[kind] {
			return "", errors.New("zero stage contradicts an observed interval")
		}
	}
	slices.Sort(c.ZeroStages)
	c.ZeroStages = slices.Compact(c.ZeroStages)
	for _, alias := range c.LegacyAliases {
		if !alias.valid() {
			return "", errors.New("incomplete legacy alias")
		}
	}
	slices.SortFunc(c.Stages, func(a, b stage) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(c.LegacyAliases, func(a, b legacyAlias) int {
		if cmp := strings.Compare(a.SourceEpoch, b.SourceEpoch); cmp != 0 {
			return cmp
		}
		if cmp := strings.Compare(a.Source, b.Source); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.Generation, b.Generation)
	})
	c.LegacyAliases = slices.Compact(c.LegacyAliases)
	if len(c.LegacyAliases) == 0 {
		c.LegacyAliases = nil
	}
	return lastAsleep.In(zone).Format(time.DateOnly), nil
}

// New initializes the dedicated shadow schema and returns its local HTTP interface.
// The caller owns the pool and supplies isolated synthetic data.
func New(ctx context.Context, pool *pgxpool.Pool) (http.Handler, error) {
	return NewWithDailyRefresh(ctx, pool, nil)
}

// DailyRefresh binds the transactional refresh to a profile and target schema.
// Pending packets retain the binding so recovery can wait for the same writer.
type DailyRefresh struct {
	Profile   string
	Schema    string
	Apply     func(context.Context, pgx.Tx, []string) error
	Reconcile func(context.Context, pgx.Tx) error
}

// NewWithDailyRefresh also runs the local refresh when replaying pending syncs.
func NewWithDailyRefresh(ctx context.Context, pool *pgxpool.Pool, refresh *DailyRefresh) (http.Handler, error) {
	if refresh != nil && (refresh.Profile == "" || refresh.Schema == "" || refresh.Apply == nil) {
		return nil, errors.New("daily refresh requires a profile, schema and callback")
	}
	if err := initialize(ctx, pool); err != nil {
		return nil, err
	}
	s := store{pool: pool, refresh: refresh}
	if err := s.recoverPending(ctx); err != nil {
		return nil, err
	}
	if refresh != nil && refresh.Reconcile != nil {
		err := s.transaction(ctx, refresh.Profile, func(tx pgx.Tx) error {
			if err := requirePendingRefresh(ctx, tx, refresh.Profile, refresh.Schema); err != nil {
				return err
			}
			return refresh.Reconcile(ctx, tx)
		})
		if err != nil && !errors.Is(err, errRefreshUnavailable) {
			return nil, err
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /profiles/{profile}/v2/sleep", func(w http.ResponseWriter, r *http.Request) {
		var input candidate
		body, err := readBody(w, r)
		if err != nil {
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			http.Error(w, "invalid candidate", http.StatusBadRequest)
			return
		}
		date, err := input.wakeDate()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		status, err := s.sync(r.Context(), r.PathValue("profile"), input, date)
		writeResult(w, status, err)
	})
	mux.HandleFunc("POST /profiles/{profile}/v1/health", func(w http.ResponseWriter, r *http.Request) {
		body, err := readBody(w, r)
		if err != nil {
			return
		}
		var packet legacyPacket
		if err := json.Unmarshal(body, &packet); err != nil || packet.Data == nil {
			http.Error(w, "invalid legacy payload", http.StatusBadRequest)
			return
		}
		status, err := s.legacy(r.Context(), r.PathValue("profile"), body, packet)
		writeResult(w, status, err)
	})
	mux.HandleFunc("GET /profiles/{profile}/daily", func(w http.ResponseWriter, r *http.Request) {
		data, err := s.daily(r.Context(), r.PathValue("profile"))
		if err != nil {
			http.Error(w, "shadow storage error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	})
	return mux, nil
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "cannot read payload", http.StatusBadRequest)
		}
		return nil, err
	}
	if !json.Valid(body) {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return nil, errors.New("invalid JSON")
	}
	return body, nil
}

func writeResult(w http.ResponseWriter, status string, err error) {
	if errors.Is(err, errRefreshUnavailable) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "refresh_unavailable"})
		return
	}
	if err != nil {
		http.Error(w, "shadow storage error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if status == "revision_conflict" {
		w.WriteHeader(http.StatusConflict)
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
}
