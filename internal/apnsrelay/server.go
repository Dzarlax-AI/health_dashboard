package apnsrelay

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"health-receiver/internal/storage"
)

const (
	OutcomeAccepted              = "accepted"
	OutcomeGrantInvalid          = "grant_invalid"
	OutcomeRetryableTokenRotated = "retryable_token_rotated"
	OutcomeRetryablePreSend      = "retryable_pre_send"
	OutcomeRetryableAPNs         = "retryable_apns"
	OutcomeAmbiguous             = "ambiguous"
	OutcomeRejected              = "rejected"

	maxRelayRequestBytes    = 32 * 1024 // App Attest CBOR attestationObject can be ~4-16 KiB
	maxDeliveryRequestBytes = 1024
	maxDeliveryTTL          = 24 * time.Hour
)

type Server struct {
	registry   *Registry
	store      *Store
	limiter    *RateLimiter
	dailyLimit int
	TrustProxy bool
	now        func() time.Time

	metricsMu        sync.Mutex
	attestRegister   map[string]int64
	deliveriesTotal  map[string]int64
	rateLimitedTotal map[string]int64
}

func NewServer(registry *Registry, store *Store) *Server {
	return &Server{
		registry:         registry,
		store:            store,
		limiter:          NewRateLimiter(time.Now),
		dailyLimit:       DefaultDailyGrantLimit,
		now:              time.Now,
		attestRegister:   make(map[string]int64),
		deliveriesTotal:  make(map[string]int64),
		rateLimitedTotal: make(map[string]int64),
	}
}

func (s *Server) SetNow(fn func() time.Time) {
	s.now = fn
	s.limiter = NewRateLimiter(fn)
	if s.store != nil {
		s.store.now = fn
	}
}

func (s *Server) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("POST /v1/attest/challenge", s.handleChallenge)
	mux.HandleFunc("POST /v1/attest/register", s.handleAttestRegister)
	mux.HandleFunc("POST /v1/grants", s.handleCreateGrant)
	mux.HandleFunc("PUT /v1/grants/{grant_id}/refresh", s.handleRefreshGrant)
	mux.HandleFunc("PUT /v1/attest/token", s.handleUpdateDeviceToken)
	mux.HandleFunc("DELETE /v1/grants/{grant_id}", s.handleRevokeGrant)
	mux.HandleFunc("DELETE /v1/grants/by-client-key/{client_grant_key}", s.handleCancelGrantByClientKey)
	mux.HandleFunc("POST /v1/deliveries", s.handleDeliver)
	return mux
}

func (s *Server) incAttestMetric(appID, env, result string) {
	s.metricsMu.Lock()
	s.attestRegister[appID+"|"+env+"|"+result]++
	s.metricsMu.Unlock()
}

func (s *Server) incDeliveryMetric(appID, kind, outcome string) {
	s.metricsMu.Lock()
	s.deliveriesTotal[appID+"|"+kind+"|"+outcome]++
	s.metricsMu.Unlock()
}

func (s *Server) incRateLimitMetric(scope string) {
	s.metricsMu.Lock()
	s.rateLimitedTotal[scope]++
	s.metricsMu.Unlock()
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unhealthy"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	activeByApp, err := s.store.CountActiveGrantsByApp(r.Context())
	if err != nil {
		activeByApp = map[string]int{}
	}
	s.metricsMu.Lock()
	attest := make(map[string]int64, len(s.attestRegister))
	for k, v := range s.attestRegister {
		attest[k] = v
	}
	deliveries := make(map[string]int64, len(s.deliveriesTotal))
	for k, v := range s.deliveriesTotal {
		deliveries[k] = v
	}
	rateLimited := make(map[string]int64, len(s.rateLimitedTotal))
	for k, v := range s.rateLimitedTotal {
		rateLimited[k] = v
	}
	s.metricsMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"grants_active":        activeByApp,
		"attest_register":      attest,
		"deliveries_total":     deliveries,
		"rate_limited_total":   rateLimited,
	})
}

type ChallengeRequest struct {
	AppID string `json:"app_id"`
	KeyID string `json:"key_id,omitempty"`
	Op    string `json:"op"`
}

