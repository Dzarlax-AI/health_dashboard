package ui

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	clientapi "health-receiver/internal/api"
	"health-receiver/internal/storage"
)

type reportHistoryCursor struct {
	At time.Time `json:"at"`
	ID uuid.UUID `json:"id"`
}

func (h *Handler) reportHistoryList(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	qv := r.URL.Query()
	to := qv.Get("to")
	if to == "" {
		to = tenantLocalToday(h, db, h.tenantSchema(r))
	}
	toDate, err := time.Parse("2006-01-02", to)
	if err != nil {
		jsonError(w, "invalid to date", http.StatusBadRequest)
		return
	}
	from := qv.Get("from")
	if from == "" {
		from = toDate.AddDate(0, 0, -29).Format("2006-01-02")
	}
	fromDate, err := time.Parse("2006-01-02", from)
	if err != nil || fromDate.After(toDate) {
		jsonError(w, "invalid from date", http.StatusBadRequest)
		return
	}
	kind := qv.Get("kind")
	if kind != "" && kind != "morning" && kind != "evening" && kind != "proactive" {
		jsonError(w, "invalid kind", http.StatusBadRequest)
		return
	}
	limit := 20
	if qv.Get("limit") != "" {
		limit, err = strconv.Atoi(qv.Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			jsonError(w, "invalid limit", http.StatusBadRequest)
			return
		}
	}
	q := storage.ReportHistoryQuery{From: from, To: to, Kind: kind, Limit: limit + 1}
	if raw := qv.Get("cursor"); raw != "" {
		data, e := base64.RawURLEncoding.DecodeString(raw)
		var c reportHistoryCursor
		if e != nil || json.Unmarshal(data, &c) != nil || c.At.IsZero() || c.ID == uuid.Nil {
			jsonError(w, "invalid cursor", http.StatusBadRequest)
			return
		}
		q.BeforeAt = &c.At
		q.BeforeID = c.ID
	}
	items, err := db.ListReportHistory(r.Context(), q)
	if err != nil {
		jsonError(w, "failed to load report history", http.StatusInternalServerError)
		return
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		data, _ := json.Marshal(reportHistoryCursor{At: last.PreparedAt, ID: last.ID})
		next = base64.RawURLEncoding.EncodeToString(data)
	}
	jsonResponse(w, clientapi.ReportHistoryResponse{Items: items, NextCursor: next})
}
func (h *Handler) reportHistoryDetail(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	detail, err := h.tenantDB(r).GetReportHistory(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		legacy, legacyErr := h.tenantDB(r).GetLegacyReportHistory(r.Context(), id)
		if errors.Is(legacyErr, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		if legacyErr != nil {
			jsonError(w, "failed to load report", http.StatusInternalServerError)
			return
		}
		jsonResponse(w, legacy)
		return
	}
	if err != nil {
		jsonError(w, "failed to load report", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, detail)
}
