package database

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

type Store struct{ Pool *pgxpool.Pool }

func Open(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("invalid database connection configuration")
	}
	cfg.MaxConns = 5
	cfg.MinConns = 0
	cfg.ConnConfig.ConnectTimeout = 3 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("could not initialize database pool")
	}
	return &Store{Pool: pool}, nil
}
func (s *Store) Close() { s.Pool.Close() }

// InTx always rolls back on failure, including callback panics through pgx's helper.
func (s *Store) InTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, s.Pool, pgx.TxOptions{}, fn)
}
func (s *Store) ReadSnapshot(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, s.Pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, fn)
}
func (s *Store) Ready(ctx context.Context) error {
	if err := s.Pool.Ping(ctx); err != nil {
		return errors.New("database unavailable")
	}
	migrations, err := bundledMigrations()
	if err != nil {
		return err
	}
	rows, err := s.Pool.Query(ctx, "SELECT version, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return errors.New("database schema is not initialized")
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var version int
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return errors.New("database schema is incompatible")
		}
		if count >= len(migrations) || version != migrations[count].version || checksum != migrations[count].checksum {
			return errors.New("database schema is incompatible")
		}
		count++
	}
	if rows.Err() != nil || count != len(migrations) {
		return errors.New("database schema is incompatible")
	}
	return nil
}

func operationError(operation string, err error) error {
	// Preserve server SQLSTATE for diagnosis without logging connection strings or SQL values.
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		return fmt.Errorf("%s failed (SQLSTATE %s)", operation, state.SQLState())
	}
	return fmt.Errorf("%s failed", operation)
}
