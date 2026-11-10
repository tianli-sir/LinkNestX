package http

import (
	"encoding/json"
	"github.com/linknestx/linknestx/server/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebCookieOriginCSRFAndNativeCapabilityReplay(t *testing.T) {
	f := newSocialFixture(t)
	router := f.router
	request := func(method, path, body, origin, csrf string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if csrf != "" {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	login := `{"username":"admin","password":"correct horse battery staple"}`
	expectCode(t, request("POST", "/api/v1/web/login", login, "http://evil.test", "", nil), 403)
	w := request("POST", "/api/v1/web/login", login, "http://example.com", "", nil)
	expectCode(t, w, 200)
	if strings.Contains(w.Body.String(), "accessToken") || strings.Contains(w.Body.String(), "refreshToken") {
		t.Fatal("web disclosed bearer")
	}
	var session struct {
		CSRF string `json:"csrfToken"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &session); e != nil || session.CSRF == "" {
		t.Fatal(w.Body)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal(cookies)
	}
	cookie := cookies[0]
	expectCode(t, request("GET", "/api/v1/web/session", "", "", "", cookie), 200)
	expectCode(t, request("POST", "/api/v1/friend-requests", "{}", "http://example.com", "", cookie), 403)
	for _, route := range []struct{ method, path string }{{"POST", "/api/v1/files/uploads"}, {"PUT", "/api/v1/uploads/id/chunks/0"}, {"POST", "/api/v1/uploads/id/complete"}, {"POST", "/api/v1/users"}, {"POST", "/api/v1/groups"}, {"PATCH", "/api/v1/profile"}, {"PUT", "/api/v1/profile/avatar"}, {"GET", "/api/v1/file-versions/id/manifest"}, {"GET", "/api/v1/files/id/chunks/hash"}, {"GET", "/api/v1/p2p/sources"}, {"GET", "/api/v1/p2p/ws"}, {"POST", "/api/v1/p2p/announce"}, {"POST", "/api/v1/p2p/grants"}, {"POST", "/api/v1/admin/users/id/disable"}} {
		expectCode(t, request(route.method, route.path, "{}", "http://example.com", session.CSRF, cookie), 403)
	}
	// Replaying the opaque browser cookie at the native refresh API must not mint a native session.
	expectCode(t, request("POST", "/api/v1/auth/refresh", `{"refreshToken":"`+cookie.Value+`"}`, "", "", nil), 401)
	expectCode(t, request("POST", "/api/v1/web/refresh", "{}", "http://example.com", "bad", cookie), 403)
	w = request("POST", "/api/v1/web/refresh", "{}", "http://example.com", session.CSRF, cookie)
	expectCode(t, w, 200)
	expectCode(t, request("GET", "/api/v1/web/session", "", "", "", cookie), 401)
	cookie = w.Result().Cookies()[0]
	json.Unmarshal(w.Body.Bytes(), &session)
	expectCode(t, request("POST", "/api/v1/web/logout", "{}", "http://example.com", session.CSRF, cookie), 204)
	expectCode(t, request("GET", "/api/v1/me", "", "", "", cookie), 401)
	// Router with auth alone also serves browser login/session.
	router = NewRouterWithAuth(config.Default(), f.a)
	expectCode(t, request("POST", "/api/v1/web/login", login, "http://example.com", "", nil), 200)
}

func TestWebHTTPSCookieAndTrustedProxyScheme(t *testing.T) {
	a := testAuthHandler(t).Service
	cfg := config.Default()
	cfg.TrustedProxyCIDRs = []string{"127.0.0.1/32"}
	router := NewRouterWithAuth(cfg, a)
	for _, tc := range []struct {
		target, remote, forwarded string
		want                      int
		secure                    bool
	}{{"https://example.com", "192.0.2.1:123", "", 200, true}, {"http://example.com", "127.0.0.1:123", "https", 200, true}, {"http://example.com", "192.0.2.1:123", "https", 403, false}} {
		r := httptest.NewRequest("POST", tc.target+"/api/v1/web/login", strings.NewReader(`{"username":"admin","password":"correct horse battery staple"}`))
		r.RemoteAddr = tc.remote
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://example.com")
		if tc.forwarded != "" {
			r.Header.Set("X-Forwarded-Proto", tc.forwarded)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		expectCode(t, w, tc.want)
		if tc.want == 200 && (len(w.Result().Cookies()) != 1 || w.Result().Cookies()[0].Secure != tc.secure) {
			t.Fatal("secure cookie missing")
		}
	}
}
