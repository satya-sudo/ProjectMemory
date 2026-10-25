package database

import (
	"context"
	"crypto/rand"
	"fmt"
	"github.com/jackc/pgx/v5"
)

type Identity struct {
	ProfileID         string `json:"profile_id"`
	DashboardClientID string `json:"dashboard_client_id"`
	AgentClientID     string `json:"agent_client_id"`
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("secure random source unavailable")
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Bootstrap provisions domain identities only. Credentials are a separate API phase.
func (s *Store) Bootstrap(ctx context.Context) (Identity, error) {
	var out Identity
	err := s.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(70749302)"); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, "SELECT id::text FROM profiles ORDER BY created_at, id LIMIT 1").Scan(&out.ProfileID)
		if err == pgx.ErrNoRows {
			out.ProfileID = newID()
			if _, err := tx.Exec(ctx, "INSERT INTO profiles(id, display_name) VALUES ($1,'Local profile')", out.ProfileID); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		clients := []struct {
			kind, name string
			id         *string
		}{{"dashboard", "Local dashboard", &out.DashboardClientID}, {"agent", "Local MCP adapter", &out.AgentClientID}}
		for _, c := range clients {
			err := tx.QueryRow(ctx, "SELECT id::text FROM clients WHERE profile_id=$1 AND kind=$2 AND name=$3 AND revoked_at IS NULL ORDER BY created_at, id LIMIT 1", out.ProfileID, c.kind, c.name).Scan(c.id)
			if err == pgx.ErrNoRows {
				*c.id = newID()
				if _, err := tx.Exec(ctx, "INSERT INTO clients(id, profile_id, kind, name) VALUES ($1,$2,$3,$4)", *c.id, out.ProfileID, c.kind, c.name); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Identity{}, operationError("bootstrap", err)
	}
	return out, nil
}
