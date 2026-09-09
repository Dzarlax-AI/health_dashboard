package ai

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDoRequestWithRetryRetriesOnlyTransientStatuses(t *testing.T) {
	tests := []struct {
		name         string
		firstStatus  int
		wantAttempts int
	}{
		{name: "request timeout", firstStatus: http.StatusRequestTimeout, wantAttempts: 2},
		{name: "rate limited", firstStatus: http.StatusTooManyRequests, wantAttempts: 2},
		{name: "server error", firstStatus: http.StatusServiceUnavailable, wantAttempts: 2},
		{name: "bad request", firstStatus: http.StatusBadRequest, wantAttempts: 1},
		{name: "unauthorized", firstStatus: http.StatusUnauthorized, wantAttempts: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			client := testHTTPClient(func(_ *http.Request) (*http.Response, error) {
				calls++
				status := http.StatusOK
				if calls == 1 {
					status = tt.firstStatus
				}
				resp := jsonResponse(status, `{}`)
				resp.Header.Set("Retry-After", "0")
				return resp, nil
			})
			resp, attempts, _, err := doRequestWithRetry(context.Background(), client, func() (*http.Request, error) {
				return http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.test", strings.NewReader("{}"))
			})
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()
			if attempts != tt.wantAttempts || calls != tt.wantAttempts {
				t.Fatalf("attempts/calls = %d/%d, want %d", attempts, calls, tt.wantAttempts)
			}
		})
	}
}

func TestDoRequestWithRetryHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := testHTTPClient(func(_ *http.Request) (*http.Response, error) {
		cancel()
		return nil, errors.New("temporary network error")
	})
	_, attempts, _, err := doRequestWithRetry(ctx, client, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, mustURLForTest(t, "https://example.test").String(), strings.NewReader("{}"))
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestRetryAfterIsBounded(t *testing.T) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	client := testHTTPClient(func(_ *http.Request) (*http.Response, error) {
		resp := jsonResponse(http.StatusTooManyRequests, `{}`)
		resp.Header.Set("Retry-After", "86400")
		return resp, nil
	})
	_, attempts, _, err := doRequestWithRetry(ctx, client, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test", strings.NewReader("{}"))
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Retry-After was not bounded by caller context: %s", elapsed)
	}
}

func mustURLForTest(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
