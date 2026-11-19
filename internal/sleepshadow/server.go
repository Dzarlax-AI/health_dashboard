package sleepshadow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"health-receiver/internal/sleepwire"
)

type legacyAlias = sleepwire.LegacyAlias
type stage = sleepwire.Stage
type candidate = sleepwire.Candidate

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
		if coverage.Valid() && coverage.MetricDate != "" && totals[pointKey{coverage.Source, coverage.MetricDate}] == 1 {
			aliases = append(aliases, coverage.legacyAlias)
		}
	}
	return aliases
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
		date, err := input.WakeDate()
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
