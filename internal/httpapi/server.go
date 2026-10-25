package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type Readiness interface{ Ready(context.Context) error }

func Handler(db Readiness, allowedHost string) http.Handler {
	mux := http.NewServeMux()
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
		mux.ServeHTTP(w, r)
	})
}
func status(w http.ResponseWriter, code int, value string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": value})
}
