package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"projectmap/internal/access"
	"projectmap/internal/jsonutil"
	"projectmap/internal/memory"
)

const sessionCookie = "projectmap_session"
const maxSessionBody = 16 << 10

type Options struct {
	Access *access.Manager
	Memory *memory.Service
}
type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
	Details   any    `json:"details,omitempty"`
}

func fail(w http.ResponseWriter, code int, name, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	writeJSON(w, map[string]any{"error": apiError{Code: name, Message: message, RequestID: rand.Text()}})
}
func result(w http.ResponseWriter, code int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	writeJSON(w, map[string]any{"data": data})
}
func accessFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, access.ErrUnauthorized):
		fail(w, 401, "unauthorized", "Valid local credentials are required.")
	case errors.Is(err, access.ErrRateLimited):
		w.Header().Set("Retry-After", "60")
		fail(w, 429, "rate_limited", "Too many authentication attempts or active sessions.")
	default:
		fail(w, 503, "service_unavailable", "The identity store is unavailable.")
	}
}

// authenticated contains the identity resolved from the database, never claims
// supplied by the caller. Session secrets are used only for CSRF and logout.
type authenticated struct {
	Identity access.Identity
	Session  access.Session
	Cookie   string
}

func authenticate(w http.ResponseWriter, r *http.Request, manager *access.Manager) (authenticated, bool) {
	if manager == nil {
		accessFailure(w, access.ErrUnauthorized)
		return authenticated{}, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	var out authenticated
	var err error
	if values, provided := r.Header["Authorization"]; provided {
		if len(values) != 1 {
			accessFailure(w, access.ErrUnauthorized)
			return out, false
		}
		parts := strings.Fields(values[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			accessFailure(w, access.ErrUnauthorized)
			return out, false
		}
		out.Identity, err = manager.Bearer(ctx, parts[1])
	} else {
		cookie, cookieErr := r.Cookie(sessionCookie)
		if cookieErr != nil {
			accessFailure(w, access.ErrUnauthorized)
			return out, false
		}
		out.Cookie = cookie.Value
		out.Identity, out.Session, err = manager.AuthenticateSession(ctx, cookie.Value)
	}
	if err != nil {
		accessFailure(w, err)
		return out, false
	}
	if out.Cookie != "" && r.Method != "GET" && r.Method != "HEAD" {
		values := r.Header.Values("X-CSRF-Token")
		if len(values) != 1 || subtle.ConstantTimeCompare([]byte(values[0]), []byte(out.Session.CSRF)) != 1 {
			fail(w, 403, "csrf_invalid", "A valid session CSRF token is required.")
			return out, false
		}
	}
	return out, true
}

func registerAccess(mux *http.ServeMux, manager *access.Manager, origin string) {
	mux.HandleFunc("GET /api/v1/me", func(w http.ResponseWriter, r *http.Request) {
		caller, ok := authenticate(w, r, manager)
		if !ok {
			return
		}
		data := map[string]any{"profile": caller.Identity.Profile, "client": caller.Identity.Client}
		if caller.Cookie != "" {
			data["csrf_token"] = caller.Session.CSRF
		}
		result(w, 200, data)
	})
	mux.HandleFunc("POST /api/v1/session", func(w http.ResponseWriter, r *http.Request) {
		// Browser credential exchange is same-origin only, including login CSRF.
		if r.Header.Get("Origin") != origin {
			fail(w, 403, "origin_not_allowed", "A same-origin session request is required.")
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			fail(w, 415, "unsupported_media_type", "Use application/json.")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxSessionBody)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			var limit *http.MaxBytesError
			if errors.As(err, &limit) {
				fail(w, 413, "request_too_large", "Request body is too large.")
			} else {
				fail(w, 400, "invalid_request", "Cannot read JSON request.")
			}
			return
		}
		var input map[string]any
		decodeErr := jsonutil.Decode(body, &input)
		credential, valid := input["credential"].(string)
		if decodeErr != nil || len(input) != 1 || !valid || strings.TrimSpace(credential) == "" || len(credential) > 128 {
			fail(w, 400, "invalid_request", "Provide a valid credential field.")
			return
		}
		if manager == nil {
			accessFailure(w, access.ErrUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		token, session, err := manager.Login(ctx, credential)
		if err != nil {
			accessFailure(w, err)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/api/v1", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: int(access.SessionLifetime.Seconds()), Expires: session.Expires})
		result(w, 200, map[string]bool{"authenticated": true})
	})
	mux.HandleFunc("DELETE /api/v1/session", func(w http.ResponseWriter, r *http.Request) {
		caller, ok := authenticate(w, r, manager)
		if !ok {
			return
		}
		if caller.Cookie == "" {
			fail(w, 403, "forbidden", "A browser session is required.")
			return
		}
		manager.Logout(caller.Cookie)
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/api/v1", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: -1, Expires: time.Unix(1, 0)})
		w.WriteHeader(http.StatusNoContent)
	})
}
