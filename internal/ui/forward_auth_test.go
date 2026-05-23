package ui

import (
	"net/http/httptest"
	"testing"
)

func TestForwardAuthTrustedRequiresEnabledFlag(t *testing.T) {
	h := &Handler{}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.8:1234"
	if h.forwardAuthTrusted(r) {
		t.Fatal("forward auth should not be trusted when disabled")
	}
}

func TestForwardAuthTrustedDefaultsToLocalAndPrivateAddresses(t *testing.T) {
	h := &Handler{trustFwdAuth: true}
	tests := []struct {
		remote string
		want   bool
	}{
		{remote: "127.0.0.1:1234", want: true},
		{remote: "10.0.0.8:1234", want: true},
		{remote: "172.16.0.8:1234", want: true},
		{remote: "192.168.50.4:1234", want: true},
		{remote: "[fd00::1]:1234", want: true},
		{remote: "8.8.8.8:1234", want: false},
		{remote: "not-an-ip", want: false},
	}
	for _, tt := range tests {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tt.remote
		if got := h.forwardAuthTrusted(r); got != tt.want {
			t.Fatalf("forwardAuthTrusted(%q) = %v, want %v", tt.remote, got, tt.want)
		}
	}
}

func TestForwardAuthTrustedCIDROverride(t *testing.T) {
	h := &Handler{trustFwdAuth: true}
	if err := h.SetTrustedForwardAuthNetworks("100.64.0.0/10, 203.0.113.4/32"); err != nil {
		t.Fatalf("SetTrustedForwardAuthNetworks: %v", err)
	}

	allowed := httptest.NewRequest("GET", "/", nil)
	allowed.RemoteAddr = "100.104.66.65:1234"
	if !h.forwardAuthTrusted(allowed) {
		t.Fatal("configured CIDR should be trusted")
	}

	privateButNotConfigured := httptest.NewRequest("GET", "/", nil)
	privateButNotConfigured.RemoteAddr = "10.0.0.8:1234"
	if h.forwardAuthTrusted(privateButNotConfigured) {
		t.Fatal("configured CIDRs should override the default private-network rule")
	}
}

func TestSetTrustedForwardAuthNetworksRejectsInvalidCIDR(t *testing.T) {
	h := &Handler{trustFwdAuth: true}
	if err := h.SetTrustedForwardAuthNetworks("10.0.0.0/8,not-a-cidr"); err == nil {
		t.Fatal("expected invalid CIDR error")
	}
}
