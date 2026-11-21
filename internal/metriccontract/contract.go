// Package metriccontract negotiates the Health Sync representation on existing routes.
package metriccontract

import (
	"net/http"
	"os"
	"sync/atomic"
)

const Header = "X-Health-Metrics-Contract"

type Version string

const (
	Legacy  Version = "1"
	Current Version = "2"
)

var legacyRequests atomic.Uint64
var currentRequests atomic.Uint64
var unsupportedRequests atomic.Uint64
var unversionedRequests atomic.Uint64

// Counts is installation-wide and contains no account or device identifiers.
func Counts() (legacy, current, unversioned, unsupported uint64) {
	return legacyRequests.Load(), currentRequests.Load(), unversionedRequests.Load(), unsupportedRequests.Load()
}

func negotiate(w http.ResponseWriter, r *http.Request, defaultVersion Version) (Version, bool) {
	w.Header().Add("Vary", Header)
	w.Header().Set("Cache-Control", "private, no-store")
	values := r.Header.Values(Header)
	version := defaultVersion
	if len(values) == 1 {
		version = Version(values[0])
	}
	if len(values) > 1 || (version != Legacy && version != Current) {
		unsupportedRequests.Add(1)
		http.Error(w, "unsupported health metrics contract", http.StatusBadRequest)
		return "", false
	}
	switch {
	case len(values) == 0:
		unversionedRequests.Add(1)
	case version == Legacy:
		legacyRequests.Add(1)
	default:
		currentRequests.Add(1)
	}
	w.Header().Set(Header, string(version))
	return version, true
}

// Read selects the temporary legacy presentation for unversioned clients.
// The flag only changes presentation and does not identify or block callers.
func Read(w http.ResponseWriter, r *http.Request) (Version, bool) {
	if os.Getenv("HEALTH_METRICS_V1_ENABLED") == "0" {
		version, ok := negotiate(w, r, Current)
		if ok && version == Legacy {
			w.Header().Del(Header)
			http.Error(w, "health metrics contract 1 is disabled", http.StatusNotAcceptable)
			return "", false
		}
		return version, ok
	}
	return negotiate(w, r, Legacy)
}

// Ingest keeps accepting the established wire format for unversioned senders,
// including Health Auto Export, regardless of the legacy-read flag.
func Ingest(w http.ResponseWriter, r *http.Request) (Version, bool) {
	return negotiate(w, r, Legacy)
}
