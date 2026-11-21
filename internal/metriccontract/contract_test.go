package metriccontract

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestContractSelection(t *testing.T) {
	for _, tt := range []struct {
		name, flag, request string
		ingest              bool
		want                Version
		status              int
	}{
		{"old reader", "", "", false, Legacy, http.StatusOK},
		{"new reader", "", "2", false, Current, http.StatusOK},
		{"unversioned reader after cutover", "0", "", false, Current, http.StatusOK},
		{"explicit legacy reader after cutover", "0", "1", false, "", http.StatusNotAcceptable},
		{"HAE after cutover", "0", "", true, Legacy, http.StatusOK},
		{"new upload after cutover", "0", "2", true, Current, http.StatusOK},
		{"unknown contract", "", "3", true, "", http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HEALTH_METRICS_V1_ENABLED", tt.flag)
			request := httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
			if tt.request != "" {
				request.Header.Set(Header, tt.request)
			}
			response := httptest.NewRecorder()
			negotiate := Read
			if tt.ingest {
				negotiate = Ingest
			}
			got, ok := negotiate(response, request)
			if response.Code != tt.status || ok != (tt.want != "") ||
				Version(response.Header().Get(Header)) != tt.want ||
				(ok && got != tt.want) || response.Header().Get("Vary") != Header {
				t.Fatalf("status=%d version=%q echoed=%q accepted=%t", response.Code, got, response.Header().Get(Header), ok)
			}
		})
	}
}
