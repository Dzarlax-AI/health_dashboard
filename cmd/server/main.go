package main

import (
	"log"
	"net/http"
	"os"

	"health-receiver/internal/handler"
	"health-receiver/internal/mcpserver"
	"health-receiver/internal/storage"
	"health-receiver/internal/ui"
)

func main() {
	dbPath := getEnv("DB_PATH", "/app/data/health.db")
	addr := getEnv("ADDR", ":8080")
	apiKey    := os.Getenv("API_KEY")
	uiPassword := os.Getenv("UI_PASSWORD")
	baseURL   := getEnv("BASE_URL", "http://localhost"+addr)

	db, err := storage.New(dbPath)
	if err != nil {
		log.Fatalf("init db: %v", err)
	}
	defer db.Close()

	mux := http.NewServeMux()
	handler.New(db, apiKey).Register(mux)
	ui.New(db, uiPassword).Register(mux)
	mcpserver.Register(mux, db, baseURL, apiKey)

	logged := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s %s", r.RemoteAddr, r.Method, r.URL.Path)
		mux.ServeHTTP(w, r)
	})

	log.Printf("listening on %s", addr)
	log.Printf("MCP endpoint: %s/mcp", baseURL)
	if err := http.ListenAndServe(addr, logged); err != nil {
		log.Fatalf("server: %v", err)
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
