package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadIngestBodyRejectsOversizePayload(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/health", strings.NewReader(strings.Repeat("x", maxIngestBodyBytes+1)))
	_, err := readIngestBody(httptest.NewRecorder(), req)
	var tooLarge *http.MaxBytesError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("oversize error = %v", err)
	}
}
