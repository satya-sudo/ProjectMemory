package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type Readiness interface{ Ready(context.Context) error }

func Handler(db Readiness, allowedHost string, options ...Options) http.Handler {
	mux := http.NewServeMux()
	var opts Options
	if len(options) > 0 {
		opts = options[0]
	}
	origin := "http://" + allowedHost
	registerAccess(mux, opts.Access, origin)
	registerMemory(mux, opts)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { status(w, http.StatusOK, "ok") })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ready(ctx); err != nil {
			status(w, http.StatusServiceUnavailable, "not_ready")
			return
		}
		status(w, http.StatusOK, "ready")
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		if r.Host != allowedHost {
			status(w, http.StatusForbidden, "forbidden")
			return
		}
		if origins := r.Header.Values("Origin"); len(origins) > 0 && (len(origins) != 1 || origins[0] != origin) {
			fail(w, 403, "origin_not_allowed", "Cross-origin requests are not allowed.")
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func status(w http.ResponseWriter, code int, value string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": value})
}

func writeJSON(w http.ResponseWriter, value any) { _ = json.NewEncoder(w).Encode(value) }
