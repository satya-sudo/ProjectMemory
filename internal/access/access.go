// Package access manages local client credentials and ephemeral browser sessions.
package access

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"time"
)

var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrUnavailable  = errors.New("identity store unavailable")
	ErrRateLimited  = errors.New("authentication rate limited")
)

type Profile struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}
type Client struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}
type Identity struct {
	Profile Profile `json:"profile"`
	Client  Client  `json:"client"`
}
type Resolver interface {
	ResolveClient(context.Context, string) (Identity, error)
}
type Session struct {
	ClientID, CSRF string
	Expires        time.Time
}

const SessionLifetime = 12 * time.Hour
const maxSessions = 1000
const loginLimit = 20

// Manager is safe for concurrent requests. Sessions expire absolutely (not on
// inactivity) and are intentionally invalidated by a server restart.
type Manager struct {
	resolver    Resolver
	credentials map[[32]byte]string
	mu          sync.Mutex
	sessions    map[[32]byte]Session
	attempts    []time.Time
	now         func() time.Time
}

func New(resolver Resolver, credentials map[[32]byte]string) *Manager {
	copied := make(map[[32]byte]string, len(credentials))
	for hash, id := range credentials {
		copied[hash] = id
	}
	return &Manager{resolver: resolver, credentials: copied, sessions: map[[32]byte]Session{}, now: time.Now}
}
func (m *Manager) Bearer(ctx context.Context, token string) (Identity, error) {
	if len(token) > 128 {
		return Identity{}, ErrUnauthorized
	}
	clientID, ok := m.credentials[sha256.Sum256([]byte(token))]
	if !ok || m.resolver == nil {
		return Identity{}, ErrUnauthorized
	}
	return m.resolver.ResolveClient(ctx, clientID)
}
func (m *Manager) Login(ctx context.Context, credential string) (string, Session, error) {
	now := m.now()
	m.mu.Lock()
	first := 0
	for first < len(m.attempts) && !m.attempts[first].After(now.Add(-time.Minute)) {
		first++
	}
	m.attempts = m.attempts[first:]
	if len(m.attempts) >= loginLimit {
		m.mu.Unlock()
		return "", Session{}, ErrRateLimited
	}
	m.attempts = append(m.attempts, now)
	m.mu.Unlock()
	identity, err := m.Bearer(ctx, credential)
	if err != nil {
		return "", Session{}, err
	}
	if identity.Client.Kind != "dashboard" {
		return "", Session{}, ErrUnauthorized
	}
	session := Session{ClientID: identity.Client.ID, CSRF: secret(), Expires: now.Add(SessionLifetime)}
	token := secret()
	m.mu.Lock()
	defer m.mu.Unlock()
	for hash, existing := range m.sessions {
		if !existing.Expires.After(now) {
			delete(m.sessions, hash)
		}
	}
	if len(m.sessions) >= maxSessions {
		return "", Session{}, ErrRateLimited
	}
	m.sessions[sha256.Sum256([]byte(token))] = session
	return token, session, nil
}
func (m *Manager) AuthenticateSession(ctx context.Context, token string) (Identity, Session, error) {
	if len(token) > 128 {
		return Identity{}, Session{}, ErrUnauthorized
	}
	hash := sha256.Sum256([]byte(token))
	m.mu.Lock()
	session, ok := m.sessions[hash]
	if ok && !session.Expires.After(m.now()) {
		delete(m.sessions, hash)
		ok = false
	}
	m.mu.Unlock()
	if !ok || m.resolver == nil {
		return Identity{}, Session{}, ErrUnauthorized
	}
	identity, err := m.resolver.ResolveClient(ctx, session.ClientID)
	if err != nil {
		return Identity{}, Session{}, err
	}
	if identity.Client.Kind != "dashboard" {
		return Identity{}, Session{}, ErrUnauthorized
	}
	return identity, session, nil
}
func (m *Manager) Logout(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, sha256.Sum256([]byte(token)))
}
