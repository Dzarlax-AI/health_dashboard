package ui

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// SetTrustedForwardAuthNetworks configures the proxy CIDR allow-list for
// X-authentik-* headers. Empty input restores the default local/private rule.
func (h *Handler) SetTrustedForwardAuthNetworks(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		h.trustedFwdAuthNets = nil
		return nil
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
			return fmt.Errorf("invalid TRUSTED_FORWARD_AUTH_NETWORK CIDR %q: %w", cidr, err)
		}
		nets = append(nets, network)
	}
	h.trustedFwdAuthNets = nets
	return nil
}

func (h *Handler) forwardAuthTrusted(r *http.Request) bool {
	if !h.trustFwdAuth {
		return false
	}
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
	return ip.IsPrivate() || ip.IsLoopback()
}
