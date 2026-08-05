package ai

import (
	"context"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

const (
	maxHTTPAttempts = 3
	maxRetryDelay   = 5 * time.Second
)

type requestBuilder func() (*http.Request, error)

func doRequestWithRetry(ctx context.Context, client *http.Client, build requestBuilder) (*http.Response, int, time.Duration, error) {
	started := time.Now()
	for attempt := 1; attempt <= maxHTTPAttempts; attempt++ {
		req, err := build()
		if err != nil {
			return nil, attempt, time.Since(started), err
		}
		resp, err := client.Do(req)
		if err == nil && (!retryableHTTPStatus(resp.StatusCode) || attempt == maxHTTPAttempts) {
			return resp, attempt, time.Since(started), nil
		}
		if err != nil && ctx.Err() != nil {
			return nil, attempt, time.Since(started), ctx.Err()
		}
		if err != nil && attempt == maxHTTPAttempts {
			return nil, attempt, time.Since(started), err
		}
		var retryAfter time.Duration
		if resp != nil {
			retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			_ = resp.Body.Close()
		}
		if retryAfter <= 0 {
			base := 200 * time.Millisecond
			retryAfter = base*time.Duration(1<<(attempt-1)) +
				time.Duration(rand.Intn(100))*time.Millisecond
		}
		if retryAfter > maxRetryDelay {
			retryAfter = maxRetryDelay
		}
		timer := time.NewTimer(retryAfter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, attempt, time.Since(started), ctx.Err()
		case <-timer.C:
		}
	}
	panic("unreachable")
}

func retryableHTTPStatus(status int) bool {
	return status == http.StatusRequestTimeout ||
		status == http.StatusTooManyRequests ||
		status >= http.StatusInternalServerError
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}