type ChallengeResponse struct {
	ChallengeID uuid.UUID `json:"challenge_id"`
	Nonce       string    `json:"nonce"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func (s *Server) handleChallenge(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.allowAttestIP(w, ip) {
		return
	}
	var req ChallengeRequest
	if _, err := readStrictJSON(w, r, 1024, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	app, ok := s.registry.Get(req.AppID)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "unknown app_id")
		return
	}
	if !ValidAttestOp(req.Op) {
		writeJSONError(w, http.StatusBadRequest, "invalid op")
		return
	}
	if req.Op != OpAttestRegister && strings.TrimSpace(req.KeyID) == "" {
		writeJSONError(w, http.StatusBadRequest, "key_id is required")
		return
	}
	ch, err := s.store.IssueChallenge(r.Context(), app.AppID, strings.TrimSpace(req.KeyID), req.Op)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to issue challenge")
		return
	}
	writeJSON(w, http.StatusOK, ChallengeResponse{
		ChallengeID: ch.ID,
		Nonce:       ch.NonceB64,
		ExpiresAt:   ch.ExpiresAt,
	})
}

type AttestRegisterRequest struct {
	AppID             string `json:"app_id"`
	KeyID             string `json:"key_id"`
	ChallengeID       string `json:"challenge_id"`
	AttestationObject string `json:"attestation_object"`
}

func (s *Server) handleAttestRegister(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.allowAttestIP(w, ip) {
		return
	}
	var req AttestRegisterRequest
	if _, err := readStrictJSON(w, r, maxRelayRequestBytes, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	app, ok := s.registry.Get(req.AppID)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "unknown app_id")
		return
	}
	chID, err := uuid.Parse(strings.TrimSpace(req.ChallengeID))
	if err != nil || chID == uuid.Nil {
		writeJSONError(w, http.StatusBadRequest, "invalid challenge_id")
		return
	}
	attestObj, err := decodeBase64Flexible(strings.TrimSpace(req.AttestationObject))
	if err != nil || len(attestObj) == 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid attestation_object")
		return
	}
	verified, err := s.store.RegisterAttestedKey(r.Context(), app, strings.TrimSpace(req.KeyID), chID, attestObj)
	if err != nil {
		switch {
		case errors.Is(err, ErrChallengeInvalid):
			s.incAttestMetric(app.AppID, "unknown", "challenge_invalid")
			writeJSONError(w, http.StatusUnauthorized, "challenge invalid or expired")
		case errors.Is(err, ErrKeyAlreadyRegistered):
			s.incAttestMetric(app.AppID, "unknown", "conflict")
			writeJSONError(w, http.StatusConflict, "key already registered")
		case errors.Is(err, ErrAttestationFailed):
			s.incAttestMetric(app.AppID, "unknown", "rejected")
			writeJSONError(w, http.StatusForbidden, "attestation verification failed")
		default:
			s.incAttestMetric(app.AppID, "unknown", "error")
			writeJSONError(w, http.StatusInternalServerError, "attestation registration failed")
		}
		return
	}
	s.incAttestMetric(app.AppID, verified.AttestEnv, "ok")
	writeJSON(w, http.StatusOK, map[string]any{
		"key_id":     verified.KeyID,
		"app_id":     app.AppID,
		"attest_env": verified.AttestEnv,
	})
}

type CreateGrantRequest struct {
	AppID                string `json:"app_id"`
	Op                   string `json:"op"`
	ResourceID           string `json:"resource_id"`
	ChallengeID          string `json:"challenge_id"`
	Nonce                string `json:"nonce"`
	ClientGrantKey       string `json:"client_grant_key"`
	SecretHash           string `json:"secret_hash"`
	RegID                string `json:"reg_id"`
	InboxContractVersion int    `json:"inbox_contract_version"`
	APNsToken            string `json:"apns_token"`
	APNsEnv              string `json:"apns_env"`
}

func (s *Server) handleCreateGrant(w http.ResponseWriter, r *http.Request) {
	var req CreateGrantRequest
	rawBody, err := readStrictJSON(w, r, maxRelayRequestBytes, &req)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	ac, ok := s.parseAssertionRequest(w, r, req.AppID, req.Op, OpGrantCreate, req.ChallengeID, req.Nonce, rawBody)
	if !ok {
		return
	}
	clientGrantKey, err := uuid.Parse(strings.TrimSpace(req.ClientGrantKey))
	if err != nil || clientGrantKey == uuid.Nil || strings.TrimSpace(req.ResourceID) != clientGrantKey.String() {
		writeJSONError(w, http.StatusBadRequest, "invalid client_grant_key or resource_id")
		return
	}
	secretHash, err := decodeBase64Flexible(strings.TrimSpace(req.SecretHash))
	if err != nil || len(secretHash) != 32 {
		writeJSONError(w, http.StatusBadRequest, "invalid secret_hash")
		return
	}
	regID, err := uuid.Parse(strings.TrimSpace(req.RegID))
	if err != nil || regID == uuid.Nil {
		writeJSONError(w, http.StatusBadRequest, "invalid reg_id")
		return
	}
	if req.InboxContractVersion < storage.InboxContractV1 || req.InboxContractVersion > storage.InboxContractV2 {
		writeJSONError(w, http.StatusBadRequest, "invalid inbox_contract_version")
		return
	}
	token := strings.TrimSpace(req.APNsToken)
	if !storage.ValidAPNsToken(token) {
		writeJSONError(w, http.StatusBadRequest, "invalid apns_token")
		return
	}
	env := strings.TrimSpace(req.APNsEnv)
	if !storage.ValidAPNsEnv(env) {
		writeJSONError(w, http.StatusBadRequest, "invalid apns_env")
		return
	}

	summary, err := s.store.CreateGrant(r.Context(), ac, CreateGrantInput{
		ClientGrantKey:       clientGrantKey,
		SecretHash:           secretHash,
		RegID:                regID,
		InboxContractVersion: req.InboxContractVersion,
		APNsToken:            token,
		APNsEnv:              env,
	})
	if err != nil {
		s.writeStoreAssertionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"grant_id":   summary.GrantID,
		"expires_at": summary.ExpiresAt,
	})
}

type RefreshGrantRequest struct {
	AppID                string `json:"app_id"`
	Op                   string `json:"op"`
	ResourceID           string `json:"resource_id"`
	ChallengeID          string `json:"challenge_id"`
	Nonce                string `json:"nonce"`
	APNsToken            string `json:"apns_token,omitempty"`
	InboxContractVersion *int   `json:"inbox_contract_version,omitempty"`
}

func (s *Server) handleRefreshGrant(w http.ResponseWriter, r *http.Request) {
	grantID, err := uuid.Parse(strings.TrimSpace(r.PathValue("grant_id")))
	if err != nil || grantID == uuid.Nil {
		writeJSONError(w, http.StatusBadRequest, "invalid grant_id")
		return
	}
	var req RefreshGrantRequest
	rawBody, err := readStrictJSON(w, r, maxRelayRequestBytes, &req)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(req.ResourceID) != grantID.String() {
		writeJSONError(w, http.StatusBadRequest, "resource_id mismatch")
		return
	}
	ac, ok := s.parseAssertionRequest(w, r, req.AppID, req.Op, OpGrantRefresh, req.ChallengeID, req.Nonce, rawBody)
	if !ok {
		return
	}
	tok := strings.TrimSpace(req.APNsToken)
	if tok != "" && !storage.ValidAPNsToken(tok) {
		writeJSONError(w, http.StatusBadRequest, "invalid apns_token")
		return
	}
	if req.InboxContractVersion != nil && (*req.InboxContractVersion < storage.InboxContractV1 || *req.InboxContractVersion > storage.InboxContractV2) {
		writeJSONError(w, http.StatusBadRequest, "invalid inbox_contract_version")
		return
	}
	summary, err := s.store.RefreshGrant(r.Context(), ac, RefreshGrantInput{
		GrantID:              grantID,
		APNsToken:            tok,
		InboxContractVersion: req.InboxContractVersion,
	})
	if err != nil {
		s.writeStoreAssertionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"grant_id":   summary.GrantID,
		"expires_at": summary.ExpiresAt,
	})
}

type UpdateDeviceTokenRequest struct {
	AppID       string `json:"app_id"`
	Op          string `json:"op"`
	ResourceID  string `json:"resource_id"`
	ChallengeID string `json:"challenge_id"`
	Nonce       string `json:"nonce"`
	APNsToken   string `json:"apns_token"`
	APNsEnv     string `json:"apns_env"`
}

func (s *Server) handleUpdateDeviceToken(w http.ResponseWriter, r *http.Request) {
	var req UpdateDeviceTokenRequest
	rawBody, err := readStrictJSON(w, r, maxRelayRequestBytes, &req)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	ac, ok := s.parseAssertionRequest(w, r, req.AppID, req.Op, OpDeviceUpdateToken, req.ChallengeID, req.Nonce, rawBody)
	if !ok {
		return
	}
	if strings.TrimSpace(req.ResourceID) != ac.KeyID {
		writeJSONError(w, http.StatusBadRequest, "resource_id mismatch")
		return
	}
	tok := strings.TrimSpace(req.APNsToken)
	if !storage.ValidAPNsToken(tok) {
		writeJSONError(w, http.StatusBadRequest, "invalid apns_token")
		return
	}
	env := strings.TrimSpace(req.APNsEnv)
	if !storage.ValidAPNsEnv(env) {
		writeJSONError(w, http.StatusBadRequest, "invalid apns_env")
		return
	}
	updated, err := s.store.UpdateDeviceToken(r.Context(), ac, tok, env)
	if err != nil {
		s.writeStoreAssertionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": updated})
}

type RevokeGrantRequest struct {
	AppID       string `json:"app_id"`
	Op          string `json:"op"`
	ResourceID  string `json:"resource_id"`
	ChallengeID string `json:"challenge_id"`
	Nonce       string `json:"nonce"`
}

func (s *Server) handleRevokeGrant(w http.ResponseWriter, r *http.Request) {
	grantID, err := uuid.Parse(strings.TrimSpace(r.PathValue("grant_id")))
	if err != nil || grantID == uuid.Nil {
		writeJSONError(w, http.StatusBadRequest, "invalid grant_id")
		return
	}
	var req RevokeGrantRequest
	rawBody, err := readStrictJSON(w, r, maxRelayRequestBytes, &req)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(req.ResourceID) != grantID.String() {
		writeJSONError(w, http.StatusBadRequest, "resource_id mismatch")
		return
	}
	ac, ok := s.parseAssertionRequest(w, r, req.AppID, req.Op, OpGrantRevoke, req.ChallengeID, req.Nonce, rawBody)
	if !ok {
		return
	}
	revoked, err := s.store.RevokeGrant(r.Context(), ac, grantID)
	if err != nil {
		s.writeStoreAssertionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": revoked})
}

func (s *Server) handleCancelGrantByClientKey(w http.ResponseWriter, r *http.Request) {
	clientKey, err := uuid.Parse(strings.TrimSpace(r.PathValue("client_grant_key")))
	if err != nil || clientKey == uuid.Nil {
		writeJSONError(w, http.StatusBadRequest, "invalid client_grant_key")
		return
	}
	var req RevokeGrantRequest
	rawBody, err := readStrictJSON(w, r, maxRelayRequestBytes, &req)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(req.ResourceID) != clientKey.String() {
		writeJSONError(w, http.StatusBadRequest, "resource_id mismatch")
		return
	}
	ac, ok := s.parseAssertionRequest(w, r, req.AppID, req.Op, OpGrantCancelByClientKey, req.ChallengeID, req.Nonce, rawBody)
	if !ok {
		return
	}
	if err := s.store.CancelGrantByClientKey(r.Context(), ac, clientKey); err != nil {
		s.writeStoreAssertionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": true})
}

type DeliveryRequest struct {
	Kind        string `json:"kind"`
	Locale      string `json:"locale"`
	InboxItemID string `json:"inbox_item_id"`
	RegID       string `json:"reg_id"`
	APNsID      string `json:"apns_id"`
	ExpiresAt   int64  `json:"expires_at"`
}

type DeliveryResponse struct {
	Status            int    `json:"status"`
	Outcome           string `json:"outcome"`
	Reason            string `json:"reason,omitempty"`
	APNsID            string `json:"apns_id,omitempty"`
	APNsStatus        int    `json:"apns_status,omitempty"`
	RetryAfterSeconds int    `json:"retry_after_seconds,omitempty"`
	Timestamp         *int64 `json:"timestamp,omitempty"`
}

func (s *Server) handleDeliver(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if ok, retryAfter := s.limiter.Allow("deliver:ip:"+ip, 120, time.Minute); !ok {
		s.incRateLimitMetric("deliver_ip")
		writeRetryAfter(w, retryAfter)
		writeJSON(w, http.StatusTooManyRequests, DeliveryResponse{
			Status:            http.StatusTooManyRequests,
			Outcome:           OutcomeRetryablePreSend,
			Reason:            "ip_rate_limited",
			RetryAfterSeconds: ceilSeconds(retryAfter),
		})
		return
	}

	secret, ok := parseBearerToken(r.Header.Get("Authorization"))
	if !ok || len(secret) < 16 {
		writeJSON(w, http.StatusUnauthorized, DeliveryResponse{
			Status:  http.StatusUnauthorized,
			Outcome: OutcomeGrantInvalid,
			Reason:  "InvalidGrant",
		})
		return
	}

	var req DeliveryRequest
	if _, err := readStrictJSON(w, r, maxDeliveryRequestBytes, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, DeliveryResponse{
			Status:  http.StatusBadRequest,
			Outcome: OutcomeRejected,
			Reason:  "invalid_json",
		})
		return
	}

	grant, err := s.store.LookupGrantBySecret(r.Context(), secret)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, DeliveryResponse{
			Status:            http.StatusServiceUnavailable,
			Outcome:           OutcomeRetryablePreSend,
			Reason:            "relay_store_unavailable",
			RetryAfterSeconds: 60,
		})
		return
	}
	switch grant.Status {
	case GrantStatusNotFound:
		writeJSON(w, http.StatusUnauthorized, DeliveryResponse{
			Status:  http.StatusUnauthorized,
			Outcome: OutcomeGrantInvalid,
			Reason:  "InvalidGrant",
		})
		return
	case GrantStatusRevoked:
		writeJSON(w, http.StatusGone, DeliveryResponse{
			Status:  http.StatusGone,
			Outcome: OutcomeGrantInvalid,
			Reason:  "GrantRevoked",
		})
		return
	case GrantStatusExpired:
		writeJSON(w, http.StatusGone, DeliveryResponse{
			Status:  http.StatusGone,
			Outcome: OutcomeGrantInvalid,
			Reason:  "GrantExpired",
		})
		return
	case GrantStatusInvalidated:
		reason := grant.InvalidReason
		if reason == "" {
			reason = "Unregistered"
		}
		writeJSON(w, http.StatusGone, DeliveryResponse{
			Status:  http.StatusGone,
			Outcome: OutcomeGrantInvalid,
			Reason:  reason,
		})
		return
	}

	app, ok := s.registry.Get(grant.AppID)
	if !ok {
		writeJSON(w, http.StatusGone, DeliveryResponse{
			Status:  http.StatusGone,
			Outcome: OutcomeGrantInvalid,
			Reason:  "AppDisabled",
		})
		return
	}
	sender := app.SenderForEnv(grant.APNsEnv)
	if sender == nil {
		writeJSON(w, http.StatusGone, DeliveryResponse{
			Status:  http.StatusGone,
			Outcome: OutcomeGrantInvalid,
			Reason:  "EnvironmentDisabled",
		})
		return
	}

	// Validate strict delivery boundaries against the grant.
	inboxItemID, err := uuid.Parse(strings.TrimSpace(req.InboxItemID))
	if err != nil || inboxItemID == uuid.Nil {
		s.incDeliveryMetric(app.AppID, req.Kind, OutcomeRejected)
		writeJSON(w, http.StatusBadRequest, DeliveryResponse{Status: http.StatusBadRequest, Outcome: OutcomeRejected, Reason: "invalid_inbox_item_id"})
		return
	}
	regID, err := uuid.Parse(strings.TrimSpace(req.RegID))
	if err != nil || regID == uuid.Nil || regID != grant.RegID {
		s.incDeliveryMetric(app.AppID, req.Kind, OutcomeRejected)
		writeJSON(w, http.StatusForbidden, DeliveryResponse{Status: http.StatusForbidden, Outcome: OutcomeRejected, Reason: "reg_id_mismatch"})
		return
	}
	apnsID, err := uuid.Parse(strings.TrimSpace(req.APNsID))
	if err != nil || apnsID == uuid.Nil {
		s.incDeliveryMetric(app.AppID, req.Kind, OutcomeRejected)
		writeJSON(w, http.StatusBadRequest, DeliveryResponse{Status: http.StatusBadRequest, Outcome: OutcomeRejected, Reason: "invalid_apns_id"})
		return
	}
	now := s.currentTime()
	exp := time.Unix(req.ExpiresAt, 0).UTC()
	if !exp.After(now) || exp.After(now.Add(maxDeliveryTTL)) {
		s.incDeliveryMetric(app.AppID, req.Kind, OutcomeRejected)
		writeJSON(w, http.StatusBadRequest, DeliveryResponse{Status: http.StatusBadRequest, Outcome: OutcomeRejected, Reason: "invalid_expires_at"})
		return
	}
	if err := app.Policy.ValidateDelivery(req.Kind, req.Locale, grant.InboxContractVersion); err != nil {
		reason := "invalid_delivery"
		if errors.Is(err, ErrKindUnsupportedForGrant) {
			reason = "kind_unsupported_for_grant"
		}
		s.incDeliveryMetric(app.AppID, req.Kind, OutcomeRejected)
		writeJSON(w, http.StatusBadRequest, DeliveryResponse{Status: http.StatusBadRequest, Outcome: OutcomeRejected, Reason: reason})
		return
	}

	// Per-grant burst and daily quota (enforced BEFORE calling APNs).
	if ok, retryAfter := s.limiter.Allow("deliver:grant:"+grant.GrantID.String(), 5, time.Minute); !ok {
		s.incRateLimitMetric("deliver_grant_burst")
		s.incDeliveryMetric(app.AppID, req.Kind, OutcomeRetryablePreSend)
		writeRetryAfter(w, retryAfter)
		writeJSON(w, http.StatusTooManyRequests, DeliveryResponse{
			Status:            http.StatusTooManyRequests,
			Outcome:           OutcomeRetryablePreSend,
			Reason:            "grant_burst_rate_limited",
			RetryAfterSeconds: ceilSeconds(retryAfter),
		})
		return
	}
	if err := s.store.ReserveDailyDeliveryQuota(r.Context(), grant.GrantID, s.dailyLimit); err != nil {
		if errors.Is(err, ErrDailyQuotaExceeded) {
			s.incRateLimitMetric("deliver_grant_daily")
			s.incDeliveryMetric(app.AppID, req.Kind, OutcomeRetryablePreSend)
			nextMidnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
			retryAfter := nextMidnight.Sub(now)
			if retryAfter < time.Minute {
				retryAfter = time.Minute
			}
			writeRetryAfter(w, retryAfter)
			writeJSON(w, http.StatusTooManyRequests, DeliveryResponse{
				Status:            http.StatusTooManyRequests,
				Outcome:           OutcomeRetryablePreSend,
				Reason:            "grant_daily_quota_exceeded",
				RetryAfterSeconds: ceilSeconds(retryAfter),
			})
			return
		}
		s.incDeliveryMetric(app.AppID, req.Kind, OutcomeRetryablePreSend)
		writeJSON(w, http.StatusServiceUnavailable, DeliveryResponse{
			Status:            http.StatusServiceUnavailable,
			Outcome:           OutcomeRetryablePreSend,
			Reason:            "relay_store_unavailable",
			RetryAfterSeconds: 60,
		})
		return
	}

	payload, err := app.Policy.BuildPayload(req.Kind, req.Locale, inboxItemID, grant.RegID)
	if err != nil {
		s.incDeliveryMetric(app.AppID, req.Kind, OutcomeRejected)
		writeJSON(w, http.StatusBadRequest, DeliveryResponse{Status: http.StatusBadRequest, Outcome: OutcomeRejected, Reason: "payload_build_failed"})
		return
	}
	collapseID := app.Policy.BuildCollapseID(req.Kind, inboxItemID)

	res, sendErr := sender.Send(r.Context(), grant.APNsEnv, grant.APNsToken, apnsID.String(), "alert", collapseID, 10, exp, payload)
	if sendErr != nil {
		// Transport outcome between Relay and Apple is unknown after dispatch:
		// MUST be returned as ambiguous (504) so the caller never blindly retries.
		s.incDeliveryMetric(app.AppID, req.Kind, OutcomeAmbiguous)
		writeJSON(w, http.StatusGatewayTimeout, DeliveryResponse{
			Status:  http.StatusGatewayTimeout,
			Outcome: OutcomeAmbiguous,
			Reason:  "apns_transport_ambiguous",
		})
		return
	}

	switch {
	case res.Status == http.StatusOK:
		s.incDeliveryMetric(app.AppID, req.Kind, OutcomeAccepted)
		outAPNsID := res.APNsID
		if outAPNsID == "" {
			outAPNsID = apnsID.String()
		}
		writeJSON(w, http.StatusOK, DeliveryResponse{
			Status:     http.StatusOK,
			Outcome:    OutcomeAccepted,
			APNsID:     outAPNsID,
			APNsStatus: http.StatusOK,
		})
		return

	case res.Status == http.StatusGone:
		reason := res.Reason
		if reason == "" {
			reason = "Unregistered"
		}
		invRes, invErr := s.store.ConditionallyInvalidateGrant(r.Context(), grant.GrantID, grant.TokenVersion, grant.TokenHash, reason, res.Timestamp)
		if invErr != nil {
			// Conservative: do not falsely invalidate client if DB write failed; report rejected without invalidating.
			s.incDeliveryMetric(app.AppID, req.Kind, OutcomeRejected)
			writeJSON(w, http.StatusBadGateway, DeliveryResponse{Status: http.StatusBadGateway, Outcome: OutcomeRejected, Reason: reason, APNsStatus: res.Status})
			return
		}
		s.writeConditionalInvalidationResponse(w, app.AppID, req.Kind, res.Status, reason, res.Timestamp, invRes)
		return

	case res.Status == http.StatusBadRequest && (res.Reason == "BadDeviceToken" || res.Reason == "DeviceTokenNotForTopic" || res.Reason == "Unregistered"):
		// Apple does not include a timestamp on 400; pass nil so condition checks token_version + token_hash only.
		invRes, invErr := s.store.ConditionallyInvalidateGrant(r.Context(), grant.GrantID, grant.TokenVersion, grant.TokenHash, res.Reason, nil)
		if invErr != nil {
			s.incDeliveryMetric(app.AppID, req.Kind, OutcomeRejected)
			writeJSON(w, http.StatusBadGateway, DeliveryResponse{Status: http.StatusBadGateway, Outcome: OutcomeRejected, Reason: res.Reason, APNsStatus: res.Status})
			return
		}
		s.writeConditionalInvalidationResponse(w, app.AppID, req.Kind, res.Status, res.Reason, nil, invRes)
		return

	case res.Status == http.StatusTooManyRequests:
		s.incDeliveryMetric(app.AppID, req.Kind, OutcomeRetryableAPNs)
		retrySec := ceilSeconds(res.RetryAfter)
		if retrySec > 0 {
			writeRetryAfter(w, res.RetryAfter)
		}
		writeJSON(w, http.StatusTooManyRequests, DeliveryResponse{
			Status:            http.StatusTooManyRequests,
			Outcome:           OutcomeRetryableAPNs,
			Reason:            res.Reason,
			APNsStatus:        res.Status,
			RetryAfterSeconds: retrySec,
		})
		return

	case res.Status >= 500:
		s.incDeliveryMetric(app.AppID, req.Kind, OutcomeRetryableAPNs)
		retrySec := ceilSeconds(res.RetryAfter)
		if retrySec > 0 {
			writeRetryAfter(w, res.RetryAfter)
		}
		writeJSON(w, http.StatusBadGateway, DeliveryResponse{
			Status:            http.StatusBadGateway,
			Outcome:           OutcomeRetryableAPNs,
			Reason:            res.Reason,
			APNsStatus:        res.Status,
			RetryAfterSeconds: retrySec,
		})
		return

	default:
		s.incDeliveryMetric(app.AppID, req.Kind, OutcomeRejected)
		writeJSON(w, http.StatusBadRequest, DeliveryResponse{
			Status:     http.StatusBadRequest,
			Outcome:    OutcomeRejected,
			Reason:     res.Reason,
			APNsStatus: res.Status,
		})
		return
	}
}

func (s *Server) writeConditionalInvalidationResponse(w http.ResponseWriter, appID, kind string, apnsStatus int, reason string, appleTS *time.Time, invRes InvalidationResult) {
	var tsMillis *int64
	if appleTS != nil {
		ms := appleTS.UnixMilli()
		tsMillis = &ms
	}
	switch invRes {
	case InvalidationResultInvalidated, InvalidationResultAlreadyInvalid:
		s.incDeliveryMetric(appID, kind, OutcomeGrantInvalid)
		writeJSON(w, http.StatusGone, DeliveryResponse{
			Status:     http.StatusGone,
			Outcome:    OutcomeGrantInvalid,
			Reason:     reason,
			APNsStatus: apnsStatus,
			Timestamp:  tsMillis,
		})
	case InvalidationResultTokenRotated:
		s.incDeliveryMetric(appID, kind, OutcomeRetryableTokenRotated)
		writeRetryAfter(w, 30*time.Second)
		writeJSON(w, http.StatusConflict, DeliveryResponse{
			Status:            http.StatusConflict,
			Outcome:           OutcomeRetryableTokenRotated,
			Reason:            "TokenRotatedDuringDelivery",
			APNsStatus:        apnsStatus,
			RetryAfterSeconds: 30,
		})
	default: // InvalidationResultStaleTimestamp (410 timestamp < token_updated_at with same token)
		s.incDeliveryMetric(appID, kind, OutcomeRejected)
		writeJSON(w, http.StatusConflict, DeliveryResponse{
			Status:     http.StatusConflict,
			Outcome:    OutcomeRejected,
			Reason:     "StaleAPNsInvalidationTimestamp",
			APNsStatus: apnsStatus,
			Timestamp:  tsMillis,
		})
	}
}

func (s *Server) allowAttestIP(w http.ResponseWriter, ip string) bool {
	if ok, retryAfter := s.limiter.Allow("attest:1m:"+ip, 10, time.Minute); !ok {
		s.incRateLimitMetric("attest_ip_minute")
		writeRetryAfter(w, retryAfter)
		writeJSONError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return false
	}
	if ok, retryAfter := s.limiter.Allow("attest:1h:"+ip, 30, time.Hour); !ok {
		s.incRateLimitMetric("attest_ip_hour")
		writeRetryAfter(w, retryAfter)
		writeJSONError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return false
	}
	return true
}

func (s *Server) parseAssertionRequest(w http.ResponseWriter, r *http.Request, appID, reqOp, expectedOp, challengeIDStr, nonceB64 string, rawBody []byte) (AssertionContext, bool) {
	app, ok := s.registry.Get(appID)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "unknown app_id")
		return AssertionContext{}, false
	}
	if reqOp != expectedOp {
		writeJSONError(w, http.StatusBadRequest, "op mismatch")
		return AssertionContext{}, false
	}
	chID, err := uuid.Parse(strings.TrimSpace(challengeIDStr))
	if err != nil || chID == uuid.Nil || strings.TrimSpace(nonceB64) == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid challenge_id or nonce")
		return AssertionContext{}, false
	}
	keyID := strings.TrimSpace(r.Header.Get("X-App-Attest-Key-ID"))
	assertionB64 := strings.TrimSpace(r.Header.Get("X-App-Attest-Assertion"))
	if keyID == "" || assertionB64 == "" {
		writeJSONError(w, http.StatusUnauthorized, "missing App Attest assertion headers")
		return AssertionContext{}, false
	}
	assertionObj, err := decodeBase64Flexible(assertionB64)
	if err != nil || len(assertionObj) == 0 {
		writeJSONError(w, http.StatusUnauthorized, "invalid App Attest assertion header")
		return AssertionContext{}, false
	}
	return AssertionContext{
		App:         app,
		KeyID:       keyID,
		Op:          expectedOp,
		ChallengeID: chID,
		NonceB64:    strings.TrimSpace(nonceB64),
		RawBody:     rawBody,
		Assertion:   assertionObj,
	}, true
}

func (s *Server) writeStoreAssertionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrKeyNotFound), errors.Is(err, ErrChallengeInvalid), errors.Is(err, ErrAssertionFailed):
		writeJSONError(w, http.StatusUnauthorized, "assertion verification failed")
	case errors.Is(err, ErrEnvNotAllowed):
		writeJSONError(w, http.StatusForbidden, "apns environment not allowed")
	case errors.Is(err, ErrGrantConflict):
		writeJSONError(w, http.StatusConflict, "grant conflict")
	case errors.Is(err, ErrGrantNotFound):
		writeJSONError(w, http.StatusNotFound, "grant not found")
	case errors.Is(err, ErrGrantLimitExceeded):
		writeJSONError(w, http.StatusTooManyRequests, "grant limit exceeded")
	default:
		writeJSONError(w, http.StatusInternalServerError, "relay operation failed")
	}
}

func readStrictJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, dst any) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes))
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return nil, err
	}
	if err := dec.Decode(new(struct{})); err != io.EOF {
		return nil, errors.New("trailing JSON data")
	}
	return body, nil
}

func decodeBase64Flexible(s string) ([]byte, error) {
	if raw, err := base64.StdEncoding.DecodeString(s); err == nil {
		return raw, nil
	}
	if raw, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return raw, nil
	}
	if raw, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return raw, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

func parseBearerToken(header string) (string, bool) {
	parts := strings.SplitN(strings.TrimSpace(header), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}
	tok := strings.TrimSpace(parts[1])
	return tok, tok != ""
}

func (s *Server) clientIP(r *http.Request) string {
	if s.TrustProxy {
		if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
			return realIP
		}
		if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
			parts := strings.Split(xff, ",")
			for i := len(parts) - 1; i >= 0; i-- {
				candidate := strings.TrimSpace(parts[i])
				if candidate != "" {
					return candidate
				}
			}
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func ceilSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int(math.Ceil(d.Seconds()))
}

func writeRetryAfter(w http.ResponseWriter, d time.Duration) {
	if sec := ceilSeconds(d); sec > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(sec))
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
