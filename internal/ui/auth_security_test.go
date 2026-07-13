package ui

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSafeNext(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "/"}, {"/activity?day=1", "/activity?day=1"},
		{"//evil.test/x", "/"}, {"https://evil.test", "/"}, {"relative", "/"}, {"/\\evil", "/"},
	} {
		if got := safeNext(tc.in); got != tc.want {
			t.Errorf("safeNext(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestSetupLimiterIgnoresUsernameVariation(t *testing.T) {
	h := &Handler{ipLimiter: newAttemptLimiter(2, time.Minute, 20)}
	r := httptest.NewRequest("POST", "/setup", nil)
	r.RemoteAddr = "192.0.2.1:1"
	for _, username := range []string{"alpha", "bravo"} {
		_ = username
		if !h.allowSetupAttempt(httptest.NewRecorder(), r) {
			t.Fatal("early setup denial")
		}
	}
	w := httptest.NewRecorder()
	if h.allowSetupAttempt(w, r) {
		t.Fatal("username variation bypassed setup IP bucket")
	}
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("status=%d retry=%q", w.Code, w.Header().Get("Retry-After"))
	}
}

func TestLoginUsernameSprayHitsIPAggregate(t *testing.T) {
	h := &Handler{ipLimiter: newAttemptLimiter(2, time.Minute, 20), accountLimiter: newAttemptLimiter(2, time.Minute, 20)}
	r := httptest.NewRequest("POST", "/login", nil)
	r.RemoteAddr = "192.0.2.2:1"
	if !h.allowLoginIP(httptest.NewRecorder(), r) || !h.allowLoginIP(httptest.NewRecorder(), r) {
		t.Fatal("early login denial")
	}
	w := httptest.NewRecorder()
	if h.allowLoginIP(w, r) {
		t.Fatal("username spray bypassed IP aggregate")
	}
}

func TestSameIPChurnCannotEvictTargetAccountBucket(t *testing.T) {
	h := &Handler{ipLimiter: newAttemptLimiter(2, time.Minute, 4), accountLimiter: newAttemptLimiter(2, time.Minute, 4)}
	r := httptest.NewRequest("POST", "/login", nil)
	r.RemoteAddr = "192.0.2.3:1"
	h.allowLoginAccount(httptest.NewRecorder(), "target")
	h.allowLoginIP(httptest.NewRecorder(), r)
	for i := 0; i < 20; i++ {
		h.allowLoginIP(httptest.NewRecorder(), r)
	}
	if _, ok := h.accountLimiter.entries["target"]; !ok {
		t.Fatal("same-IP churn evicted target account protection")
	}
}

func TestLimiterUsesBoundedLRU(t *testing.T) {
	l := newAttemptLimiter(2, time.Minute, 3)
	now := time.Now()
	for i := 0; i < 100; i++ {
		l.allow(strconv.Itoa(i), now)
	}
	if len(l.entries) != 3 || l.lru.Len() != 3 {
		t.Fatalf("map=%d lru=%d", len(l.entries), l.lru.Len())
	}
}

func TestSuccessfulLoginClearsIPAndAccountBuckets(t *testing.T) {
	h := &Handler{ipLimiter: newAttemptLimiter(2, time.Minute, 4), accountLimiter: newAttemptLimiter(2, time.Minute, 4)}
	r := httptest.NewRequest("POST", "/login", nil)
	r.RemoteAddr = "192.0.2.9:1"
	h.allowLoginIP(httptest.NewRecorder(), r)
	h.allowLoginAccount(httptest.NewRecorder(), "target")
	h.clearLoginLimits(r, "target")
	if len(h.ipLimiter.entries) != 0 || len(h.accountLimiter.entries) != 0 {
		t.Fatal("successful login did not clear throttles")
	}
}

func TestUnknownUsernameChurnUsesFixedBucketAndPreservesTarget(t *testing.T) {
	h := &Handler{accountLimiter: newAttemptLimiter(100, time.Minute, 2)}
	h.allowLoginAccount(httptest.NewRecorder(), "target")
	for i := 0; i < 1000; i++ {
		h.allowLoginAccount(httptest.NewRecorder(), "unknown")
	}
	if len(h.accountLimiter.entries) != 2 {
		t.Fatalf("account buckets=%d", len(h.accountLimiter.entries))
	}
	if _, ok := h.accountLimiter.entries["target"]; !ok {
		t.Fatal("unknown username churn evicted target")
	}
}

func TestSetupTokenConstantTimePolicy(t *testing.T) {
	h := &Handler{setupToken: "capability"}
	if h.validSetupToken("") || h.validSetupToken("wrong") {
		t.Fatal("missing/wrong setup token accepted")
	}
	if !h.validSetupToken("capability") {
		t.Fatal("right setup token rejected")
	}
	h.setupToken = ""
	if h.validSetupToken("") {
		t.Fatal("empty configured setup token must fail closed")
	}
	h.setupToken = "a"
	if h.validSetupToken(strings.Repeat("a", 10000)) {
		t.Fatal("different-length token accepted")
	}
}

func TestSetupAvailableRequiresRegistryWithNoReservations(t *testing.T) {
	tests := []struct {
		name            string
		legacy          bool
		registryPresent bool
		registryEmpty   bool
		want            bool
	}{
		{name: "fresh registry", registryPresent: true, registryEmpty: true, want: true},
		{name: "active user", registryPresent: true},
		{name: "pending or failed reservation", registryPresent: true},
		{name: "missing registry", registryEmpty: true},
		{name: "legacy mode", legacy: true, registryPresent: true, registryEmpty: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := setupAvailable(tt.legacy, tt.registryPresent, tt.registryEmpty); got != tt.want {
				t.Fatalf("setupAvailable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoginLimiterBoundsAndRetry(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newAttemptLimiter(2, time.Minute, 2)
	if ok, _ := l.allow("a", now); !ok {
		t.Fatal("first denied")
	}
	if ok, _ := l.allow("a", now); !ok {
		t.Fatal("second denied")
	}
	if ok, retry := l.allow("a", now); ok || retry <= 0 {
		t.Fatalf("third ok=%v retry=%v", ok, retry)
	}
	l.allow("b", now)
	l.allow("c", now)
	if len(l.entries) > 2 {
		t.Fatalf("entries=%d exceeds bound", len(l.entries))
	}
	if ok, _ := l.allow("a", now.Add(time.Minute)); !ok {
		t.Fatal("window expiry did not permit retry")
	}
}

func TestLoginLimiterCapacityEvictsInsteadOfGloballyDenying(t *testing.T) {
	l := newAttemptLimiter(2, time.Minute, 2)
	now := time.Unix(1000, 0)
	l.allow("attacker-1", now)
	l.allow("attacker-2", now.Add(time.Second))
	if ok, _ := l.allow("legitimate", now.Add(2*time.Second)); !ok {
		t.Fatal("capacity flood globally denied a new key")
	}
	if len(l.entries) > 2 {
		t.Fatalf("entries=%d exceeds bound", len(l.entries))
	}
}

func TestClientIPIgnoresSpoofedForwardingFromUntrustedPeer(t *testing.T) {
	h := &Handler{}
	r := httptest.NewRequest("POST", "/login", nil)
	r.RemoteAddr = "203.0.113.10:1234"
	r.Header.Set("X-Forwarded-For", "198.51.100.9")
	if got := h.clientIP(r); got != "203.0.113.10" {
		t.Fatalf("clientIP=%q", got)
	}
	h.SetTrustedForwardAuthNetworks("203.0.113.0/24")
	if got := h.clientIP(r); got != "198.51.100.9" {
		t.Fatalf("trusted proxy clientIP=%q", got)
	}
}
