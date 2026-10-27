package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"projectmap/internal/access"
	"projectmap/internal/memory"
)

type MemoryStore struct {
	store   *Store
	origin  string
	cursors *memory.Cursors
}

func NewMemoryStore(store *Store, origin string) *MemoryStore {
	return &MemoryStore{store: store, origin: strings.TrimRight(origin, "/"), cursors: memory.NewCursors()}
}

type entitySchema struct {
	table   string
	columns []string
}

var memoryEntities = map[string]entitySchema{
	"project":           {"projects", []string{"id", "profile_id", "title", "summary", "next_step", "revision", "archived_at", "created_at", "updated_at"}},
	"document":          {"documents", []string{"id", "project_id", "created_at", "updated_at", "last_changed_revision", "kind", "title", "content", "state"}},
	"decision":          {"decisions", []string{"id", "project_id", "created_at", "updated_at", "last_changed_revision", "title", "statement", "rationale", "state", "source_reference", "acceptance_reference"}},
	"question":          {"open_questions", []string{"id", "project_id", "created_at", "updated_at", "last_changed_revision", "question", "state", "resolution"}},
	"progress":          {"progress_entries", []string{"id", "project_id", "created_at", "updated_at", "last_changed_revision", "summary", "verification", "evidence"}},
	"workspace_binding": {"workspace_bindings", []string{"id", "project_id", "created_at", "updated_at", "last_changed_revision", "workspace_key", "repository_url"}},
}

