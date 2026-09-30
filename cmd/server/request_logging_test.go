package main

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLogRequestsRedactsWebhookSecret(t *testing.T) {
	var output bytes.Buffer
	old := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(old) })
	h := logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, path := range []string{"/api/telegram/webhook/synthetic-secret", "/api/telegram/webhook/synthetic-secret/extra", "/api/telegram//webhook/synthetic-secret", "/api/telegram/x/../webhook/synthetic-secret", "/api/telegram/webhook/synthetic-secret/..", "/health?token=synthetic-query-secret"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, path, nil))
	}
	got := output.String()
	if strings.Contains(got, "synthetic-secret") || strings.Contains(got, "synthetic-query-secret") {
		t.Fatalf("request logger exposed secret: %s", got)
	}
	if strings.Count(got, "/api/telegram/webhook/[redacted]") != 5 || !strings.Contains(got, "POST /health ") {
		t.Fatalf("missing useful request paths: %s", got)
	}
}
