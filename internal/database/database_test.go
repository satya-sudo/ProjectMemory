package database

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestBundledMigrationSequence(t *testing.T) {
	migrations, err := bundledMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 1 || migrations[0].version != 1 {
		t.Fatalf("unexpected migrations: %d", len(migrations))
	}
}

// Uses a generated isolated schema; never drops or migrates the caller's public schema.
func TestPostgresFoundation(t *testing.T) {
	ctx, store := testStore(t)
	if err := store.Ready(ctx); err == nil {
		t.Fatal("unmigrated database was ready")
	}
	for range 2 {
		if err := store.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	a, err := store.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("bootstrap is not idempotent")
	}
	sentinel := errors.New("rollback")
	err = store.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO profiles(id,display_name) VALUES ($1,'rolled back')", newID()); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("callback error lost: %v", err)
	}
	var count int
	if err := store.Pool.QueryRow(ctx, "SELECT count(*) FROM profiles").Scan(&count); err != nil || count != 1 {
		t.Fatalf("rollback failed: %d, %v", count, err)
	}
	// A context assembled from several queries must not mix committed versions.
	concurrentProfile := newID()
	err = store.ReadSnapshot(ctx, func(tx pgx.Tx) error {
		var before, after int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM profiles").Scan(&before); err != nil {
			return err
		}
		if _, err := store.Pool.Exec(ctx, "INSERT INTO profiles(id,display_name) VALUES ($1,'concurrent write')", concurrentProfile); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM profiles").Scan(&after); err != nil {
			return err
		}
		if before != after {
			return errors.New("read snapshot changed during transaction")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Pool.QueryRow(ctx, "SELECT count(*) FROM profiles").Scan(&count); err != nil || count != 2 {
		t.Fatalf("concurrent write not visible after snapshot: %d, %v", count, err)
	}
	err = store.ReadSnapshot(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO profiles(id,display_name) VALUES ($1,'must not write')", newID())
		return err
	})
	var readOnlyError *pgconn.PgError
	if !errors.As(err, &readOnlyError) || readOnlyError.Code != "25006" {
		t.Fatalf("snapshot allowed writes or failed unexpectedly: %v", err)
	}
	project := newID()
	if _, err := store.Pool.Exec(ctx, "INSERT INTO projects(id,profile_id,title) VALUES ($1,$2,'Test')", project, a.ProfileID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, sql string
		args      []any
	}{
		{"blank title", "INSERT INTO projects(id,profile_id,title) VALUES ($1,$2,'  ')", []any{newID(), a.ProfileID}},
		{"unaccepted decision", "INSERT INTO decisions(id,project_id,last_changed_revision,title,statement,state) VALUES ($1,$2,1,'Decision','Choose local','accepted')", []any{newID(), project}},
		{"unresolved resolution", "INSERT INTO open_questions(id,project_id,last_changed_revision,question,state) VALUES ($1,$2,1,'Question','resolved')", []any{newID(), project}},
		{"verified without evidence", "INSERT INTO progress_entries(id,project_id,last_changed_revision,summary,verification) VALUES ($1,$2,1,'Done','verified')", []any{newID(), project}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.Pool.Exec(ctx, tc.sql, tc.args...)
			var pgError *pgconn.PgError
			if !errors.As(err, &pgError) || pgError.Code != "23514" {
				t.Fatalf("expected check-constraint rejection, got %v", err)
			}
		})
	}
	for i := range 2 {
		_, err := store.Pool.Exec(ctx, "INSERT INTO documents(id,project_id,last_changed_revision,kind,title) VALUES ($1,$2,1,'overview','Overview')", newID(), project)
		if (i == 0 && err != nil) || (i == 1 && err == nil) {
			t.Fatalf("overview uniqueness failed: %v", err)
		}
		if i == 1 {
			var pgError *pgconn.PgError
			if !errors.As(err, &pgError) || pgError.Code != "23505" {
				t.Fatalf("expected unique-constraint rejection, got %v", err)
			}
		}
	}
	reopened, err := Open(ctx, store.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Pool.QueryRow(ctx, "SELECT count(*) FROM projects WHERE id=$1", project).Scan(&count); err != nil || count != 1 {
		t.Fatalf("data unavailable on new connection: %v", err)
	}
	if _, err := store.Pool.Exec(ctx, "UPDATE schema_migrations SET checksum='tampered' WHERE version=1"); err != nil {
		t.Fatal(err)
	}
	if err := store.Ready(ctx); err == nil {
		t.Fatal("tampered schema was ready")
	}
	if err := store.Migrate(ctx); err == nil {
		t.Fatal("migration accepted changed checksum")
	}
}
