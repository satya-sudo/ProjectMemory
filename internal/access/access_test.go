package access

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"
)

const dashboardID = "11111111-1111-4111-8111-111111111111"
const agentID = "22222222-2222-4222-8222-222222222222"

type resolverFunc func(context.Context, string) (Identity, error)

func (f resolverFunc) ResolveClient(ctx context.Context, id string) (Identity, error) {
	return f(ctx, id)
}
func testManager() (*Manager, string, string) {
	dashboard, agent := secret(), secret()
	resolver := resolverFunc(func(_ context.Context, id string) (Identity, error) {
		kind := "dashboard"
		if id == agentID {
			kind = "agent"
		}
		return Identity{Profile: Profile{ID: "profile", DisplayName: "Local"}, Client: Client{ID: id, Kind: kind}}, nil
	})
	return New(resolver, map[[32]byte]string{sha256.Sum256([]byte(dashboard)): dashboardID, sha256.Sum256([]byte(agent)): agentID}), dashboard, agent
}
func TestBearerCredentialsAndIdentityFailures(t *testing.T) {
	manager, dashboard, _ := testManager()
	identity, err := manager.Bearer(context.Background(), dashboard)
	if err != nil || identity.Client.ID != dashboardID {
		t.Fatal("valid bearer failed")
	}
	if _, err := manager.Bearer(context.Background(), "unknown"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("unknown token accepted")
	}
	manager.resolver = resolverFunc(func(context.Context, string) (Identity, error) { return Identity{}, ErrUnauthorized })
	if _, err := manager.Bearer(context.Background(), dashboard); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked identity accepted")
	}
	manager.resolver = resolverFunc(func(context.Context, string) (Identity, error) { return Identity{}, ErrUnavailable })
	if _, err := manager.Bearer(context.Background(), dashboard); !errors.Is(err, ErrUnavailable) {
		t.Fatal("store outage was lost")
	}
}
func TestSessionLifecycleAndRevocation(t *testing.T) {
	manager, dashboard, agent := testManager()
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	if _, _, err := manager.Login(context.Background(), agent); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("agent exchanged a browser credential")
	}
	token, session, err := manager.Login(context.Background(), dashboard)
	if err != nil || token == "" || session.CSRF == "" || token == session.CSRF {
		t.Fatal("session secrets invalid")
	}
	if _, _, err := manager.AuthenticateSession(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	original := manager.resolver
	manager.resolver = resolverFunc(func(context.Context, string) (Identity, error) { return Identity{}, ErrUnauthorized })
	if _, _, err := manager.AuthenticateSession(context.Background(), token); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("session bypassed client revocation")
	}
	manager.resolver = original
	now = now.Add(SessionLifetime)
	if _, _, err := manager.AuthenticateSession(context.Background(), token); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("expired session accepted")
	}
	token, _, err = manager.Login(context.Background(), dashboard)
	if err != nil {
		t.Fatal(err)
	}
	manager.Logout(token)
	if _, _, err := manager.AuthenticateSession(context.Background(), token); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("logged-out session accepted")
	}
}
func TestLoginRateLimitAndReset(t *testing.T) {
	manager, _, _ := testManager()
	now := time.Now()
	manager.now = func() time.Time { return now }
	for range loginLimit {
		if _, _, err := manager.Login(context.Background(), "wrong"); !errors.Is(err, ErrUnauthorized) {
			t.Fatal("unexpected login error")
		}
	}
	if _, _, err := manager.Login(context.Background(), "wrong"); !errors.Is(err, ErrRateLimited) {
		t.Fatal("login limit not enforced")
	}
	now = now.Add(time.Minute)
	if _, _, err := manager.Login(context.Background(), "wrong"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("login limit did not expire")
	}
}
func TestConcurrentSessionAccess(t *testing.T) {
	manager, dashboard, _ := testManager()
	token, _, err := manager.Login(context.Background(), dashboard)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			if _, _, err := manager.AuthenticateSession(context.Background(), token); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	manager.Logout(token)
}
func TestSessionLimitAndExpiryCleanup(t *testing.T) {
	manager, dashboard, _ := testManager()
	now := time.Now()
	manager.now = func() time.Time { return now }
	for i := range maxSessions {
		hash := sha256.Sum256([]byte{byte(i), byte(i >> 8)})
		manager.sessions[hash] = Session{Expires: now.Add(time.Hour)}
	}
	if _, _, err := manager.Login(context.Background(), dashboard); !errors.Is(err, ErrRateLimited) {
		t.Fatal("session bound not enforced")
	}
	now = now.Add(2 * time.Hour)
	if _, _, err := manager.Login(context.Background(), dashboard); err != nil {
		t.Fatal(err)
	}
	if len(manager.sessions) != 1 {
		t.Fatal("expired sessions were not cleared")
	}
}

func TestRestartInvalidatesBrowserSessions(t *testing.T) {
	manager, dashboard, _ := testManager()
	token, _, err := manager.Login(context.Background(), dashboard)
	if err != nil {
		t.Fatal(err)
	}
	restarted := New(manager.resolver, manager.credentials)
	if _, _, err := restarted.AuthenticateSession(context.Background(), token); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("session survived a server restart")
	}
	if _, err := restarted.Bearer(context.Background(), dashboard); err != nil {
		t.Fatal("configured credential was lost on restart")
	}
}
