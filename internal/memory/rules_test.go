package memory

import (
	"context"
	"encoding/json"
	"errors"
	"projectmap/internal/access"
	"strings"
	"testing"
	"time"
)

const projectID = "11111111-1111-4111-8111-111111111111"
const targetID = "22222222-2222-4222-8222-222222222222"

func operation(kind string, data Record) Operation {
	raw := map[string]json.RawMessage{}
	for key, value := range data {
		body, _ := json.Marshal(value)
		raw[key] = body
	}
	op := Operation{Type: kind, Ref: kind, Data: raw}
	if Existing(kind) {
		op.ID = targetID
	}
	return op
}
func checkCode(t *testing.T, err error, code string) {
	t.Helper()
	var problem *Error
	if !errors.As(err, &problem) || problem.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}
func baseRecord(state string) Record {
	return Record{"id": targetID, "project_id": projectID, "state": state, "title": "Plan", "content": "Original", "statement": "Use Go", "rationale": "Local", "acceptance_reference": "conversation:agreed", "created_at": time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC), "updated_at": time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC), "last_changed_revision": int64(1)}
}
func TestAcceptedContentResetsAndPreservesPriorSnapshot(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct{ kind, field, expected string }{{"document.update", "content", "draft"}, {"decision.update", "statement", "proposed"}} {
		t.Run(tc.kind, func(t *testing.T) {
			before := baseRecord("accepted")
			original := canonical(before)
			after, _, err := Apply(operation(tc.kind, Record{tc.field: "Updated"}), before, targetID, projectID, 2, now)
			if err != nil || after["state"] != tc.expected || Int(after["last_changed_revision"]) != 2 {
				t.Fatalf("accepted content did not reset: %v", err)
			}
			if string(canonical(before)) != string(original) {
				t.Fatal("prior snapshot was mutated")
			}
			if tc.kind == "decision.update" && after["acceptance_reference"] != nil {
				t.Fatal("stale acceptance retained")
			}
		})
	}
}
func TestStateTransitionsAndAcceptanceEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, kind, from, to, code string
		reference                  bool
	}{
		{"accept document", "document.set_state", "draft", "accepted", "", true},
		{"missing document acceptance", "document.set_state", "draft", "accepted", "validation_failed", false},
		{"accept decision", "decision.set_state", "proposed", "accepted", "", true},
		{"missing decision acceptance", "decision.set_state", "proposed", "accepted", "validation_failed", false},
		{"superseded document", "document.set_state", "superseded", "draft", "invalid_transition", false},
		{"rejected decision", "decision.set_state", "rejected", "proposed", "invalid_transition", false},
		{"accepted cannot reject", "decision.set_state", "accepted", "rejected", "invalid_transition", false},
		{"same state", "document.set_state", "draft", "draft", "no_change", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := Record{"state": tc.to}
			if tc.reference {
				data["acceptance_reference"] = "conversation:user-agreed"
			}
			after, metadata, err := Apply(operation(tc.kind, data), baseRecord(tc.from), targetID, projectID, 2, time.Now())
			if tc.code != "" {
				checkCode(t, err, tc.code)
				return
			}
			if err != nil || after["state"] != tc.to || metadata["acceptance_reference"] == nil {
				t.Fatalf("valid acceptance failed: %v", err)
			}
			if tc.kind == "decision.set_state" && after["acceptance_reference"] != data["acceptance_reference"] {
				t.Fatal("decision acceptance not stored")
			}
		})
	}
}
func TestQuestionResolutionAndReopen(t *testing.T) {
	before := Record{"id": targetID, "question": "Which database?", "state": "open", "resolution": nil}
	if _, _, err := Apply(operation("question.update", Record{"state": "resolved"}), before, targetID, projectID, 2, time.Now()); err == nil {
		t.Fatal("resolved without answer")
	}
	resolved, _, err := Apply(operation("question.update", Record{"state": "resolved", "resolution": "PostgreSQL"}), before, targetID, projectID, 2, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Apply(operation("question.update", Record{"question": "Different?"}), resolved, targetID, projectID, 3, time.Now()); err == nil {
		t.Fatal("changed resolved question without reopening")
	}
	opened, _, err := Apply(operation("question.update", Record{"question": "Different?", "state": "open", "resolution": nil}), resolved, targetID, projectID, 3, time.Now())
	if err != nil || opened["state"] != "open" || opened["resolution"] != nil {
		t.Fatal("question did not reopen")
	}
}
func TestProgressEvidenceResetsAfterOutcomeChanges(t *testing.T) {
	before := Record{"id": targetID, "summary": "Tests passed", "verification": "verified", "evidence": []Evidence{{Kind: "test", Reference: "test:original", Description: ""}}}
	after, _, err := Apply(operation("progress.update", Record{"summary": "Different outcome"}), before, targetID, projectID, 2, time.Now())
	if err != nil || after["verification"] != "reported" || !Equal(after["evidence"], []Evidence{}) {
		t.Fatal("changed outcome retained evidence")
	}
	if _, _, err := Apply(operation("progress.create", Record{"summary": "Done", "verification": "verified"}), nil, targetID, projectID, 1, time.Now()); err == nil {
		t.Fatal("verified progress without evidence")
	}
	if _, _, err := Apply(operation("progress.create", Record{"summary": "Done", "verification": "verified", "evidence": []Evidence{{Kind: "test", Reference: "run:42"}}}), nil, targetID, projectID, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
}
func TestOperationShapeAndBatchValidation(t *testing.T) {
	valid := operation("document.update", Record{"content": "Changed"})
	state := operation("document.set_state", Record{"state": "accepted", "acceptance_reference": "user:agreed"})
	base := UpdateRequest{ExpectedRevision: 1, Summary: "Save plans", Operations: []Operation{valid, state}}
	if err := ValidateUpdate(base); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		ops  []Operation
		code string
	}{
		{"duplicate update", []Operation{valid, {Ref: "other", Type: valid.Type, ID: targetID, Data: valid.Data}}, "validation_failed"},
		{"state before update", []Operation{state, valid}, "validation_failed"},
		{"duplicate ref", []Operation{valid, {Ref: valid.Ref, Type: state.Type, ID: targetID, Data: state.Data}}, "validation_failed"},
		{"archive with edit", []Operation{valid, operation("project.archive", nil)}, "validation_failed"},
		{"unknown field", []Operation{operation("document.update", Record{"profile_id": "spoof"})}, "invalid_request"},
		{"null content", []Operation{operation("document.update", Record{"content": nil})}, "invalid_request"},
		{"missing target", []Operation{{Ref: "edit", Type: "document.update", Data: valid.Data}}, "invalid_request"},
		{"oversize content", []Operation{operation("document.update", Record{"content": strings.Repeat("界", 90000)})}, "validation_failed"},
		{"no fields", []Operation{operation("document.update", nil)}, "validation_failed"},
		{"untrusted type", []Operation{operation("execute.sql", Record{})}, "invalid_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := base
			copy.Operations = tc.ops
			checkCode(t, ValidateUpdate(copy), tc.code)
		})
	}
}
func TestNoopDoesNotResetAcceptedState(t *testing.T) {
	before := baseRecord("accepted")
	_, _, err := Apply(operation("document.update", Record{"content": "Original"}), before, targetID, projectID, 2, time.Now())
	checkCode(t, err, "no_change")
	if before["state"] != "accepted" {
		t.Fatal("no-op changed acceptance")
	}
}
func TestWorkspaceAndCreateRequirements(t *testing.T) {
	content := "Idea"
	valid := CreateRequest{Title: "Memory", Overview: &OverviewInput{Title: "Overview", Content: &content}, Checkpoint: CheckpointInput{Summary: "Capture"}}
	if err := ValidateCreate(&valid); err != nil {
		t.Fatal(err)
	}
	if valid.Decisions == nil || valid.Questions == nil {
		t.Fatal("optional arrays not normalized")
	}
	for _, path := range []string{"relative/path", "/project/../other", "/project/"} {
		bad := valid
		bad.Workspace = &WorkspaceInput{WorkspaceKey: path}
		if ValidateCreate(&bad) == nil {
			t.Fatalf("noncanonical workspace accepted: %s", path)
		}
	}
	bad := valid
	bad.Overview = nil
	if ValidateCreate(&bad) == nil {
		t.Fatal("missing overview accepted")
	}
	bad = valid
	bad.Title = " \n"
	if ValidateCreate(&bad) == nil {
		t.Fatal("blank title accepted")
	}
}
func TestCursorScopeTamperingAndStaleness(t *testing.T) {
	codec := NewCursors()
	q := Query{Resource: "documents", ProjectID: projectID, Limit: 20}
	scope := Scope("owner", q)
	token := codec.Encode(Cursor{Scope: scope, Revision: 4, ID: targetID, Time: time.Now().UTC().Format(time.RFC3339Nano)})
	cursor, err := codec.Decode(token, scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckCursorRevision(cursor, 4); err != nil {
		t.Fatal(err)
	}
	checkCode(t, CheckCursorRevision(cursor, 5), "cursor_stale")
	for _, test := range []struct{ token, scope string }{{token, "another-profile"}, {token + "x", scope}, {"untrusted", scope}} {
		_, err := codec.Decode(test.token, test.scope)
		checkCode(t, err, "invalid_cursor")
	}
	other := q
	other.Kind = "architecture"
	if _, err := codec.Decode(token, Scope("owner", other)); err == nil {
		t.Fatal("filter-changing cursor accepted")
	}
	if _, err := NewCursors().Decode(token, scope); err == nil {
		t.Fatal("cursor survived a restart with another signing key")
	}
}
func TestPreviewPreservesUnicode(t *testing.T) {
	value, truncated := Preview("ab界cd", 4)
	if value != "ab" || !truncated {
		t.Fatalf("invalid Unicode preview %q", value)
	}
	value, truncated = Preview("界", 3)
	if value != "界" || truncated {
		t.Fatal("exact-boundary preview truncated")
	}
}

type capturingBackend struct {
	request WriteRequest
	query   Query
	calls   int
}

func (b *capturingBackend) Write(_ context.Context, _ access.Identity, r WriteRequest) (WriteResult, error) {
	b.request = r
	b.calls++
	return WriteResult{Status: 201, Body: json.RawMessage(`{"data":{}}`)}, nil
}
func (b *capturingBackend) Read(_ context.Context, _ access.Identity, q Query) (Envelope, error) {
	b.query = q
	b.calls++
	return Envelope{Data: []Record{}}, nil
}
func TestServiceCanonicalHashesAndValidationBeforePersistence(t *testing.T) {
	backend := &capturingBackend{}
	service := Service{Backend: backend}
	ctx := context.Background()
	actor := access.Identity{}
	update := UpdateRequest{ExpectedRevision: 1, Summary: "Edit", Operations: []Operation{{Ref: "edit", Type: "document.update", ID: targetID, Data: map[string]json.RawMessage{"content": json.RawMessage(`"changed"`)}}}}
	if _, err := service.Update(ctx, actor, projectID, "key", update); err != nil {
		t.Fatal(err)
	}
	first := backend.request.Hash
	update.Operations[0].Data["content"] = json.RawMessage(`  "changed"  `)
	if _, err := service.Update(ctx, actor, projectID, "key", update); err != nil {
		t.Fatal(err)
	}
	if first != backend.request.Hash {
		t.Fatal("whitespace changed retry hash")
	}
	update.Operations[0].Data["content"] = json.RawMessage(`"different"`)
	if _, err := service.Update(ctx, actor, projectID, "key", update); err != nil {
		t.Fatal(err)
	}
	if first == backend.request.Hash {
		t.Fatal("changed input reused hash")
	}
	calls := backend.calls
	if _, err := service.Update(ctx, actor, projectID, "", update); err == nil {
		t.Fatal("missing retry key accepted")
	}
	if backend.calls != calls {
		t.Fatal("invalid request reached persistence")
	}
	if _, err := service.Read(ctx, actor, Query{Resource: "documents", ProjectID: projectID, State: "unknown"}); err == nil {
		t.Fatal("bad read filter reached persistence")
	}
}

func TestServiceNormalizesIDsWithoutMutatingCallerOperations(t *testing.T) {
	backend := &capturingBackend{}
	service := Service{Backend: backend}
	upper := "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
	request := UpdateRequest{ExpectedRevision: 1, Summary: "Edit", Operations: []Operation{operation("document.update", Record{"content": "Updated"})}}
	request.Operations[0].ID = upper
	if _, err := service.Update(context.Background(), access.Identity{}, projectID, "normalize", request); err != nil {
		t.Fatal(err)
	}
	if request.Operations[0].ID != upper || backend.request.Update.Operations[0].ID != strings.ToLower(upper) {
		t.Fatal("normalization changed caller-owned input or failed to normalize")
	}
}