func decodeMemory(raw []byte) (memory.Record, error) {
	var record memory.Record
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&record); err != nil {
		return nil, err
	}
	for _, key := range []string{"created_at", "updated_at", "archived_at"} {
		if text, ok := record[key].(string); ok {
			parsed, err := time.Parse(time.RFC3339Nano, text)
			if err != nil {
				return nil, err
			}
			record[key] = parsed.UTC()
		}
	}
	return record, nil
}
func memoryRow(row pgx.Row) (memory.Record, error) {
	var raw []byte
	if err := row.Scan(&raw); err != nil {
		return nil, err
	}
	return decodeMemory(raw)
}
func readOwnedProject(ctx context.Context, tx pgx.Tx, profile, id string, lock bool) (memory.Record, error) {
	query := "SELECT to_jsonb(p) FROM projects p WHERE p.id=$1 AND p.profile_id=$2"
	if lock {
		query += " FOR UPDATE"
	}
	project, err := memoryRow(tx.QueryRow(ctx, query, id, profile))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, memory.Problem(404, "not_found", "Project not found.")
	}
	return project, err
}
func readMemoryEntity(ctx context.Context, tx pgx.Tx, entity, project, id string) (memory.Record, error) {
	schema, ok := memoryEntities[entity]
	if !ok || entity == "project" {
		return nil, errors.New("invalid entity lookup")
	}
	query := "SELECT to_jsonb(t) FROM " + pgx.Identifier{schema.table}.Sanitize() + " t WHERE t.project_id=$1 AND t.id=$2"
	row, err := memoryRow(tx.QueryRow(ctx, query, project, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, memory.Problem(404, "not_found", "Resource not found in this project.")
	}
	return row, err
}
func (m *MemoryStore) projectDTO(project memory.Record) memory.Record {
	out := memory.Clone(project)
	delete(out, "profile_id")
	out["dashboard_url"] = m.origin + "/projects/" + project["id"].(string)
	return out
}
func memoryError(err error) error {
	if err == nil {
		return nil
	}
	var problem *memory.Error
	if errors.As(err, &problem) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return memory.Problem(503, "service_unavailable", "The storage operation could not complete.")
	}
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		if strings.HasPrefix(state.SQLState(), "08") || state.SQLState() == "57P01" || state.SQLState() == "53300" || state.SQLState() == "40001" || state.SQLState() == "40P01" {
			return memory.Problem(503, "service_unavailable", "The storage operation could not complete.")
		}
		return memory.Problem(500, "internal_error", "The storage operation failed.")
	}
	return memory.Problem(503, "service_unavailable", "The local storage service is unavailable.")
}
func (m *MemoryStore) Read(ctx context.Context, actor access.Identity, query memory.Query) (memory.Envelope, error) {
	var result memory.Envelope
	err := m.store.ReadSnapshot(ctx, func(tx pgx.Tx) error {
		if query.Resource == "projects" {
			page, _, err := m.list(ctx, tx, actor.Profile.ID, query, 0)
			result = page
			return err
		}
		if query.Resource == "resolve" {
			binding, err := memoryRow(tx.QueryRow(ctx, `SELECT to_jsonb(w) FROM workspace_bindings w JOIN projects p ON p.id=w.project_id WHERE w.workspace_key=$1 AND p.profile_id=$2`, query.WorkspaceKey, actor.Profile.ID))
			if errors.Is(err, pgx.ErrNoRows) {
				return memory.Problem(404, "workspace_unbound", "This workspace has no tracked project.")
			}
			if err != nil {
				return err
			}
			project, err := readOwnedProject(ctx, tx, actor.Profile.ID, binding["project_id"].(string), false)
			if err != nil {
				return err
			}
			result = memory.Envelope{Data: memory.Record{"binding": binding, "project": m.projectDTO(project)}}
			return nil
		}
		project, err := readOwnedProject(ctx, tx, actor.Profile.ID, query.ProjectID, false)
		if err != nil {
			return err
		}
		revision := memory.Int(project["revision"])
		result.Meta = memory.Record{"project_revision": revision}
		switch query.Resource {
		case "project":
			dto := m.projectDTO(project)
			counts := memory.Record{}
			for resource, entity := range map[string]string{"documents": "document", "decisions": "decision", "questions": "question", "progress": "progress", "workspace_bindings": "workspace_binding"} {
				var count int64
				if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{memoryEntities[entity].table}.Sanitize()+" WHERE project_id=$1", query.ProjectID).Scan(&count); err != nil {
					return err
				}
				counts[resource] = count
			}
			dto["counts"] = counts
			result.Data = dto
		case "document":
			row, err := readMemoryEntity(ctx, tx, "document", query.ProjectID, query.TargetID)
			if err != nil {
				return err
			}
			result.Data = row
		case "checkpoint":
			row, err := m.checkpoint(ctx, tx, query.ProjectID, query.TargetID)
			if err != nil {
				return err
			}
			result.Data = row
		case "context":
			data, err := m.contextPacket(ctx, tx, actor.Profile.ID, project)
			if err != nil {
				return err
			}
			result.Data = data
		default:
			page, _, err := m.list(ctx, tx, actor.Profile.ID, query, revision)
			if err != nil {
				return err
			}
			result = page
		}
		return nil
	})
	return result, memoryError(err)
}
func (m *MemoryStore) list(ctx context.Context, tx pgx.Tx, profile string, query memory.Query, revision int64) (memory.Envelope, int64, error) {
	var cursor *memory.Cursor
	if query.Cursor != "" {
		decoded, err := m.cursors.Decode(query.Cursor, memory.Scope(profile, query))
		if err != nil {
			return memory.Envelope{}, 0, err
		}
		cursor = &decoded
		if query.Resource != "projects" {
			if err := memory.CheckCursorRevision(decoded, revision); err != nil {
				return memory.Envelope{}, 0, err
			}
		}
	}
	table, selection, sortColumn := "", "to_jsonb(t)", "created_at"
	switch query.Resource {
	case "projects":
		table = "projects"
		sortColumn = "updated_at"
	case "documents":
		table = "documents"
		sortColumn = "updated_at"
		selection = "to_jsonb(t)-'content'"
	case "decisions":
		table = "decisions"
	case "questions":
		table = "open_questions"
	case "progress":
		table = "progress_entries"
	case "bindings":
		table = "workspace_bindings"
	case "history":
		table = "checkpoints"
		sortColumn = "revision"
		selection = `to_jsonb(t)||jsonb_build_object('client',jsonb_build_object('id',c.id,'name',c.name,'kind',c.kind))`
	default:
		return memory.Envelope{}, 0, memory.Problem(400, "invalid_request", "Invalid resource listing.")
	}
	args := []any{}
	where := []string{}
	add := func(expression string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(expression, len(args)))
	}
	if query.Resource == "projects" {
		add("t.profile_id=$%d", profile)
		if query.Archived == "false" {
			where = append(where, "t.archived_at IS NULL")
		} else if query.Archived == "true" {
			where = append(where, "t.archived_at IS NOT NULL")
		}
		if query.Search != "" {
			escaped := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(query.Search)
			args = append(args, "%"+escaped+"%")
			where = append(where, fmt.Sprintf("(t.title ILIKE $%d OR t.summary ILIKE $%d)", len(args), len(args)))
		}
	} else {
		add("t.project_id=$%d", query.ProjectID)
	}
	if query.Kind != "" {
		add("t.kind=$%d", query.Kind)
	}
	if query.State != "" {
		add("t.state=$%d", query.State)
	}
	from := pgx.Identifier{table}.Sanitize() + " t"
	if query.Resource == "history" {
		from += " JOIN clients c ON c.id=t.client_id"
	}
	var total int64
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+from+" WHERE "+strings.Join(where, " AND "), args...).Scan(&total); err != nil {
		return memory.Envelope{}, 0, err
	}
	if cursor != nil {
		if sortColumn == "revision" {
			if cursor.LastRevision < 1 {
				return memory.Envelope{}, 0, memory.Problem(400, "invalid_cursor", "Invalid history cursor.")
			}
			add("t.revision<$%d", cursor.LastRevision)
		} else {
			if cursor.LastRevision != 0 {
				return memory.Envelope{}, 0, memory.Problem(400, "invalid_cursor", "Invalid resource cursor.")
			}
			timestamp, err := time.Parse(time.RFC3339Nano, cursor.Time)
			if err != nil {
				return memory.Envelope{}, 0, err
			}
			args = append(args, timestamp, cursor.ID)
			where = append(where, fmt.Sprintf("(t.%s,t.id)<($%d::timestamptz,$%d::uuid)", sortColumn, len(args)-1, len(args)))
		}
	}
	args = append(args, query.Limit+1)
	sql := "SELECT " + selection + " FROM " + from + " WHERE " + strings.Join(where, " AND ") + " ORDER BY t." + sortColumn + " DESC"
	if sortColumn != "revision" {
		sql += ",t.id DESC"
	}
	sql += fmt.Sprintf(" LIMIT $%d", len(args))
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return memory.Envelope{}, 0, err
	}
	defer rows.Close()
	items := []memory.Record{}
	for rows.Next() {
		row, err := memoryRow(rows)
		if err != nil {
			return memory.Envelope{}, 0, err
		}
		if query.Resource == "projects" {
			row = m.projectDTO(row)
		}
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return memory.Envelope{}, 0, err
	}
	page := &memory.Page{}
	if len(items) > query.Limit {
		items = items[:query.Limit]
		last := items[len(items)-1]
		next := memory.Cursor{Scope: memory.Scope(profile, query), Revision: revision}
		if sortColumn == "revision" {
			next.LastRevision = memory.Int(last["revision"])
		} else {
			next.ID = last["id"].(string)
			next.Time = last[sortColumn].(time.Time).UTC().Format(time.RFC3339Nano)
		}
		token := m.cursors.Encode(next)
		page.NextCursor = &token
		page.HasMore = true
	}
	result := memory.Envelope{Data: items, Page: page}
	if query.Resource != "projects" {
		result.Meta = memory.Record{"project_revision": revision}
	}
	return result, total, nil
}
func (m *MemoryStore) checkpoint(ctx context.Context, tx pgx.Tx, project, id string) (memory.Record, error) {
	checkpoint, err := memoryRow(tx.QueryRow(ctx, `SELECT to_jsonb(t)||jsonb_build_object('client',jsonb_build_object('id',c.id,'name',c.name,'kind',c.kind)) FROM checkpoints t JOIN clients c ON c.id=t.client_id WHERE t.project_id=$1 AND t.id=$2`, project, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, memory.Problem(404, "not_found", "Checkpoint not found.")
	}
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, "SELECT to_jsonb(t) FROM change_items t WHERE t.checkpoint_id=$1 ORDER BY ordinal", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []memory.Record{}
	for rows.Next() {
		item, err := memoryRow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	checkpoint["change_items"] = items
	return checkpoint, nil
}
func (m *MemoryStore) contextPacket(ctx context.Context, tx pgx.Tx, profile string, project memory.Record) (memory.Record, error) {
	data := memory.Record{"project": m.projectDTO(project), "overview": nil}
	coverage := memory.Record{}
	id := project["id"].(string)
	revision := memory.Int(project["revision"])
	overview, err := memoryRow(tx.QueryRow(ctx, "SELECT to_jsonb(t) FROM documents t WHERE t.project_id=$1 AND t.kind='overview'", id))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if overview != nil {
		content := overview["content"].(string)
		preview, truncated := memory.Preview(content, 8000)
		data["overview"] = memory.Record{"document_id": overview["id"], "state": overview["state"], "preview": preview, "truncated": truncated}
	}
	for _, component := range []struct {
		name, resource, state string
		limit                 int
	}{{"documents", "documents", "", 20}, {"accepted_decisions", "decisions", "accepted", 20}, {"proposed_decisions", "decisions", "proposed", 20}, {"open_questions", "questions", "open", 20}, {"recent_progress", "progress", "", 5}} {
		q := memory.Query{Resource: component.resource, ProjectID: id, State: component.state, Limit: component.limit}
		page, total, err := m.list(ctx, tx, profile, q, revision)
		if err != nil {
			return nil, err
		}
		items := page.Data.([]memory.Record)
		data[component.name] = items
		continuation := any(nil)
		if page.Page.NextCursor != nil {
			params := url.Values{"limit": {fmt.Sprint(q.Limit)}, "cursor": {*page.Page.NextCursor}}
			if q.State != "" {
				params.Set("state", q.State)
			}
			continuation = "/api/v1/projects/" + id + "/" + component.resource + "?" + params.Encode()
		}
		coverage[component.name] = memory.Record{"total": total, "returned": len(items), "limit": q.Limit, "next_cursor": page.Page.NextCursor, "next_url": continuation}
	}
	data["coverage"] = coverage
	return data, nil
}
