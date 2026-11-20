package main

import (
	"encoding/json"
	"net/http"
	"time"

	"health-receiver/internal/storage"
)

func withLocalReadiness(shadow http.Handler, db *storage.DB, profile string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", shadow)
	mux.HandleFunc("GET /profiles/{profile}/readiness", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("profile") != profile {
			http.NotFound(w, r)
			return
		}
		date := r.URL.Query().Get("date")
		if date != "" {
			if _, err := time.Parse(time.DateOnly, date); err != nil {
				http.Error(w, "invalid readiness date", http.StatusBadRequest)
				return
			}
		}
		readiness, err := db.GetLocalShadowReadiness(r.Context(), profile, date)
		if err != nil {
			http.Error(w, "local readiness storage error", http.StatusInternalServerError)
			return
		}
		if readiness == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(readiness)
	})
	return mux
}

func withLocalEnergyBriefing(shadow http.Handler, db *storage.DB, schema, profile string, clock func() time.Time) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", shadow)
	mux.HandleFunc("GET /api/health-briefing", func(w http.ResponseWriter, r *http.Request) {
		lang := r.URL.Query().Get("lang")
		if lang != "ru" && lang != "sr" {
			lang = "en"
		}
		briefing, err := db.GetLocalShadowEnergyBriefing(r.Context(), schema, profile, lang, clock())
		if err != nil {
			http.Error(w, "local EnergyBank storage error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(briefing)
	})
	return mux
}
