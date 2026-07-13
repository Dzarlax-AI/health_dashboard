package ui

import (
	"container/list"
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

func safeNext(s string) string {
	u, e := url.Parse(s)
	if s == "" || e != nil || !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") || strings.Contains(s, "\\") || u.IsAbs() || u.Host != "" {
		return "/"
	}
	return s
}
func (h *Handler) validSetupToken(g string) bool {
	w := h.setupToken
	if w == "" {
		return false
	}
	want := sha256.Sum256([]byte(w))
	got := sha256.Sum256([]byte(g))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

type attemptEntry struct {
	count int
	reset time.Time
	key   string
}
type attemptLimiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	capacity int
	entries  map[string]*list.Element
	lru      *list.List
}

func newAttemptLimiter(m int, w time.Duration, c int) *attemptLimiter {
	return &attemptLimiter{max: m, window: w, capacity: c, entries: map[string]*list.Element{}, lru: list.New()}
}
func (l *attemptLimiter) allow(k string, n time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	elem, ok := l.entries[k]
	if ok && !n.Before(elem.Value.(*attemptEntry).reset) {
		l.remove(elem)
		ok = false
	}
	if !ok && len(l.entries) >= l.capacity {
		l.remove(l.lru.Back())
	}
	if !ok {
		elem = l.lru.PushFront(&attemptEntry{key: k, reset: n.Add(l.window)})
		l.entries[k] = elem
	} else {
		l.lru.MoveToFront(elem)
	}
	e := elem.Value.(*attemptEntry)
	if e.count >= l.max {
		return false, e.reset.Sub(n)
	}
	e.count++
	return true, 0
}
func (l *attemptLimiter) remove(e *list.Element) {
	if e == nil {
		return
	}
	delete(l.entries, e.Value.(*attemptEntry).key)
	l.lru.Remove(e)
}
func (l *attemptLimiter) clear(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.remove(l.entries[key])
}
func (h *Handler) clientIP(r *http.Request) string {
	if h.trustedProxy(r) {
		if x := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); net.ParseIP(x) != nil {
			return x
		}
	}
	x, _, e := net.SplitHostPort(r.RemoteAddr)
	if e == nil {
		return x
	}
	return r.RemoteAddr
}
func allowFrom(w http.ResponseWriter, l *attemptLimiter, key string) bool {
	ok, d := l.allow(key, time.Now())
	if ok {
		return true
	}
	s := int(d.Seconds())
	if s < 1 {
		s = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(s))
	http.Error(w, "too many attempts; retry later", http.StatusTooManyRequests)
	return false
}
func (h *Handler) allowSetupAttempt(w http.ResponseWriter, r *http.Request) bool {
	return allowFrom(w, h.ipLimiter, "setup\x00"+h.clientIP(r))
}
func (h *Handler) allowLoginIP(w http.ResponseWriter, r *http.Request) bool {
	return allowFrom(w, h.ipLimiter, "login\x00"+h.clientIP(r))
}
func (h *Handler) allowLoginAccount(w http.ResponseWriter, u string) bool {
	return allowFrom(w, h.accountLimiter, strings.ToLower(strings.TrimSpace(u)))
}
func (h *Handler) clearLoginLimits(r *http.Request, u string) {
	h.ipLimiter.clear("login\x00" + h.clientIP(r))
	h.accountLimiter.clear(strings.ToLower(strings.TrimSpace(u)))
}
