package database

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestConcurrentSetup(t *testing.T) {
	ctx, store := testStore(t)
	const workers = 8
	start := make(chan struct{})
	failures := make(chan error, workers)
	identities := make(chan Identity, workers)
	var group sync.WaitGroup
	for range workers {
		group.Go(func() {
			<-start
			if err := store.Migrate(ctx); err != nil {
				failures <- err
				return
			}
			identity, err := store.Bootstrap(ctx)
			if err != nil {
				failures <- err
				return
			}
			identities <- identity
		})
	}
	close(start)
	group.Wait()
	close(failures)
	close(identities)
	for err := range failures {
		t.Errorf("concurrent setup: %v", err)
	}
	var first Identity
	count := 0
	for identity := range identities {
		if count == 0 {
			first = identity
		} else if identity != first {
			t.Errorf("concurrent bootstrap returned different identities")
		}
		count++
	}
	if count != workers {
		t.Fatalf("only %d/%d setup calls succeeded", count, workers)
	}
	var profiles, clients, versions int
	if err := store.Pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM profiles), (SELECT count(*) FROM clients), (SELECT count(*) FROM schema_migrations)").Scan(&profiles, &clients, &versions); err != nil {
		t.Fatal(err)
	}
	if profiles != 1 || clients != 2 || versions != 1 {
		t.Fatalf("duplicated setup records: profiles=%d clients=%d versions=%d", profiles, clients, versions)
	}
}

func TestFailedMigrationLeavesNoPartialSchema(t *testing.T) {
	ctx, store := testStore(t)
	// A preexisting conflicting table forces failure after migration tracking is created.
	if _, err := store.Pool.Exec(ctx, "CREATE TABLE profiles (marker text)"); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err == nil {
		t.Fatal("conflicting schema unexpectedly migrated")
	}
	var trackingMissing, projectsMissing bool
	if err := store.Pool.QueryRow(ctx, "SELECT to_regclass('schema_migrations') IS NULL, to_regclass('projects') IS NULL").Scan(&trackingMissing, &projectsMissing); err != nil {
		t.Fatal(err)
	}
	if !trackingMissing || !projectsMissing {
		t.Fatal("failed migration left partial tables or migration history")
	}
	if _, err := store.Pool.Exec(ctx, "DROP TABLE profiles"); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("could not recover after resolving conflict: %v", err)
	}
	if err := store.Ready(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestUnsupportedMigrationHistory(t *testing.T) {
	for _, version := range []int{2, 3} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			ctx, store := testStore(t)
			if err := store.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			if version == 3 {
				if _, err := store.Pool.Exec(ctx, "DELETE FROM schema_migrations"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.Pool.Exec(ctx, "INSERT INTO schema_migrations(version,checksum) VALUES ($1,'unknown')", version); err != nil {
				t.Fatal(err)
			}
			if err := store.Migrate(ctx); err == nil {
				t.Fatal("unsupported history was accepted by migrations")
			}
			if err := store.Ready(ctx); err == nil {
				t.Fatal("unsupported history was ready")
			}
		})
	}
}

func TestRevokedClientIsNotReused(t *testing.T) {
	ctx, store := testStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := store.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool.Exec(ctx, "UPDATE clients SET revoked_at=now() WHERE id=$1", before.AgentClientID); err != nil {
		t.Fatal(err)
	}
	after, err := store.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.ProfileID != before.ProfileID || after.DashboardClientID != before.DashboardClientID || after.AgentClientID == before.AgentClientID {
		t.Fatal("bootstrap reused revoked client or replaced unaffected identities")
	}
	var count int
	if err := store.Pool.QueryRow(ctx, "SELECT count(*) FROM clients").Scan(&count); err != nil || count != 3 {
		t.Fatalf("revoked history not preserved: %d, %v", count, err)
	}
}

