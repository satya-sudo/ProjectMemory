package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

type readinessFunc func(context.Context) error

func (f readinessFunc) Ready(ctx context.Context) error { return f(ctx) }

func TestReadinessBoundsSlowDatabaseCheck(t *testing.T) {
	var timeoutObserved bool
	db := readinessFunc(func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("readiness database check has no deadline")
		}
		<-ctx.Done()
		timeoutObserved = ctx.Err() == context.DeadlineExceeded
		return ctx.Err()
	})
	req := httptest.NewRequest("GET", "http://127.0.0.1:8080/readyz", nil)
	res := httptest.NewRecorder()
	start := time.Now()
	Handler(db, "127.0.0.1:8080").ServeHTTP(res, req)
	if res.Code != 503 || !timeoutObserved || time.Since(start) > 5*time.Second {
		t.Fatalf("slow readiness check was not bounded: code=%d timeout=%v elapsed=%v", res.Code, timeoutObserved, time.Since(start))
	}
}

func TestHealthDoesNotWaitForDatabase(t *testing.T) {
	db := readinessFunc(func(context.Context) error {
		t.Fatal("liveness consulted the database")
		return nil
	})
	req := httptest.NewRequest("GET", "http://127.0.0.1:8080/healthz", nil)
	res := httptest.NewRecorder()
	Handler(db, "127.0.0.1:8080").ServeHTTP(res, req)
	if res.Code != 200 {
		t.Fatalf("liveness failed: %d", res.Code)
	}
}
