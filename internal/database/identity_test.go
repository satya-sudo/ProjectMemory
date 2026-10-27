package database

import (
	"errors"
	"projectmap/internal/access"
	"testing"
)

// Explicitly opt-in through the same isolated-schema helper as the foundation suite.
func TestClientIdentityResolution(t *testing.T) {
	ctx, store := testStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	ids, err := store.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := store.ResolveClient(ctx, ids.AgentClientID)
	if err != nil || identity.Client.ID != ids.AgentClientID || identity.Profile.ID != ids.ProfileID || identity.Client.Kind != "agent" {
		t.Fatal("client identity or ownership resolved incorrectly")
	}
	if _, err := store.ResolveClient(ctx, newID()); !errors.Is(err, access.ErrUnauthorized) {
		t.Fatal("unknown client accepted")
	}
	if _, err := store.Pool.Exec(ctx, "UPDATE clients SET revoked_at=now() WHERE id=$1", ids.AgentClientID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveClient(ctx, ids.AgentClientID); !errors.Is(err, access.ErrUnauthorized) {
		t.Fatal("revoked client accepted")
	}
	if _, err := store.ResolveClient(ctx, ids.DashboardClientID); err != nil {
		t.Fatal("unaffected client was disabled")
	}
}
