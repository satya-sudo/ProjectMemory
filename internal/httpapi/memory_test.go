package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"projectmap/internal/access"
	"projectmap/internal/memory"
	"strings"
	"testing"
)

const apiProjectID = "11111111-1111-4111-8111-111111111111"
const apiTargetID = "22222222-2222-4222-8222-222222222222"

type memoryBackend struct {
	query  memory.Query
	write  memory.WriteRequest
	actor  access.Identity
	calls  int
	err    error
	replay bool
}

func (b *memoryBackend) Read(_ context.Context, actor access.Identity, q memory.Query) (memory.Envelope, error) {
	b.query = q
	b.actor = actor
	b.calls++
	return memory.Envelope{Data: memory.Record{"resource": q.Resource}, Meta: memory.Record{"project_revision": int64(3)}}, b.err
}
func (b *memoryBackend) Write(_ context.Context, actor access.Identity, q memory.WriteRequest) (memory.WriteResult, error) {
	b.write = q
	b.actor = actor
	b.calls++
	return memory.WriteResult{Status: 201, Body: json.RawMessage(`{"data":{"saved":true}}`), Replayed: b.replay}, b.err
}
func projectHandler(backend *memoryBackend) (http.Handler, string, string) {
	agent, dashboard := strings.Repeat("a", 43), strings.Repeat("d", 43)
	manager := access.New(&identityResolver{}, map[[32]byte]string{sha256.Sum256([]byte(agent)): "agent", sha256.Sum256([]byte(dashboard)): "dashboard"})
	return Handler(fakeDB{}, "127.0.0.1:8080", Options{Access: manager, Memory: &memory.Service{Backend: backend}}), agent, dashboard
}
func captureBody() string {
	return `{"title":"Memory","overview":{"title":"Idea","content":"Keep ideas"},"checkpoint":{"summary":"Capture idea"}}`
}
func updateBody() string {
	return `{"expected_revision":3,"summary":"Save plan","operations":[{"ref":"plan","type":"document.create","data":{"kind":"feature_plan","title":"MVP","content":"Local capture"}}]}`
}
func TestProjectRoutesResolveTypedQueries(t *testing.T) {
	for _, tc := range []struct{ path, resource string }{
		{"/api/v1/projects?q=memory&archived=all&limit=5", "projects"},
		{"/api/v1/projects/" + apiProjectID, "project"},
		{"/api/v1/projects/" + apiProjectID + "/context", "context"},
		{"/api/v1/projects/" + apiProjectID + "/documents?kind=architecture&state=draft", "documents"},
		{"/api/v1/projects/" + apiProjectID + "/documents/" + apiTargetID, "document"},
		{"/api/v1/projects/" + apiProjectID + "/decisions?state=accepted", "decisions"},
		{"/api/v1/projects/" + apiProjectID + "/questions?state=open", "questions"},
		{"/api/v1/projects/" + apiProjectID + "/progress", "progress"},
		{"/api/v1/projects/" + apiProjectID + "/workspace-bindings", "bindings"},
		{"/api/v1/projects/" + apiProjectID + "/history", "history"},
		{"/api/v1/projects/" + apiProjectID + "/history/" + apiTargetID, "checkpoint"},
		{"/api/v1/workspace-bindings/resolve?workspace_key=%2Ftest%2Fproject", "resolve"},
	} {
		t.Run(tc.resource, func(t *testing.T) {
			backend := &memoryBackend{}
			handler, agent, _ := projectHandler(backend)
			res := request(handler, "GET", tc.path, "", agent, nil, nil)
			if res.Code != 200 || backend.query.Resource != tc.resource || backend.actor.Profile.ID != "owner" {
				t.Fatalf("route failed: %d %s", res.Code, res.Body.String())
			}
			if strings.Contains(tc.path, apiProjectID) && backend.query.ProjectID != apiProjectID {
				t.Fatal("lost project scope")
			}
			if strings.Contains(tc.path, apiTargetID) && backend.query.TargetID != apiTargetID {
				t.Fatal("lost target scope")
			}
		})
	}
}
func TestProjectWriteContractsAndReplayHeaders(t *testing.T) {
	backend := &memoryBackend{}
	handler, agent, _ := projectHandler(backend)
	res := request(handler, "POST", "/api/v1/projects", captureBody(), agent, nil, map[string]string{"Idempotency-Key": "capture-key"})
	if res.Code != 201 || backend.write.Create == nil || backend.write.Key != "capture-key" || backend.write.Hash == "" {
		t.Fatalf("capture failed: %d %s", res.Code, res.Body.String())
	}
	backend.replay = true
	res = request(handler, "POST", "/api/v1/projects/"+apiProjectID+"/checkpoints", updateBody(), agent, nil, map[string]string{"Idempotency-Key": "checkpoint-key"})
	if res.Code != 201 || res.Header().Get("Idempotency-Replayed") != "true" || backend.write.Update.ExpectedRevision != 3 || backend.write.ProjectID != apiProjectID {
		t.Fatal("checkpoint contract lost")
	}
}
func TestInvalidProjectRequestsNeverReachPersistence(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body, key string
		status                        int
	}{
		{"missing retry key", "POST", "/api/v1/projects", captureBody(), "", 400},
		{"duplicate JSON", "POST", "/api/v1/projects", `{"title":"a","title":"b"}`, "key", 400},
		{"unknown owner", "POST", "/api/v1/projects", `{"title":"a","profile_id":"spoof"}`, "key", 400},
		{"case alias", "POST", "/api/v1/projects", `{"Title":"a"}`, "key", 400},
		{"missing overview", "POST", "/api/v1/projects", `{"title":"a","checkpoint":{"summary":"capture"}}`, "key", 422},
		{"oversize body", "POST", "/api/v1/projects", strings.Repeat("x", maxMemoryBody+1), "key", 413},
		{"write query", "POST", "/api/v1/projects?q=ignored", captureBody(), "key", 400},
		{"bad identifier", "GET", "/api/v1/projects/invalid", "", "", 400},
		{"query unknown", "GET", "/api/v1/projects?profile_id=spoof", "", "", 400},
		{"query repeated", "GET", "/api/v1/projects?q=a&q=b", "", "", 400},
		{"query limit", "GET", "/api/v1/projects?limit=0", "", "", 400},
		{"query archive", "GET", "/api/v1/projects?archived=yes", "", "", 400},
		{"query state", "GET", "/api/v1/projects/" + apiProjectID + "/decisions?state=building", "", "", 400},
		{"query encoding", "GET", "/api/v1/projects?q=%zz", "", "", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &memoryBackend{}
			handler, agent, _ := projectHandler(backend)
			res := request(handler, tc.method, tc.path, tc.body, agent, nil, map[string]string{"Idempotency-Key": tc.key})
			if res.Code != tc.status || backend.calls != 0 {
				t.Fatalf("expected %d before persistence, got %d: %s", tc.status, res.Code, res.Body.String())
			}
		})
	}
}
func TestProjectWritesRequireSessionCSRF(t *testing.T) {
	backend := &memoryBackend{}
	handler, _, dashboard := projectHandler(backend)
	cookie := login(t, handler, dashboard)
	headers := map[string]string{"Idempotency-Key": "create", "Origin": localOrigin}
	res := request(handler, "POST", "/api/v1/projects", captureBody(), "", cookie, headers)
	if res.Code != 403 || backend.calls != 0 {
		t.Fatal("cookie mutation bypassed CSRF")
	}
	headers["X-CSRF-Token"] = csrfFromMe(t, handler, cookie)
	res = request(handler, "POST", "/api/v1/projects", captureBody(), "", cookie, headers)
	if res.Code != 201 || backend.actor.Client.ID != "dashboard" {
		t.Fatal("valid session mutation failed")
	}
}
func TestMemoryErrorsPreserveSafeCodesAndDetails(t *testing.T) {
	backend := &memoryBackend{err: &memory.Error{Status: 409, Code: "revision_conflict", Message: "Reload project.", Details: memory.Record{"current_revision": 4}}}
	handler, agent, _ := projectHandler(backend)
	res := request(handler, "POST", "/api/v1/projects/"+apiProjectID+"/checkpoints", updateBody(), agent, nil, map[string]string{"Idempotency-Key": "edit"})
	if res.Code != 409 || !strings.Contains(res.Body.String(), `"current_revision":4`) {
		t.Fatal("conflict details missing")
	}
	backend.err = errors.New("database password should not appear")
	res = request(handler, "GET", "/api/v1/projects", "", agent, nil, nil)
	if res.Code != 503 || strings.Contains(res.Body.String(), "password") {
		t.Fatal("unsafe database error returned")
	}
	backend.calls = 0
	res = request(handler, "GET", "/api/v1/projects", "", "", nil, nil)
	if res.Code != 401 || backend.calls != 0 {
		t.Fatal("unauthenticated request reached persistence")
	}
}
