package httpapi

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"projectmap/internal/jsonutil"
	"projectmap/internal/memory"
	"strconv"
	"time"
)

const maxMemoryBody = 2 << 20

func memoryFailure(w http.ResponseWriter, err error) {
	var problem *memory.Error
	if errors.As(err, &problem) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(problem.Status)
		writeJSON(w, map[string]any{"error": apiError{Code: problem.Code, Message: problem.Message, Details: problem.Details, RequestID: rand.Text()}})
		return
	}
	fail(w, 503, "service_unavailable", "The project memory service is unavailable.")
}
func memoryJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(w, 415, "unsupported_media_type", "Use application/json.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxMemoryBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			fail(w, 413, "request_too_large", "Request body is too large.")
		} else {
			fail(w, 400, "invalid_request", "Cannot read JSON request.")
		}
		return false
	}
	if err := jsonutil.Decode(body, target); err != nil {
		fail(w, 400, "invalid_request", "Invalid JSON shape, duplicate keys, or unknown fields.")
		return false
	}
	return true
}
func singleKey(r *http.Request) (string, error) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 || !memory.ValidKey(values[0]) {
		return "", memory.Problem(400, "invalid_request", "Supply one valid Idempotency-Key.")
	}
	return values[0], nil
}
func memoryQuery(r *http.Request, resource string) (memory.Query, error) {
	query := memory.Query{Resource: resource, ProjectID: r.PathValue("project_id"), TargetID: r.PathValue("target_id")}
	allowed := map[string]bool{}
	switch resource {
	case "projects":
		for _, name := range []string{"q", "archived", "limit", "cursor"} {
			allowed[name] = true
		}
	case "documents":
		for _, name := range []string{"kind", "state", "limit", "cursor"} {
			allowed[name] = true
		}
	case "decisions", "questions":
		for _, name := range []string{"state", "limit", "cursor"} {
			allowed[name] = true
		}
	case "progress", "bindings", "history":
		allowed["limit"] = true
		allowed["cursor"] = true
	case "resolve":
		allowed["workspace_key"] = true
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return query, memory.Problem(400, "invalid_request", "Invalid query encoding.")
	}
	for name, items := range values {
		if !allowed[name] || len(items) != 1 {
			return query, memory.Problem(400, "invalid_request", "Unknown or repeated query parameter.")
		}
	}
	query.Search = values.Get("q")
	query.Archived = values.Get("archived")
	query.Kind = values.Get("kind")
	query.State = values.Get("state")
	query.Cursor = values.Get("cursor")
	query.WorkspaceKey = values.Get("workspace_key")
	if raw, exists := values["limit"]; exists {
		limit, err := strconv.Atoi(raw[0])
		if err != nil || limit < 1 || limit > 100 {
			return query, memory.Problem(400, "invalid_request", "Limit must be an integer between 1 and 100.")
		}
		query.Limit = limit
	}
	return query, nil
}
func registerMemory(mux *http.ServeMux, options Options) {
	routes := map[string]string{
		"GET /api/v1/projects":                                    "projects",
		"GET /api/v1/projects/{project_id}":                       "project",
		"GET /api/v1/projects/{project_id}/context":               "context",
		"GET /api/v1/projects/{project_id}/documents":             "documents",
		"GET /api/v1/projects/{project_id}/documents/{target_id}": "document",
		"GET /api/v1/projects/{project_id}/decisions":             "decisions",
		"GET /api/v1/projects/{project_id}/questions":             "questions",
		"GET /api/v1/projects/{project_id}/progress":              "progress",
		"GET /api/v1/projects/{project_id}/workspace-bindings":    "bindings",
		"GET /api/v1/projects/{project_id}/history":               "history",
		"GET /api/v1/projects/{project_id}/history/{target_id}":   "checkpoint",
		"GET /api/v1/workspace-bindings/resolve":                  "resolve",
		"POST /api/v1/projects":                                   "create",
		"POST /api/v1/projects/{project_id}/checkpoints":          "update",
	}
	for route, resource := range routes {
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			caller, ok := authenticate(w, r, options.Access)
			if !ok {
				return
			}
			if options.Memory == nil {
				fail(w, 503, "service_unavailable", "The project memory service is unavailable.")
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			if resource == "create" || resource == "update" {
				// Writes accept no query arguments; targets and revision are explicit.
				if r.URL.RawQuery != "" {
					fail(w, 400, "invalid_request", "Writes do not accept query parameters.")
					return
				}
				key, err := singleKey(r)
				if err != nil {
					memoryFailure(w, err)
					return
				}
				var saved memory.WriteResult
				if resource == "create" {
					var request memory.CreateRequest
					if !memoryJSON(w, r, &request) {
						return
					}
					saved, err = options.Memory.Create(ctx, caller.Identity, key, request)
				} else {
					var request memory.UpdateRequest
					if !memoryJSON(w, r, &request) {
						return
					}
					saved, err = options.Memory.Update(ctx, caller.Identity, r.PathValue("project_id"), key, request)
				}
				if err != nil {
					memoryFailure(w, err)
					return
				}
				if saved.Replayed {
					w.Header().Set("Idempotency-Replayed", "true")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(saved.Status)
				_, _ = w.Write(saved.Body)
				return
			}
			query, err := memoryQuery(r, resource)
			if err != nil {
				memoryFailure(w, err)
				return
			}
			data, err := options.Memory.Read(ctx, caller.Identity, query)
			if err != nil {
				memoryFailure(w, err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			writeJSON(w, data)
		})
	}
}
