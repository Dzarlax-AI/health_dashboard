package ui

import (
	"net/http"

	clientapi "health-receiver/internal/api"
	"health-receiver/internal/health"
)

func (h *Handler) sleepDurationComparison(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	date := r.URL.Query().Get("date")
	if date == "" {
		date = db.Today()
	}
	if _, err := health.ParseSleepComparisonDate(date); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	jsonResponse(w, clientapi.SleepDurationComparisonResponse{
		Comparison: db.GetSleepDurationComparison(r.Context(), date),
	})
}
