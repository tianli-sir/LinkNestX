package http

import (
	"net/http/httptest"
	"testing"
)

func TestAuthenticationClientAddressUsesOnlyConfiguredTrustedProxyBoundary(t *testing.T) {
	h := NewAuthHandlers(nil)
	for _, tc := range []struct {
		name, remote, forwarded, want string
		trusted                       []string
	}{
		{"untrusted_header", "192.0.2.1:123", "203.0.113.99", "192.0.2.1", nil},
		{"loopback_requires_configuration", "127.0.0.1:123", "203.0.113.99", "127.0.0.1", nil},
		{"trusted_proxy", "127.0.0.1:123", "203.0.113.99", "203.0.113.99", []string{"127.0.0.1/32"}},
		{"rightmost_untrusted_boundary", "127.0.0.1:123", "203.0.113.99, 192.0.2.1", "192.0.2.1", []string{"127.0.0.1/32"}},
		{"trusted_chain", "127.0.0.1:123", "203.0.113.99, 10.0.0.2", "203.0.113.99", []string{"127.0.0.1/32", "10.0.0.0/24"}},
		{"malformed_header", "127.0.0.1:123", "forged", "127.0.0.1", []string{"127.0.0.1/32"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h.configureTrustedProxies(tc.trusted)
			r := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
			r.RemoteAddr = tc.remote
			r.Header.Set("X-Forwarded-For", tc.forwarded)
			if got := h.clientAddress(r); got != tc.want {
				t.Fatalf("client=%s want=%s", got, tc.want)
			}
		})
	}
}
