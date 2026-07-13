package ui

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

func ValidateForwardAuthConfig(enabled bool, raw string) ([]*net.IPNet, error) {
	if strings.TrimSpace(raw) == "" {
		if enabled {
			return nil, errors.New("TRUST_FORWARD_AUTH requires explicit trusted proxy CIDRs")
		}
		return nil, nil
	}
	return ParseTrustedForwardAuthNetworks(raw)
}

// SetTrustedForwardAuthNetworks configures the proxy CIDR allow-list for
// X-authentik-* headers. Empty input trusts no peer.
func (h *Handler) SetTrustedForwardAuthNetworks(raw string) error {
	nets, err := ParseTrustedForwardAuthNetworks(raw)
	if err != nil {
		return err
	}
	h.trustedFwdAuthNets = nets
	return nil
}

func ParseTrustedForwardAuthNetworks(raw string) ([]*net.IPNet, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	parts := strings.Split(raw, ",")
	nets := make([]*net.IPNet, 0, len(parts))
	for _, part := range parts {
		cidr := strings.TrimSpace(part)
		if cidr == "" {
			continue
		}
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("invalid TRUSTED_FORWARD_AUTH_NETWORK CIDR %q: %w", cidr, err)
		}
		nets = append(nets, network)
	}
	return nets, nil
}

func (h *Handler) SetTrustedForwardAuthNetworkList(nets []*net.IPNet) { h.trustedFwdAuthNets = nets }

func (h *Handler) forwardAuthTrusted(r *http.Request) bool {
	if !h.trustFwdAuth {
		return false
	}
	return h.trustedProxy(r)
}

func (h *Handler) trustedProxy(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if len(h.trustedFwdAuthNets) > 0 {
		for _, network := range h.trustedFwdAuthNets {
			if network.Contains(ip) {
				return true
			}
		}
		return false
	}
	return false
}
