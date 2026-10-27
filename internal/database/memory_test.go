package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"projectmap/internal/access"
	"projectmap/internal/memory"
	"sync"
	"testing"
)

func memoryFixture(t *testing.T) (context.Context, *Store, *memory.Service, access.Identity) {
	t.Helper()
	ctx, store := testStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	ids, err := store.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := store.ResolveClient(ctx, ids.AgentClientID)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, store, &memory.Service{Backend: NewMemoryStore(store, "http://127.0.0.1:8080")}, actor
}
func memoryInput() memory.CreateRequest {
	content := "## Problem\nIdeas get lost."
	return memory.CreateRequest{Title: "Project Memory", Summary: "Save project ideas", Overview: &memory.OverviewInput{Title: "Overview", Content: &content}, Checkpoint: memory.CheckpointInput{Summary: "Track idea"}}
}
func writeData(t *testing.T, result memory.WriteResult) memory.Record {
	t.Helper()
	var decoded struct {
		Data memory.Record `json:"data"`
	}
	if err := json.Unmarshal(result.Body, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded.Data
}
func projectFrom(t *testing.T, result memory.WriteResult) memory.Record {
	t.Helper()
	return memory.Record(writeData(t, result)["project"].(map[string]any))
}
func testOperation(ref, kind, id string, data memory.Record) memory.Operation {
	return memory.Operation{Ref: ref, Type: kind, ID: id, Data: operationData(data)}
}
func requireMemoryCode(t *testing.T, err error, code string) {
	t.Helper()
	var problem *memory.Error
	if !errors.As(err, &problem) || problem.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}
func TestMemoryCaptureUpdatesHistoryAndReplay(t *testing.T) {
	ctx, _, service, actor := memoryFixture(t)
	input := memoryInput()
	input.Questions = []memory.QuestionInput{{Question: "Which features first?"}}
	input.Decisions = []memory.DecisionInput{{Title: "Deployment", Statement: "Local"}}
	input.Workspace = &memory.WorkspaceInput{WorkspaceKey: "/test/memory"}
	created, err := service.Create(ctx, actor, "capture", input)
	if err != nil {
		t.Fatal(err)
	}
	project := projectFrom(t, created)
	id := project["id"].(string)
	replay, err := service.Create(ctx, actor, "capture", input)
	if err != nil || !replay.Replayed || !memory.Equal(writeData(t, created), writeData(t, replay)) {
		t.Fatalf("capture replay failed: %v", err)
	}
	changed := input
	changed.Title = "Another title"
	_, err = service.Create(ctx, actor, "capture", changed)
	requireMemoryCode(t, err, "idempotency_key_reused")
	data := writeData(t, created)
	ids := data["created"].(map[string]any)
	decision := ids["decision_ids"].([]any)[0].(string)
	overview := ids["overview_document_id"].(string)
	update := memory.UpdateRequest{ExpectedRevision: 1, Summary: "Save architecture and decision", Operations: []memory.Operation{
		testOperation("architecture", "document.create", "", memory.Record{"kind": "architecture", "title": "System", "content": "React → Go → PostgreSQL"}),
		testOperation("decision", "decision.set_state", decision, memory.Record{"state": "accepted", "acceptance_reference": "conversation:user-confirmed"}),
		testOperation("next", "project.update", "", memory.Record{"next_step": "Implement the API"}),
	}}
	saved, err := service.Update(ctx, actor, id, "checkpoint", update)
	if err != nil {
		t.Fatal(err)
	}
	if projectFrom(t, saved)["revision"] != float64(2) {
		t.Fatal("checkpoint did not advance revision once")
	}
	replay, err = service.Update(ctx, actor, id, "checkpoint", update)
	if err != nil || !replay.Replayed {
		t.Fatalf("stale successful retry failed: %v", err)
	}
	_, err = service.Update(ctx, actor, id, "new-stale-key", update)
	requireMemoryCode(t, err, "revision_conflict")
	packet, err := service.Read(ctx, actor, memory.Query{Resource: "context", ProjectID: id})
	if err != nil {
		t.Fatal(err)
	}
	contextData := packet.Data.(memory.Record)
	if len(contextData["accepted_decisions"].([]memory.Record)) != 1 || contextData["project"].(memory.Record)["next_step"] != "Implement the API" {
		t.Fatal("current context incomplete")
	}
	doc, err := service.Read(ctx, actor, memory.Query{Resource: "document", ProjectID: id, TargetID: overview})
	if err != nil || doc.Data.(memory.Record)["content"] == nil {
		t.Fatal("full document unavailable")
	}
	binding, err := service.Read(ctx, actor, memory.Query{Resource: "resolve", WorkspaceKey: "/test/memory"})
	if err != nil || binding.Data.(memory.Record)["project"].(memory.Record)["id"] != id {
		t.Fatal("workspace not resolved")
	}
	history, err := service.Read(ctx, actor, memory.Query{Resource: "history", ProjectID: id})
	if err != nil {
		t.Fatal(err)
	}
	rows := history.Data.([]memory.Record)
	if len(rows) != 2 {
		t.Fatal("duplicate/missing history")
	}
	detail, err := service.Read(ctx, actor, memory.Query{Resource: "checkpoint", ProjectID: id, TargetID: rows[0]["id"].(string)})
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Data.(memory.Record)["change_items"].([]memory.Record)) != 3 {
		t.Fatal("atomic checkpoint history incomplete")
	}
	other := actor
	other.Profile.ID = newID()
	_, err = service.Read(ctx, other, memory.Query{Resource: "project", ProjectID: id})
	requireMemoryCode(t, err, "not_found")
}
func TestMemoryRollbackArchiveAndCrossProjectTargets(t *testing.T) {
	ctx, store, service, actor := memoryFixture(t)
	created, err := service.Create(ctx, actor, "first", memoryInput())
	if err != nil {
		t.Fatal(err)
	}
	id := projectFrom(t, created)["id"].(string)
	otherInput := memoryInput()
	otherInput.Workspace = &memory.WorkspaceInput{WorkspaceKey: "/test/occupied"}
	other, err := service.Create(ctx, actor, "other", otherInput)
	if err != nil {
		t.Fatal(err)
	}
	failed := memory.UpdateRequest{ExpectedRevision: 1, Summary: "Rollback all writes", Operations: []memory.Operation{
		testOperation("new", "document.create", "", memory.Record{"kind": "feature_plan", "title": "Must roll back", "content": "Draft"}),
		testOperation("binding", "workspace.bind", "", memory.Record{"workspace_key": "/test/occupied"}),
	}}
	_, err = service.Update(ctx, actor, id, "failed-batch", failed)
	requireMemoryCode(t, err, "workspace_already_bound")
	read, err := service.Read(ctx, actor, memory.Query{Resource: "project", ProjectID: id})
	if err != nil {
		t.Fatal(err)
	}
	if memory.Int(read.Data.(memory.Record)["revision"]) != 1 {
		t.Fatal("failed transaction advanced revision")
	}
	var count int
	if err := store.Pool.QueryRow(ctx, "SELECT count(*) FROM documents WHERE project_id=$1", id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("partial document committed: %d %v", count, err)
	}
	if err := store.Pool.QueryRow(ctx, "SELECT count(*) FROM write_requests WHERE idempotency_key='failed-batch'").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed request recorded as success")
	}
	foreign := writeData(t, other)["created"].(map[string]any)["overview_document_id"].(string)
	_, err = service.Update(ctx, actor, id, "foreign", memory.UpdateRequest{ExpectedRevision: 1, Summary: "Wrong target", Operations: []memory.Operation{testOperation("edit", "document.update", foreign, memory.Record{"content": "No"})}})
	requireMemoryCode(t, err, "not_found")
	_, err = service.Update(ctx, actor, id, "archive", memory.UpdateRequest{ExpectedRevision: 1, Summary: "Archive", Operations: []memory.Operation{testOperation("archive", "project.archive", "", nil)}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Update(ctx, actor, id, "blocked", memory.UpdateRequest{ExpectedRevision: 2, Summary: "Cannot edit archived", Operations: []memory.Operation{testOperation("edit", "project.update", "", memory.Record{"title": "No"})}})
	requireMemoryCode(t, err, "project_archived")
	_, err = service.Update(ctx, actor, id, "unarchive", memory.UpdateRequest{ExpectedRevision: 2, Summary: "Resume", Operations: []memory.Operation{testOperation("resume", "project.unarchive", "", nil)}})
	if err != nil {
		t.Fatal(err)
	}
}
func TestMemoryPaginationContextCoverageAndStaleCursors(t *testing.T) {
	ctx, _, service, actor := memoryFixture(t)
	input := memoryInput()
	for i := range 20 {
		input.Decisions = append(input.Decisions, memory.DecisionInput{Title: fmt.Sprint("Decision ", i), Statement: "Proposed"})
	}
	created, err := service.Create(ctx, actor, "create", input)
	if err != nil {
		t.Fatal(err)
	}
	id := projectFrom(t, created)["id"].(string)
	operations := []memory.Operation{}
	for i := range 21 {
		operations = append(operations, testOperation(fmt.Sprint("doc", i), "document.create", "", memory.Record{"kind": "note", "title": fmt.Sprint("Note ", i), "content": "Body"}))
	}
	for i := range 5 {
		operations = append(operations, testOperation(fmt.Sprint("decision", i), "decision.create", "", memory.Record{"title": fmt.Sprint("Extra ", i), "statement": "Proposed"}))
	}
	for i := range 6 {
		operations = append(operations, testOperation(fmt.Sprint("progress", i), "progress.create", "", memory.Record{"summary": fmt.Sprint("Outcome ", i)}))
	}
	_, err = service.Update(ctx, actor, id, "bulk", memory.UpdateRequest{ExpectedRevision: 1, Summary: "Add plans", Operations: operations})
	if err != nil {
		t.Fatal(err)
	}
	decisions, err := service.Read(ctx, actor, memory.Query{Resource: "decisions", ProjectID: id, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	accepts := []memory.Operation{}
	for i, record := range decisions.Data.([]memory.Record)[:22] {
		accepts = append(accepts, testOperation(fmt.Sprint("accept", i), "decision.set_state", record["id"].(string), memory.Record{"state": "accepted", "acceptance_reference": "user:confirmed"}))
	}
	_, err = service.Update(ctx, actor, id, "accept", memory.UpdateRequest{ExpectedRevision: 2, Summary: "Agree decisions", Operations: accepts})
	if err != nil {
		t.Fatal(err)
	}
	packet, err := service.Read(ctx, actor, memory.Query{Resource: "context", ProjectID: id})
	if err != nil {
		t.Fatal(err)
	}
	data := packet.Data.(memory.Record)
	coverage := data["coverage"].(memory.Record)
	accepted := coverage["accepted_decisions"].(memory.Record)
	if memory.Int(accepted["total"]) != 22 || len(data["accepted_decisions"].([]memory.Record)) != 20 || accepted["next_url"] == nil {
		t.Fatal("accepted decisions silently truncated")
	}
	cursor := *(accepted["next_cursor"].(*string))
	remaining, err := service.Read(ctx, actor, memory.Query{Resource: "decisions", ProjectID: id, State: "accepted", Limit: 20, Cursor: cursor})
	if err != nil || len(remaining.Data.([]memory.Record)) != 2 {
		t.Fatalf("context continuation failed: %v", err)
	}
	progress := coverage["recent_progress"].(memory.Record)
	if memory.Int(progress["total"]) != 6 || len(data["recent_progress"].([]memory.Record)) != 5 || progress["limit"] != 5 {
		t.Fatal("progress coverage incorrect")
	}
	docs, err := service.Read(ctx, actor, memory.Query{Resource: "documents", ProjectID: id, Limit: 1})
	if err != nil || docs.Page.NextCursor == nil {
		t.Fatal("document cursor missing")
	}
	_, err = service.Update(ctx, actor, id, "change", memory.UpdateRequest{ExpectedRevision: 3, Summary: "Next step", Operations: []memory.Operation{testOperation("next", "project.update", "", memory.Record{"next_step": "Continue"})}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Read(ctx, actor, memory.Query{Resource: "documents", ProjectID: id, Limit: 1, Cursor: *docs.Page.NextCursor})
	requireMemoryCode(t, err, "cursor_stale")
}
func TestMemoryConcurrentRetriesAndRevisionConflicts(t *testing.T) {
	ctx, _, service, actor := memoryFixture(t)
	var group sync.WaitGroup
	start := make(chan struct{})
	results := make(chan memory.WriteResult, 8)
	failures := make(chan error, 8)
	for range 8 {
		group.Go(func() {
			<-start
			saved, err := service.Create(ctx, actor, "shared-key", memoryInput())
			if err != nil {
				failures <- err
				return
			}
			results <- saved
		})
	}
	close(start)
	group.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	id := ""
	created, replayed := 0, 0
	for saved := range results {
		current := projectFrom(t, saved)["id"].(string)
		if id == "" {
			id = current
		} else if id != current {
			t.Fatal("duplicate projects for one retry key")
		}
		if saved.Replayed {
			replayed++
		} else {
			created++
		}
	}
	if created != 1 || replayed != 7 {
		t.Fatalf("create/replay counts %d/%d", created, replayed)
	}
	success := make(chan bool, 8)
	conflicts := make(chan error, 8)
	start = make(chan struct{})
	for i := range 8 {
		group.Go(func() {
			<-start
			_, err := service.Update(ctx, actor, id, fmt.Sprint("update", i), memory.UpdateRequest{ExpectedRevision: 1, Summary: "Competing edit", Operations: []memory.Operation{testOperation("next", "project.update", "", memory.Record{"next_step": fmt.Sprint("Task ", i)})}})
			if err != nil {
				conflicts <- err
			} else {
				success <- true
			}
		})
	}
	close(start)
	group.Wait()
	close(success)
	close(conflicts)
	if len(success) != 1 || len(conflicts) != 7 {
		t.Fatalf("concurrent writes did not serialize: %d/%d", len(success), len(conflicts))
	}
	for err := range conflicts {
		requireMemoryCode(t, err, "revision_conflict")
	}
}

func TestMemoryLiteralSearchAndCoalescedAcceptance(t *testing.T) {
	ctx, _, service, actor := memoryFixture(t)
	input := memoryInput()
	input.Title = "Idea 100%_local"
	created, err := service.Create(ctx, actor, "literal", input)
	if err != nil {
		t.Fatal(err)
	}
	id := projectFrom(t, created)["id"].(string)
	other := memoryInput()
	other.Title = "Idea 100xxlocal"
	if _, err := service.Create(ctx, actor, "other", other); err != nil {
		t.Fatal(err)
	}
	listing, err := service.Read(ctx, actor, memory.Query{Resource: "projects", Search: "%_"})
	if err != nil || len(listing.Data.([]memory.Record)) != 1 {
		t.Fatalf("literal wildcard search failed: %v", err)
	}
	overview := writeData(t, created)["created"].(map[string]any)["overview_document_id"].(string)
	_, err = service.Update(ctx, actor, id, "initial-accept", memory.UpdateRequest{ExpectedRevision: 1, Summary: "Accept overview", Operations: []memory.Operation{testOperation("accept", "document.set_state", overview, memory.Record{"state": "accepted", "acceptance_reference": "user:first"})}})
	if err != nil {
		t.Fatal(err)
	}
	revised, err := service.Update(ctx, actor, id, "revise-and-accept", memory.UpdateRequest{ExpectedRevision: 2, Summary: "Accept revised overview", Operations: []memory.Operation{
		testOperation("edit", "document.update", overview, memory.Record{"content": "New agreed overview"}),
		testOperation("accept", "document.set_state", overview, memory.Record{"state": "accepted", "acceptance_reference": "user:second"}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := writeData(t, revised)["checkpoint"].(map[string]any)["id"].(string)
	history, err := service.Read(ctx, actor, memory.Query{Resource: "checkpoint", ProjectID: id, TargetID: checkpoint})
	if err != nil {
		t.Fatal(err)
	}
	changes := history.Data.(memory.Record)["change_items"].([]memory.Record)
	if len(changes) != 2 {
		t.Fatal("paired document update produced duplicate audit entries")
	}
	document := changes[1]
	before := document["before_data"].(map[string]any)
	after := document["after_data"].(map[string]any)
	metadata := document["metadata"].(map[string]any)
	if before["state"] != "accepted" || after["state"] != "accepted" || after["content"] != "New agreed overview" || metadata["acceptance_reference"] != "user:second" {
		t.Fatal("coalesced snapshots or acceptance metadata incorrect")
	}
}
