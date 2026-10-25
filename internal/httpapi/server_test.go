package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeDB struct{ err error }

func (f fakeDB) Ready(context.Context) error { return f.err }
func TestHealthAndReadiness(t *testing.T) {
	for _, tc := range []struct {
		path string
		err  error
		code int
		body string
	}{
		{"/healthz", errors.New("database password must not leak"), 200, "ok"},
		{"/readyz", nil, 200, "ready"},
		{"/readyz", errors.New("database password must not leak"), 503, "not_ready"},
	} {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080"+tc.path, nil)
		res := httptest.NewRecorder()
		Handler(fakeDB{tc.err}, "127.0.0.1:8080").ServeHTTP(res, req)
		if res.Code != tc.code || !strings.Contains(res.Body.String(), `"status":"`+tc.body+`"`) {
			t.Fatalf("%s: %d %s", tc.path, res.Code, res.Body.String())
		}
		if strings.Contains(res.Body.String(), "password") {
			t.Fatal("leaked internal error")
		}
	}
}
func TestRejectUnexpectedHost(t *testing.T) {
	req := httptest.NewRequest("GET", "http://attacker.example/healthz", nil)
	res := httptest.NewRecorder()
	Handler(fakeDB{}, "127.0.0.1:8080").ServeHTTP(res, req)
	if res.Code != 403 {
		t.Fatalf("unexpected host accepted: %d", res.Code)
	}
}
