package http

import (
	"github.com/linknestx/linknestx/server/internal/config"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuthBodyLimitsAndRemoteAddressRateLimit(t *testing.T) {
	h := testAuthHandler(t)
	router := NewRouterWithAuth(config.Default(), h.Service)
	request := func(body, forwarded string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Forwarded-For", forwarded)
		r.RemoteAddr = "192.0.2.1:1234"
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if w := request(`{"username":"admin","password":"`+strings.Repeat("x", 9000)+`"}`, ""); w.Code != 413 {
		t.Fatalf("oversized=%d %s", w.Code, w.Body)
	}
	if w := request(`{"username":"admin","password":"`+strings.Repeat("x", 1025)+`"}`, ""); w.Code != 400 {
		t.Fatalf("oversized field=%d", w.Code)
	}
	for _, body := range []string{"null", "{}{}", `{"username":"admin"} trailing`} {
		if w := request(body, ""); w.Code != 400 {
			t.Fatalf("malformed=%d", w.Code)
		}
	}
	var last *httptest.ResponseRecorder
	for i := 0; i < 25; i++ {
		last = request(`{"username":"missing","password":"password123"}`, "198.51.100."+string(rune('a'+i)))
	}
	if last.Code != 429 || last.Header().Get("Retry-After") == "" {
		t.Fatalf("spoofed forward address bypassed rate limit: %d", last.Code)
	}
}
func TestAuthenticationRateLimiterHasBoundedMemoryAndRecovers(t *testing.T) {
	limiter := newAuthLimiter()
	now := time.Now()
	limiter.now = func() time.Time { return now }
	for i := 0; i < 20; i++ {
		if !limiter.allow("192.0.2.1:2") {
			t.Fatal("burst denied early")
		}
	}
	if limiter.allow("192.0.2.1:3") {
		t.Fatal("port bypassed limit")
	}
	now = now.Add(3 * time.Second)
	if !limiter.allow("192.0.2.1:4") {
		t.Fatal("refill failed")
	}
	for i := 0; i < 5000; i++ {
		limiter.allow(string(rune(i + 1000)))
	}
	if len(limiter.entries) > 4096 {
		t.Fatalf("unbounded entries=%d", len(limiter.entries))
	}
	now = now.Add(2 * time.Minute)
	if !limiter.allow("new") {
		t.Fatal("stale buckets did not expire")
	}
}
