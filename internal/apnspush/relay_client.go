package apnspush

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	TransportModeDirect = "direct"
	TransportModeRelay  = "relay"
	TransportModeNone   = "none"

	RelayOutcomeAccepted              = "accepted"
	RelayOutcomeGrantInvalid          = "grant_invalid"
	RelayOutcomeRetryableTokenRotated = "retryable_token_rotated"
	RelayOutcomeRetryablePreSend      = "retryable_pre_send"
	RelayOutcomeRetryableAPNs         = "retryable_apns"
	RelayOutcomeAmbiguous             = "ambiguous"
	RelayOutcomeRejected              = "rejected"
)

// RelayDeliveryRequest is the strict structured payload sent from a self-hosted
// server to the trusted APNs Relay. No arbitrary text, local dates, or
// collapse_id are ever sent by the server; Relay builds the neutral payload.
type RelayDeliveryRequest struct {
	GrantSecret string `json:"-"`
	Kind        string `json:"kind"`
	Locale      string `json:"locale"`
	InboxItemID string `json:"inbox_item_id"`
	RegID       string `json:"reg_id"`
	APNsID      string `json:"apns_id"`
	ExpiresAt   int64  `json:"expires_at"`
}

type RelayDeliveryResult struct {
	Status            int           `json:"status"`
	Outcome           string        `json:"outcome"`
	Reason            string        `json:"reason,omitempty"`
	APNsID            string        `json:"apns_id,omitempty"`
	APNsStatus        int           `json:"apns_status,omitempty"`
	RetryAfterSeconds int           `json:"retry_after_seconds,omitempty"`
	Timestamp         *int64        `json:"timestamp,omitempty"`
	RetryAfter        time.Duration `json:"-"`
	AppleInvalidated  *time.Time    `json:"-"`
}

// RelayTransportSender is implemented by *RelayClient and mock relay senders.
type RelayTransportSender interface {
	SendRelay(ctx context.Context, req RelayDeliveryRequest) (RelayDeliveryResult, error)
}

// RelayClient delivers push events through a configured trusted APNs Relay.
// Redirects are strictly forbidden so Bearer delivery secrets can never leak.
type RelayClient struct {
	RelayURL string
	Topic    string
	Client   *http.Client
}

// ValidateRelayURL validates APNS_RELAY_URL for SSRF resistance. Production
// servers require https://, a non-local public hostname, and no userinfo/query.
func ValidateRelayURL(raw string, allowInsecureLocal bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("APNS_RELAY_URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid APNS_RELAY_URL: %w", err)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("APNS_RELAY_URL must not contain credentials, query, or fragment")
	}
	if u.Path != "" && u.Path != "/" {
		return "", errors.New("APNS_RELAY_URL must be an origin without path")
	}
	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return "", errors.New("APNS_RELAY_URL must include a hostname")
	}
	if !allowInsecureLocal {
		if !strings.EqualFold(u.Scheme, "https") {
			return "", errors.New("APNS_RELAY_URL must use https")
		}
		lowerHost := strings.ToLower(host)
		if lowerHost == "localhost" || strings.HasSuffix(lowerHost, ".localhost") || strings.HasSuffix(lowerHost, ".local") {
			return "", errors.New("APNS_RELAY_URL must not target localhost")
		}
		if ip, err := netip.ParseAddr(host); err == nil && !isPublicRelayIP(ip) {
			return "", errors.New("APNS_RELAY_URL must not target private or loopback IP")
		}
	} else if !strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http") {
		return "", errors.New("APNS_RELAY_URL must use http or https")
	}
	origin := strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
	return origin, nil
}

func NewRelayClient(rawURL, topic string, allowInsecureLocal bool) (*RelayClient, error) {
	origin, err := ValidateRelayURL(rawURL, allowInsecureLocal)
	if err != nil {
		return nil, err
	}
	topic = strings.TrimSpace(topic)
	if topic == "" {
		topic = DefaultTopic
	}
	var transport http.RoundTripper
	if !allowInsecureLocal {
		tr := &http.Transport{
			DialContext:         dialPublicRelay,
			ForceAttemptHTTP2:   true,
			MaxIdleConns:        10,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		}
		transport = tr
	}
	return &RelayClient{
		RelayURL: origin,
		Topic:    topic,
		Client: &http.Client{
			Timeout:   15 * time.Second,
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("redirects are forbidden for APNs relay")
			},
		},
	}, nil
}

var sharedIPv4 = netip.MustParsePrefix("100.64.0.0/10")

func isPublicRelayIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.Zone() == "" && ip.IsGlobalUnicast() && !ip.IsPrivate() && !sharedIPv4.Contains(ip)
}

