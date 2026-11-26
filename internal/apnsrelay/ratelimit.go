package apnsrelay

import (
	"sync"
	"time"
)

type windowBucket struct {
	timestamps []time.Time
}

// RateLimiter implements an exact in-memory sliding-window rate limiter keyed
// by opaque scope strings (e.g. IP or grant_id).
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*windowBucket
	now     func() time.Time
}

func NewRateLimiter(now func() time.Time) *RateLimiter {
	if now == nil {
		now = time.Now
	}
	return &RateLimiter{
		buckets: make(map[string]*windowBucket),
		now:     now,
	}
}

// Allow returns (true, 0) if the key is within limit for the given window, or
// (false, retryAfter) if exceeded.
func (r *RateLimiter) Allow(key string, limit int, window time.Duration) (bool, time.Duration) {
	if r == nil || limit <= 0 || window <= 0 {
		return true, 0
	}
	now := r.now()
	cutoff := now.Add(-window)

	r.mu.Lock()
	defer r.mu.Unlock()

	b := r.buckets[key]
	if b == nil {
		b = &windowBucket{}
		r.buckets[key] = b
	}
	keep := b.timestamps[:0]
	for _, ts := range b.timestamps {
		if ts.After(cutoff) {
			keep = append(keep, ts)
		}
	}
	b.timestamps = keep
	if len(b.timestamps) >= limit {
		retryAfter := b.timestamps[0].Add(window).Sub(now)
		if retryAfter < time.Second {
			retryAfter = time.Second
		}
		return false, retryAfter
	}
	b.timestamps = append(b.timestamps, now)

	// Opportunistic bound on map size.
	if len(r.buckets) > 10000 {
		for k, v := range r.buckets {
			if len(v.timestamps) == 0 || !v.timestamps[len(v.timestamps)-1].After(cutoff) {
				delete(r.buckets, k)
			}
		}
	}
	return true, 0
}
