package http

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

type loginBucket struct {
	tokens float64
	at     time.Time
}
type authLimiter struct {
	mu      sync.Mutex
	entries map[string]loginBucket
	now     func() time.Time
}

func newAuthLimiter() *authLimiter {
	return &authLimiter{entries: map[string]loginBucket{}, now: time.Now}
}
func (l *authLimiter) allow(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	if len(host) > 256 {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	bucket, ok := l.entries[host]
	if !ok {
		if len(l.entries) >= 4096 {
			for key, item := range l.entries {
				if now.Sub(item.at) > time.Minute {
					delete(l.entries, key)
				}
			}
		}
		if len(l.entries) >= 4096 {
			return false
		}
		bucket = loginBucket{20, now}
	}
	bucket.tokens = min(20, bucket.tokens+now.Sub(bucket.at).Seconds()/3)
	bucket.at = now
	allowed := bucket.tokens >= 1
	if allowed {
		bucket.tokens--
	}
	l.entries[host] = bucket
	return allowed
}
func (h *AuthHandlers) allowAuthentication(w http.ResponseWriter, r *http.Request) bool {
	if !h.limiter.allow(h.clientAddress(r)) {
		w.Header().Set("Retry-After", "3")
		writeError(w, 429, errRateLimit)
		return false
	}
	return true
}

func (h *AuthHandlers) configureTrustedProxies(cidrs []string) {
	h.trustedProxies = nil
	for _, cidr := range cidrs {
		if prefix, err := netip.ParsePrefix(cidr); err == nil {
			h.trustedProxies = append(h.trustedProxies, prefix)
		}
	}
}
func (h *AuthHandlers) isTrustedProxy(ip netip.Addr) bool {
	for _, prefix := range h.trustedProxies {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

// Walk from the socket peer toward the client and stop at the first untrusted
// hop. Forwarded values supplied to an untrusted direct peer never affect limits.
func (h *AuthHandlers) clientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remote, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	remote = remote.Unmap()
	if !h.isTrustedProxy(remote) {
		return remote.String()
	}
	values := r.Header.Values("X-Forwarded-For")
	if len(values) != 1 || len(values[0]) > 2048 {
		return remote.String()
	}
	parts := strings.Split(values[0], ",")
	if len(parts) > 32 {
		return remote.String()
	}
	current := remote
	for i := len(parts) - 1; i >= 0 && h.isTrustedProxy(current); i-- {
		next, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			return remote.String()
		}
		current = next.Unmap()
	}
	return current.String()
}