func dialPublicRelay(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	var dialer net.Dialer
	lastErr := errors.New("APNs relay hostname does not resolve to a public address")
	for _, ip := range addresses {
		if !isPublicRelayIP(ip) {
			continue
		}
		// Dial the checked address directly to avoid resolving the name again.
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (c *RelayClient) EffectiveTopic() string {
	if c == nil || strings.TrimSpace(c.Topic) == "" {
		return DefaultTopic
	}
	return strings.TrimSpace(c.Topic)
}

func (c *RelayClient) CloseIdleConnections() {
	if c != nil && c.Client != nil {
		c.Client.CloseIdleConnections()
	}
}

// Send satisfies TransportSender so *RelayClient can be passed through
// existing TransportSender plumbing; direct calls are rejected as mode mismatch.
func (c *RelayClient) Send(_ context.Context, _, _, _, _, _ string, _ int, _ time.Time, _ []byte) (Result, error) {
	return Result{}, errors.New("relay client does not support direct APNs sends")
}

func (c *RelayClient) httpClient() *http.Client {
	if c.Client != nil {
		// Copy client struct to guarantee CheckRedirect always rejects redirects
		// even if a caller injected a custom Client without CheckRedirect.
		cp := *c.Client
		cp.CheckRedirect = func(*http.Request, []*http.Request) error {
			return errors.New("redirects are forbidden for APNs relay")
		}
		return &cp
	}
	return &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("redirects are forbidden for APNs relay")
		},
	}
}

func (c *RelayClient) SendRelay(ctx context.Context, req RelayDeliveryRequest) (RelayDeliveryResult, error) {
	var out RelayDeliveryResult
	if c == nil || strings.TrimSpace(c.RelayURL) == "" || strings.TrimSpace(req.GrantSecret) == "" {
		return out, errors.New("relay client or grant secret unavailable")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return out, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.RelayURL, "/")+"/v1/deliveries", bytes.NewReader(body))
	if err != nil {
		return out, errors.New("relay request unavailable")
	}
	httpReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(req.GrantSecret))
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return out, errors.New("relay transport outcome unknown")
	}
	defer resp.Body.Close()

	out.Status = resp.StatusCode
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if readErr != nil {
		return out, errors.New("relay response outcome unknown")
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return RelayDeliveryResult{Status: resp.StatusCode}, errors.New("relay response malformed; outcome unknown")
	}
	if out.Status != 0 && out.Status != resp.StatusCode {
		return RelayDeliveryResult{Status: resp.StatusCode}, errors.New("relay response status mismatch; outcome unknown")
	}
	out.Status = resp.StatusCode
	if out.RetryAfterSeconds > 0 {
		out.RetryAfter = time.Duration(out.RetryAfterSeconds) * time.Second
	} else if ra := strings.TrimSpace(resp.Header.Get("Retry-After")); ra != "" {
		if sec, parseErr := strconv.Atoi(ra); parseErr == nil && sec > 0 {
			out.RetryAfter = time.Duration(sec) * time.Second
		}
	}
	if out.Timestamp != nil && *out.Timestamp > 0 {
		ts := time.UnixMilli(*out.Timestamp).UTC()
		out.AppleInvalidated = &ts
	}
	if resp.StatusCode == http.StatusGatewayTimeout || out.Outcome == RelayOutcomeAmbiguous {
		return out, errors.New("relay APNs transport outcome ambiguous")
	}
	return out, nil
}

// TransportConfig represents the active process-level APNs delivery mode
// (Direct APNs takes precedence over APNS_RELAY_URL).
type TransportConfig struct {
	Mode         string
	Topic        string
	RelayURL     string
	DirectSender *Sender
	RelayClient  *RelayClient
}

func DirectTransportConfig(sender *Sender) *TransportConfig {
	if sender == nil {
		return nil
	}
	return &TransportConfig{
		Mode:         TransportModeDirect,
		Topic:        sender.EffectiveTopic(),
		DirectSender: sender,
	}
}

func RelayTransportConfig(client *RelayClient) *TransportConfig {
	if client == nil {
		return nil
	}
	return &TransportConfig{
		Mode:        TransportModeRelay,
		Topic:       client.EffectiveTopic(),
		RelayURL:    client.RelayURL,
		RelayClient: client,
	}
}

func (t *TransportConfig) EffectiveMode() string {
	if t == nil {
		return TransportModeNone
	}
	return t.Mode
}

func (t *TransportConfig) EffectiveTopic() string {
	if t == nil || strings.TrimSpace(t.Topic) == "" {
		return DefaultTopic
	}
	return strings.TrimSpace(t.Topic)
}

func (t *TransportConfig) Sender() TransportSender {
	if t == nil {
		return nil
	}
	if t.Mode == TransportModeDirect && t.DirectSender != nil {
		return t.DirectSender
	}
	if t.Mode == TransportModeRelay && t.RelayClient != nil {
		return t.RelayClient
	}
	return nil
}

func (t *TransportConfig) CloseIdleConnections() {
	if t == nil {
		return
	}
	if t.DirectSender != nil {
		t.DirectSender.CloseIdleConnections()
	}
	if t.RelayClient != nil {
		t.RelayClient.CloseIdleConnections()
	}
}

// LoadTransportFromEnv resolves the active APNs transport in priority order:
//  1. Direct APNs when APNS credentials env vars are set.
//  2. APNs Relay when APNS_RELAY_URL is explicitly configured (opt-in).
//  3. Disabled (nil, nil) when neither is configured.
func LoadTransportFromEnv() (*TransportConfig, error) {
	direct, err := LoadSenderFromEnv()
	if err != nil {
		return nil, err
	}
	if direct != nil {
		return DirectTransportConfig(direct), nil
	}
	relayURL := strings.TrimSpace(os.Getenv("APNS_RELAY_URL"))
	if relayURL == "" {
		return nil, nil
	}
	topic := strings.TrimSpace(os.Getenv("APNS_TOPIC"))
	client, err := NewRelayClient(relayURL, topic, false)
	if err != nil {
		return nil, err
	}
	return RelayTransportConfig(client), nil
}
