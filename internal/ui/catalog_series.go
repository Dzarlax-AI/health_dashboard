package ui

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	clientapi "health-receiver/internal/api"
	"health-receiver/internal/storage"
)

// Cursors contain private producer keys. Authenticate their tenant and query,
// and encrypt the keys. A process restart expires outstanding continuations.
var catalogCursorCipher = func() cipher.AEAD {
	var key [32]byte
	rand.Read(key[:])
	block, err := aes.NewCipher(key[:])
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		panic(err)
	}
	return aead
}()

func catalogQuery(q url.Values) (storage.CatalogMetricRequest, error) {
	bad := errors.New("invalid catalog filters")
	for name, values := range q {
		switch name {
		case "view", "metric", "date_field", "from", "to", "limit", "cursor":
		default:
			return storage.CatalogMetricRequest{}, bad
		}
		if len(values) != 1 {
			return storage.CatalogMetricRequest{}, bad
		}
	}
	req := storage.CatalogMetricRequest{MetricID: q.Get("metric"), DateField: q.Get("date_field"), FromDate: q.Get("from"), ToDate: q.Get("to"), Limit: 50}
	if req.MetricID == "" || len(req.MetricID) > 256 || (req.DateField != "" && !storage.ValidCatalogDateField(req.DateField)) || len(q.Get("cursor")) > 16384 {
		return req, bad
	}
	for _, date := range []string{req.FromDate, req.ToDate} {
		if date != "" {
			if _, err := time.Parse(time.DateOnly, date); err != nil {
				return req, bad
			}
		}
	}
	if req.ToDate != "" && req.ToDate < req.FromDate {
		return req, bad
	}
	if q.Has("limit") {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil || n < 1 || n > 256 {
			return req, bad
		}
		req.Limit = n
	}
	return req, nil
}

func (h *Handler) catalogSeries(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		jsonError(w, "invalid catalog query", 400)
		return
	}
	req, err := catalogQuery(q)
	if err != nil {
		jsonError(w, err.Error(), 400)
		return
	}
	scope, _ := json.Marshal([]string{h.tenantSchema(r), req.MetricID, req.FromDate, req.ToDate, req.DateField})
	if q.Has("cursor") {
		raw, err := base64.RawURLEncoding.DecodeString(q.Get("cursor"))
		if err != nil || len(raw) < catalogCursorCipher.Overhead() {
			jsonError(w, "invalid catalog cursor", 400)
			return
		}
		data, err := catalogCursorCipher.Open(nil, nil, raw, scope)
		if err != nil || json.Unmarshal(data, &req.After) != nil || req.After == nil {
			jsonError(w, "invalid catalog cursor", 400)
			return
		}
	}
	page, err := h.tenantDB(r).ReadCatalogMetricPage(r.Context(), req)
	if errors.Is(err, storage.ErrNativeCatalogUnavailable) {
		jsonError(w, "catalog unavailable for this profile", 503)
		return
	}
	if err != nil {
		jsonError(w, "catalog lookup failed", 500)
		return
	}
	out := clientapi.CatalogSeriesResponse{Metric: req.MetricID, Samples: make([]clientapi.CatalogSeriesSample, 0, len(page.Samples))}
	for _, v := range page.Samples {
		s := clientapi.CatalogSeriesSample{Status: v.Status, Disposition: v.Disposition, Reason: v.Reason, Value: v.Value, ObservedAt: v.ObservedAt, AvailableAt: v.AvailableAt}
		s.BooleanValue, s.TextValue = v.Boolean, v.Text
		if v.Number != nil {
			n := v.Number.String()
			s.NumericValue = &n
		}
		if v.Source != nil {
			s.Source = *v.Source
			if strings.HasSuffix(s.Source, ".metric_points") {
				s.Source = "metric_points"
			}
		}
		var key map[string]json.RawMessage
		_ = json.Unmarshal(v.SampleKey, &key)
		for _, dimension := range []string{"date", "day", "wake_date", "target_date", "original_date", "anchor", "anchor_day", "period_day", "effective_date", "metric_date", "local_date", "start_date", "end_date", "source", "units", "component"} {
			var value string
			if json.Unmarshal(key[dimension], &value) == nil && value != "" {
				if s.Dimensions == nil {
					s.Dimensions = map[string]string{}
				}
				s.Dimensions[dimension] = value
			}
		}
		out.Samples = append(out.Samples, s)
	}
	if page.Next != nil {
		data, _ := json.Marshal(page.Next)
		out.NextCursor = base64.RawURLEncoding.EncodeToString(catalogCursorCipher.Seal(nil, nil, data, scope))
	}
	jsonResponse(w, out)
}
