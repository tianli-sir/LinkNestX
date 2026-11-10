package http

import (
	"context"
	"net"
	"net/http"
	"net/netip"
)

type webSchemeKey struct{}

func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if scheme, ok := r.Context().Value(webSchemeKey{}).(string); ok {
		return scheme
	}
	return "http"
}

// Forwarded protocol is only meaningful on a socket from a configured proxy.
// The original Host still defines the origin; forwarded hosts are never trusted.
func withTrustedWebScheme(cidrs []string, next http.Handler) http.Handler {
	prefixes := []netip.Prefix{}
	for _, cidr := range cidrs {
		if p, e := netip.ParsePrefix(cidr); e == nil {
			prefixes = append(prefixes, p)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, e := net.SplitHostPort(r.RemoteAddr)
		if e == nil {
			ip, e := netip.ParseAddr(host)
			if e == nil {
				for _, p := range prefixes {
					if p.Contains(ip.Unmap()) {
						values := r.Header.Values("X-Forwarded-Proto")
						if len(values) == 1 && (values[0] == "http" || values[0] == "https") {
							r = r.WithContext(context.WithValue(r.Context(), webSchemeKey{}, values[0]))
						}
						break
					}
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
