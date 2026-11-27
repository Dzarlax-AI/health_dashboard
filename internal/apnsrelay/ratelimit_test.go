package apnsrelay

import (
	"strconv"
	"testing"
	"time"
)

func TestRateLimiterBoundsLiveBucketsAndRetainsHourlyLimits(t *testing.T) {
	now := time.Unix(0, 0)
	limiter := NewRateLimiter(func() time.Time { return now })
	if ok, _ := limiter.Allow("attestation", 1, time.Hour); !ok {
		t.Fatal("first attestation was limited")
	}
	for i := range 9999 {
		if ok, _ := limiter.Allow("delivery:"+strconv.Itoa(i), 1, time.Minute); !ok {
			t.Fatalf("unexpected limit for delivery bucket %d", i)
		}
	}
	if ok, _ := limiter.Allow("overflow", 1, time.Minute); ok {
		t.Fatal("new bucket allowed at capacity")
	}
	now = now.Add(time.Minute)
	if ok, _ := limiter.Allow("new delivery", 1, time.Minute); !ok {
		t.Fatal("expired delivery buckets did not release capacity")
	}
	if ok, _ := limiter.Allow("attestation", 1, time.Hour); ok {
		t.Fatal("delivery cleanup reset the hourly attestation quota")
	}
}
