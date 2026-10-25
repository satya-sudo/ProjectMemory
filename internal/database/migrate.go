package database

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type migration struct {
	version       int
	sql, checksum string
}

func bundledMigrations() ([]migration, error) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	out := make([]migration, 0, len(entries))
	for _, e := range entries {
		v, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil {
			return nil, errors.New("invalid bundled migration name")
		}
		body, err := migrationFiles.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)
		out = append(out, migration{v, string(body), hex.EncodeToString(sum[:])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	if len(out) == 0 {
		return nil, errors.New("no bundled migrations")
	}
	for i, m := range out {
		if m.version != i+1 {
			return nil, errors.New("migrations must be sequential from version 1")
		}
	}
	return out, nil
}

// Migrate uses one transaction and lock, so partial migrations are never recorded.
func (s *Store) Migrate(ctx context.Context) error {
	migrations, err := bundledMigrations()
	if err != nil {
		return err
	}
	err = s.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(70749301)"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
            version integer PRIMARY KEY CHECK (version > 0), checksum text NOT NULL,
            applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT version, checksum FROM schema_migrations ORDER BY version")
		if err != nil {
			return err
		}
		applied := map[int]string{}
		for rows.Next() {
			var v int
			var checksum string
			if err := rows.Scan(&v, &checksum); err != nil {
				rows.Close()
				return err
			}
			applied[v] = checksum
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(applied) > len(migrations) {
			return errors.New("database has newer migrations than this application")
		}
		for i := 1; i <= len(applied); i++ {
			if _, ok := applied[i]; !ok {
				return errors.New("database migration history contains gaps")
			}
		}
		for _, m := range migrations {
			if checksum, ok := applied[m.version]; ok {
				if checksum != m.checksum {
					return fmt.Errorf("migration %d checksum mismatch", m.version)
				}
				continue
			}
			if _, err := tx.Exec(ctx, m.sql); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations(version, checksum) VALUES ($1,$2)", m.version, m.checksum); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return operationError("migration", err)
	}
	return nil
}
