package database

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"projectmap/internal/access"
)

// ResolveClient checks revocation on every request; credentials cannot assert
// their own profile, client name, or permissions.
func (s *Store) ResolveClient(ctx context.Context, id string) (access.Identity, error) {
	var identity access.Identity
	err := s.Pool.QueryRow(ctx, `SELECT p.id::text,p.display_name,c.id::text,c.name,c.kind
        FROM clients c JOIN profiles p ON p.id=c.profile_id
        WHERE c.id=$1 AND c.revoked_at IS NULL`, id).Scan(
		&identity.Profile.ID, &identity.Profile.DisplayName, &identity.Client.ID, &identity.Client.Name, &identity.Client.Kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return access.Identity{}, access.ErrUnauthorized
	}
	if err != nil {
		return access.Identity{}, access.ErrUnavailable
	}
	return identity, nil
}
