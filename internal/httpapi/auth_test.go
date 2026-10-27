package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"projectmap/internal/access"
)

const localOrigin = "http://127.0.0.1:8080"

type identityResolver struct{ err error }

func (s *identityResolver) ResolveClient(_ context.Context, id string) (access.Identity, error) {
	if s.err != nil {
		return access.Identity{}, s.err
	}
	kind := "dashboard"
	if id == "agent" {
		kind = "agent"
	}
	return access.Identity{Profile: access.Profile{ID: "owner", DisplayName: "Local profile"}, Client: access.Client{ID: id, Name: "Local " + kind, Kind: kind}}, nil
}
func protectedHandler() (http.Handler, string, string, *identityResolver) {
	dashboard := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("d", 32)))
	agent := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("a", 32)))
	resolver := &identityResolver{}
	manager := access.New(resolver, map[[32]byte]string{sha256.Sum256([]byte(dashboard)): "dashboard", sha256.Sum256([]byte(agent)): "agent"})
	return Handler(fakeDB{}, "127.0.0.1:8080", Options{Access: manager}), dashboard, agent, resolver
}
func request(handler http.Handler, method, path, body, bearer string, cookie *http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, localOrigin+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	return res
}
func login(t *testing.T, handler http.Handler, credential string) *http.Cookie {
	t.Helper()
	res := request(handler, "POST", "/api/v1/session", `{"credential":"`+credential+`"}`, "", nil, map[string]string{"Origin": localOrigin})
	if res.Code != 200 {
		t.Fatalf("login failed: %d %s", res.Code, res.Body.String())
	}
	cookies := res.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session cookie")
	}
	return cookies[0]
}
func csrfFromMe(t *testing.T, handler http.Handler, cookie *http.Cookie) string {
	t.Helper()
	res := request(handler, "GET", "/api/v1/me", "", "", cookie, nil)
	var decoded struct {
		Data struct {
			CSRF string `json:"csrf_token"`
		} `json:"data"`
	}
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &decoded) != nil || decoded.Data.CSRF == "" {
		t.Fatalf("session identity failed: %d", res.Code)
	}
	return decoded.Data.CSRF
}
func TestBearerMeRequiresTrustedCredential(t *testing.T) {
	handler, _, agent, resolver := protectedHandler()
	if res := request(handler, "GET", "/api/v1/me", "", "", nil, nil); res.Code != 401 {
		t.Fatal("unauthenticated identity access")
	}
	if res := request(handler, "GET", "/api/v1/me", "", "bad", nil, nil); res.Code != 401 {
		t.Fatal("invalid bearer accepted")
	}
	res := request(handler, "GET", "/api/v1/me", "", agent, nil, nil)
	if res.Code != 200 || !strings.Contains(res.Body.String(), `"id":"agent"`) || strings.Contains(res.Body.String(), "csrf_token") {
		t.Fatalf("bearer identity failed: %s", res.Body.String())
	}
	resolver.err = access.ErrUnauthorized
	if res := request(handler, "GET", "/api/v1/me", "", agent, nil, nil); res.Code != 401 {
		t.Fatal("revoked bearer accepted")
	}
	resolver.err = errors.New("private connection credentials")
	res = request(handler, "GET", "/api/v1/me", "", agent, nil, nil)
	if res.Code != 503 || strings.Contains(res.Body.String(), "private connection") {
		t.Fatal("store outage not safely reported")
	}
}
func TestBrowserSessionAndCSRF(t *testing.T) {
	handler, dashboard, _, _ := protectedHandler()
	cookie := login(t, handler, dashboard)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/api/v1" || cookie.MaxAge != 43200 || cookie.Secure {
		t.Fatal("incorrect local session cookie attributes")
	}
	csrf := csrfFromMe(t, handler, cookie)
	for _, token := range []string{"", "wrong"} {
		res := request(handler, "DELETE", "/api/v1/session", "", "", cookie, map[string]string{"Origin": localOrigin, "X-CSRF-Token": token})
		if res.Code != 403 {
			t.Fatalf("invalid CSRF accepted: %d", res.Code)
		}
	}
	res := request(handler, "DELETE", "/api/v1/session", "", "", cookie, map[string]string{"Origin": localOrigin, "X-CSRF-Token": csrf})
	if res.Code != 204 || len(res.Result().Cookies()) != 1 || res.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout did not clear cookie")
	}
	if res := request(handler, "GET", "/api/v1/me", "", "", cookie, nil); res.Code != 401 {
		t.Fatal("logged out session still works")
	}
}
func TestOriginAndLoginRequestValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body, origin, contentType string
		status                          int
	}{
		{"missing origin", `{"credential":"a"}`, "", "application/json", 403},
		{"foreign origin", `{"credential":"a"}`, "https://attacker.example", "application/json", 403},
		{"unsupported media", `{"credential":"a"}`, localOrigin, "text/plain", 415},
		{"unknown field", `{"credential":"a","client_id":"spoof"}`, localOrigin, "application/json", 400},
		{"duplicate field", `{"credential":"a","credential":"b"}`, localOrigin, "application/json", 400},
		{"misnamed field", `{"Credential":"a"}`, localOrigin, "application/json", 400},
		{"null credential", `{"credential":null}`, localOrigin, "application/json", 400},
		{"extra JSON", `{"credential":"a"} {}`, localOrigin, "application/json", 400},
		{"empty credential", `{"credential":""}`, localOrigin, "application/json", 400},
		{"oversize body", strings.Repeat("x", maxSessionBody+1), localOrigin, "application/json", 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, _, _, _ := protectedHandler()
			res := request(handler, "POST", "/api/v1/session", tc.body, "", nil, map[string]string{"Origin": tc.origin, "Content-Type": tc.contentType})
			if res.Code != tc.status {
				t.Fatalf("expected %d, got %d: %s", tc.status, res.Code, res.Body.String())
			}
		})
	}
	handler, _, agent, _ := protectedHandler()
	res := request(handler, "POST", "/api/v1/session", `{"credential":"`+agent+`"}`, "", nil, map[string]string{"Origin": localOrigin})
	if res.Code != 401 {
		t.Fatal("agent credential created a browser session")
	}
	res = request(handler, "GET", "/api/v1/me", "", agent, nil, map[string]string{"Origin": "null"})
	if res.Code != 403 {
		t.Fatal("foreign origin authenticated")
	}
}
func TestInvalidAuthorizationDoesNotFallBackToCookie(t *testing.T) {
	handler, dashboard, _, _ := protectedHandler()
	cookie := login(t, handler, dashboard)
	res := request(handler, "GET", "/api/v1/me", "", "", cookie, map[string]string{"Authorization": "Basic invalid"})
	if res.Code != 401 {
		t.Fatal("bad authorization fell back to a browser session")
	}
}
func TestSessionRateLimiting(t *testing.T) {
	handler, _, _, _ := protectedHandler()
	for range 20 {
		res := request(handler, "POST", "/api/v1/session", `{"credential":"invalid"}`, "", nil, map[string]string{"Origin": localOrigin})
		if res.Code != 401 {
			t.Fatalf("unexpected authentication result: %d", res.Code)
		}
	}
	res := request(handler, "POST", "/api/v1/session", `{"credential":"invalid"}`, "", nil, map[string]string{"Origin": localOrigin})
	if res.Code != 429 || res.Header().Get("Retry-After") == "" {
		t.Fatal("authentication was not rate-limited")
	}
}
func TestUnprovisionedServerLeavesIdentityDisabled(t *testing.T) {
	handler := Handler(fakeDB{}, "127.0.0.1:8080")
	if res := request(handler, "GET", "/api/v1/me", "", "token", nil, nil); res.Code != 401 {
		t.Fatal("unprovisioned server exposed identity")
	}
}
