package database

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"projectmap/internal/access"
	"projectmap/internal/memory"
	"strings"
	"time"
)

type memoryChange struct {
	Entity, ID              string
	Before, After, Metadata memory.Record
}

func operationData(data memory.Record) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for name, value := range data {
		body, _ := json.Marshal(value)
		out[name] = body
	}
	return out
}
func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
func originFor(kind string) string {
	switch kind {
	case "dashboard":
		return "dashboard"
	case "system":
		return "system"
	default:
		return "mcp"
	}
}

// Write commits current state, audit snapshots and retry response together. The
// advisory lock is request-scoped; the row lock serializes project revisions.
func (m *MemoryStore) Write(ctx context.Context, actor access.Identity, request memory.WriteRequest) (memory.WriteResult, error) {
	var result memory.WriteResult
	err := m.store.InTx(ctx, func(tx pgx.Tx) error {
		// Hold a shared client-row lock so revocation cannot race a commit.
		var kind string
		err := tx.QueryRow(ctx, "SELECT kind FROM clients WHERE id=$1 AND profile_id=$2 AND revoked_at IS NULL FOR SHARE", actor.Client.ID, actor.Profile.ID).Scan(&kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return memory.Problem(401, "unauthorized", "Client access is no longer valid.")
		}
		if err != nil {
			return err
		}
		actor.Client.Kind = kind
		digest := sha256.Sum256([]byte(actor.Client.ID + ":" + request.Key))
		lock := int64(binary.BigEndian.Uint64(digest[:8]))
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", lock); err != nil {
			return err
		}
		var previousHash string
		err = tx.QueryRow(ctx, "SELECT request_hash,response_status,response_body FROM write_requests WHERE client_id=$1 AND idempotency_key=$2", actor.Client.ID, request.Key).Scan(&previousHash, &result.Status, &result.Body)
		if err == nil {
			if previousHash != request.Hash {
				return memory.Problem(409, "idempotency_key_reused", "The retry key was used for different input.")
			}
			result.Replayed = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var project, beforeProject memory.Record
		now := time.Now().UTC().Truncate(time.Microsecond)
		var changes []*memoryChange
		var summary string
		var source *string
		var payload memory.Record
		if request.Create != nil {
			input := request.Create
			id := newID()
			project = memory.Record{"id": id, "profile_id": actor.Profile.ID, "title": input.Title, "summary": input.Summary, "next_step": input.NextStep, "revision": int64(1), "archived_at": nil, "created_at": now, "updated_at": now}
			summary, source = input.Checkpoint.Summary, input.Checkpoint.SourceReference
			changes = append(changes, &memoryChange{Entity: "project", ID: id, After: project, Metadata: memory.Record{}})
			created := memory.Record{"decision_ids": []string{}, "question_ids": []string{}, "workspace_binding_id": nil}
			add := func(op memory.Operation) (string, error) {
				entity := memory.EntityFor(op.Type)
				target := newID()
				after, metadata, err := memory.Apply(op, nil, target, id, 1, now)
				if err != nil {
					return "", err
				}
				changes = append(changes, &memoryChange{Entity: entity, ID: target, After: after, Metadata: metadata})
				return target, nil
			}
			overview, err := add(memory.Operation{Type: "document.create", Data: operationData(memory.Record{"kind": "overview", "title": input.Overview.Title, "content": *input.Overview.Content})})
			if err != nil {
				return err
			}
			created["overview_document_id"] = overview
			for _, item := range input.Decisions {
				id, err := add(memory.Operation{Type: "decision.create", Data: operationData(memory.Record{"title": item.Title, "statement": item.Statement, "rationale": item.Rationale, "source_reference": nullableString(item.SourceReference)})})
				if err != nil {
					return err
				}
				created["decision_ids"] = append(created["decision_ids"].([]string), id)
			}
			for _, item := range input.Questions {
				id, err := add(memory.Operation{Type: "question.create", Data: operationData(memory.Record{"question": item.Question})})
				if err != nil {
					return err
				}
				created["question_ids"] = append(created["question_ids"].([]string), id)
			}
			if input.Workspace != nil {
				id, err := add(memory.Operation{Type: "workspace.bind", Data: operationData(memory.Record{"workspace_key": input.Workspace.WorkspaceKey, "repository_url": nullableString(input.Workspace.RepositoryURL)})})
				if err != nil {
					return err
				}
				created["workspace_binding_id"] = id
			}
			payload = memory.Record{"created": created}
		} else if request.Update != nil {
			input := request.Update
			var err error
			beforeProject, err = readOwnedProject(ctx, tx, actor.Profile.ID, request.ProjectID, true)
			if err != nil {
				return err
			}
			current := memory.Int(beforeProject["revision"])
			if current != input.ExpectedRevision {
				return &memory.Error{Status: 409, Code: "revision_conflict", Message: "Project changed since it was read.", Details: memory.Record{"expected_revision": input.ExpectedRevision, "current_revision": current}}
			}
			if beforeProject["archived_at"] != nil && (len(input.Operations) != 1 || input.Operations[0].Type != "project.unarchive") {
				return memory.Problem(409, "project_archived", "Unarchive this project before editing.")
			}
			project = memory.Clone(beforeProject)
			next := current + 1
			if next < 1 {
				return memory.Problem(409, "revision_conflict", "Project revision cannot advance.")
			}
			summary, source = input.Summary, input.SourceReference
			projectChange := &memoryChange{Entity: "project", ID: request.ProjectID, Before: beforeProject, Metadata: memory.Record{}}
			changes = append(changes, projectChange)
			byTarget := map[string]*memoryChange{}
			results := []memory.Record{}
			for _, op := range input.Operations {
				entity := memory.EntityFor(op.Type)
				id := strings.ToLower(op.ID)
				var before memory.Record
				if entity == "project" {
					id = request.ProjectID
					before = project
				} else if memory.Existing(op.Type) {
					if changed, ok := byTarget[entity+":"+id]; ok {
						before = changed.After
					} else {
						before, err = readMemoryEntity(ctx, tx, entity, request.ProjectID, id)
						if err != nil {
							return err
						}
					}
				} else {
					id = newID()
				}
				after, metadata, err := memory.Apply(op, before, id, request.ProjectID, next, now)
				if err != nil {
					return err
				}
				if entity == "project" {
					project = after
				} else if existing, ok := byTarget[entity+":"+id]; ok {
					existing.After = after
					for key, value := range metadata {
						existing.Metadata[key] = value
					}
				} else {
					change := &memoryChange{Entity: entity, ID: id, Before: before, After: after, Metadata: metadata}
					byTarget[entity+":"+id] = change
					changes = append(changes, change)
				}
				results = append(results, memory.Record{"ref": op.Ref, "entity_type": entity, "id": id})
			}
			project["revision"] = next
			project["updated_at"] = now
			projectChange.After = project
			payload = memory.Record{"results": results}
		} else {
			return memory.Problem(400, "invalid_request", "Missing project write input.")
		}
		for _, change := range changes {
			if err := persistMemory(ctx, tx, change); err != nil {
				return err
			}
		}
		checkpoint := memory.Record{"id": newID(), "project_id": project["id"], "revision": project["revision"], "client_id": actor.Client.ID, "origin": originFor(kind), "summary": summary, "source_reference": nullableString(source), "created_at": now}
		if _, err := tx.Exec(ctx, `INSERT INTO checkpoints(id,project_id,revision,client_id,origin,summary,source_reference,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, checkpoint["id"], project["id"], project["revision"], actor.Client.ID, checkpoint["origin"], summary, nullableString(source), now); err != nil {
			return err
		}
		for i, change := range changes {
			operation := "update"
			if change.Before == nil {
				operation = "create"
			}
			if change.After == nil {
				operation = "delete"
			}
			var before, after json.RawMessage
			if change.Before != nil {
				body, err := json.Marshal(change.Before)
				if err != nil {
					return err
				}
				before = body
			}
			if change.After != nil {
				body, err := json.Marshal(change.After)
				if err != nil {
					return err
				}
				after = body
			}
			metadata, err := json.Marshal(change.Metadata)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO change_items(id,checkpoint_id,ordinal,entity_type,entity_id,operation,before_data,after_data,metadata) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, newID(), checkpoint["id"], i+1, change.Entity, change.ID, operation, before, after, json.RawMessage(metadata)); err != nil {
				return err
			}
		}
		payload["project"] = m.projectDTO(project)
		payload["checkpoint"] = checkpoint
		body, err := json.Marshal(memory.Envelope{Data: payload})
		if err != nil {
			return err
		}
		result = memory.WriteResult{Status: 201, Body: body}
		_, err = tx.Exec(ctx, `INSERT INTO write_requests(id,client_id,idempotency_key,request_hash,response_status,response_body) VALUES ($1,$2,$3,$4,$5,$6)`, newID(), actor.Client.ID, request.Key, request.Hash, result.Status, json.RawMessage(body))
		return err
	})
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) && pgError.Code == "23505" {
		switch pgError.ConstraintName {
		case "workspace_bindings_workspace_key_key":
			err = memory.Problem(409, "workspace_already_bound", "This workspace is already bound to a project.")
		case "documents_one_overview_idx":
			err = memory.Invalid("kind", "A project can have only one overview document.")
		}
	}
	if err != nil {
		return memory.WriteResult{}, memoryError(err)
	}
	return result, nil
}
func persistMemory(ctx context.Context, tx pgx.Tx, change *memoryChange) error {
	schema := memoryEntities[change.Entity]
	table := pgx.Identifier{schema.table}.Sanitize()
	if change.After == nil {
		_, err := tx.Exec(ctx, "DELETE FROM "+table+" WHERE id=$1 AND project_id=$2", change.ID, change.Before["project_id"])
		return err
	}
	columns := []string{}
	args := []any{}
	placeholders := []string{}
	assignments := []string{}
	for _, column := range schema.columns {
		value := change.After[column]
		if column == "evidence" {
			body, err := json.Marshal(value)
			if err != nil {
				return err
			}
			value = json.RawMessage(body)
		}
		if column == "revision" || column == "last_changed_revision" {
			value = memory.Int(value)
		}
		columns = append(columns, pgx.Identifier{column}.Sanitize())
		args = append(args, value)
		placeholder := fmt.Sprintf("$%d", len(args))
		placeholders = append(placeholders, placeholder)
		if column != "id" {
			assignments = append(assignments, pgx.Identifier{column}.Sanitize()+"="+placeholder)
		}
	}
	if change.Before == nil {
		_, err := tx.Exec(ctx, "INSERT INTO "+table+" ("+strings.Join(columns, ",")+") VALUES ("+strings.Join(placeholders, ",")+")", args...)
		return err
	}
	// Columns come only from the static entity schema, never request field names.
	_, err := tx.Exec(ctx, "UPDATE "+table+" SET "+strings.Join(assignments, ",")+" WHERE id=$1", args...)
	return err
}
