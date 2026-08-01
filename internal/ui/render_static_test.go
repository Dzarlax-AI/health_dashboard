package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServeStaticUsesAssetMIMEAndImmutableCache(t *testing.T) {
	tests := []struct {
		path        string
		contentType string
	}{
		{path: "/static/app.js?v=" + StaticVer(), contentType: "application/javascript"},
		{path: "/static/morning-meadow-hero.webp?v=" + StaticVer(), contentType: "image/webp"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, tt.path, nil)
			serveStatic(w, r)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%q", w.Code, w.Body.String())
			}
			if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, tt.contentType) {
				t.Fatalf("Content-Type = %q, want prefix %q", got, tt.contentType)
			}
			if got := w.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
				t.Fatalf("Cache-Control = %q", got)
			}
			if w.Body.Len() == 0 {
				t.Fatal("empty asset body")
			}
		})
	}
}

func TestServeStaticUsesShortCacheForUnversionedAsset(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/static/app.js", nil)
	serveStatic(w, r)
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=3600" {
		t.Fatalf("Cache-Control = %q", got)
	}
}