func TestInterruptedWritesRollback(t *testing.T) {
	ctx, store := testStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Run("panic", func(t *testing.T) {
		func() {
			defer func() {
				if recover() != "test panic" {
					t.Error("callback panic was lost")
				}
			}()
			_ = store.InTx(ctx, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, "INSERT INTO profiles(id,display_name) VALUES ($1,'panic write')", newID()); err != nil {
					t.Fatal(err)
				}
				panic("test panic")
			})
		}()
	})
	t.Run("cancellation", func(t *testing.T) {
		canceled, cancel := context.WithCancel(ctx)
		defer cancel()
		err := store.InTx(canceled, func(tx pgx.Tx) error {
			if _, err := tx.Exec(canceled, "INSERT INTO profiles(id,display_name) VALUES ($1,'canceled write')", newID()); err != nil {
				return err
			}
			cancel()
			return canceled.Err()
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	})
	var count int
	if err := store.Pool.QueryRow(ctx, "SELECT count(*) FROM profiles").Scan(&count); err != nil || count != 0 {
		t.Fatalf("interrupted writes persisted: %d, %v", count, err)
	}
	if err := store.Ready(ctx); err != nil {
		t.Fatalf("pool did not recover: %v", err)
	}
}

func TestSchemaIntegrityEdges(t *testing.T) {
	ctx, store := testStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	identity, err := store.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	project, checkpoint := newID(), newID()
	if _, err := store.Pool.Exec(ctx, "INSERT INTO projects(id,profile_id,title) VALUES ($1,$2,'Test')", project, identity.ProfileID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool.Exec(ctx, "INSERT INTO checkpoints(id,project_id,revision,client_id,origin,summary) VALUES ($1,$2,1,$3,'mcp','Initial')", checkpoint, project, identity.AgentClientID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool.Exec(ctx, "INSERT INTO workspace_bindings(id,project_id,last_changed_revision,workspace_key) VALUES ($1,$2,1,'/test/workspace')", newID(), project); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, statement, state string
		args                   []any
	}{
		{"missing parent", "INSERT INTO documents(id,project_id,last_changed_revision,kind,title) VALUES ($1,$2,1,'note','Note')", "23503", []any{newID(), newID()}},
		{"negative revision", "UPDATE projects SET revision=-1 WHERE id=$1", "23514", []any{project}},
		{"invalid document state", "INSERT INTO documents(id,project_id,last_changed_revision,kind,title,state) VALUES ($1,$2,1,'note','Note','building')", "23514", []any{newID(), project}},
		{"duplicate checkpoint", "INSERT INTO checkpoints(id,project_id,revision,client_id,origin,summary) VALUES ($1,$2,1,$3,'mcp','Duplicate')", "23505", []any{newID(), project, identity.AgentClientID}},
		{"duplicate workspace", "INSERT INTO workspace_bindings(id,project_id,last_changed_revision,workspace_key) VALUES ($1,$2,1,'/test/workspace')", "23505", []any{newID(), project}},
		{"invalid history snapshots", "INSERT INTO change_items(id,checkpoint_id,ordinal,entity_type,entity_id,operation) VALUES ($1,$2,1,'project',$3,'create')", "23514", []any{newID(), checkpoint, project}},
		{"nonobject history metadata", "INSERT INTO change_items(id,checkpoint_id,ordinal,entity_type,entity_id,operation,after_data,metadata) VALUES ($1,$2,1,'project',$3,'create','{}','[]')", "23514", []any{newID(), checkpoint, project}},
		{"nonarray evidence", "INSERT INTO progress_entries(id,project_id,last_changed_revision,summary,evidence) VALUES ($1,$2,1,'Done','{}')", "23514", []any{newID(), project}},
		{"restrict project deletion", "DELETE FROM projects WHERE id=$1", "23503", []any{project}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.Pool.Exec(ctx, tc.statement, tc.args...)
			var pgError *pgconn.PgError
			if !errors.As(err, &pgError) || pgError.Code != tc.state {
				t.Fatalf("expected SQLSTATE %s, got %v", tc.state, err)
			}
		})
	}
	hash := strings.Repeat("a", 64)
	for i := range 2 {
		_, err := store.Pool.Exec(ctx, "INSERT INTO write_requests(id,client_id,idempotency_key,request_hash,response_status,response_body) VALUES ($1,$2,'retry-key',$3,201,'{}')", newID(), identity.AgentClientID, hash)
		if i == 0 && err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			var pgError *pgconn.PgError
			if !errors.As(err, &pgError) || pgError.Code != "23505" {
				t.Fatalf("duplicate retry key accepted: %v", err)
			}
		}
	}
}
