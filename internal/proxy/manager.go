package proxy

import (
	"context"
	"log/slog"
	"sync"
)

// Store persists per-user proxy lists across restarts.
type Store interface {
	Load(ctx context.Context, userID int64) ([]string, error)
	Save(ctx context.Context, userID int64, proxies []string) error
}

type userSet struct {
	urls []string
	pool *Pool
}

// Manager resolves which proxy pool a user's checks should use: their own
// list when one is configured, otherwise the shared global pool.
type Manager struct {
	mu       sync.Mutex
	global   *Pool
	users    map[int64]*userSet
	store    Store
	perProxy int
}

// NewManager builds a manager. store may be nil, in which case user lists live
// only for the life of the process.
func NewManager(perProxy int, store Store) *Manager {
	return &Manager{
		global:   New(perProxy),
		users:    make(map[int64]*userSet),
		store:    store,
		perProxy: perProxy,
	}
}

// Global returns the shared pool loaded from the environment.
func (m *Manager) Global() *Pool { return m.global }

// Persistent reports whether user lists survive a restart.
func (m *Manager) Persistent() bool { return m.store != nil }

// URLs returns the user's proxy list, loading it from the store on first use.
func (m *Manager) URLs(ctx context.Context, userID int64) []string {
	m.mu.Lock()
	set, ok := m.users[userID]
	m.mu.Unlock()
	if ok {
		return set.urls
	}

	if m.store == nil {
		return nil
	}

	raw, err := m.store.Load(ctx, userID)
	if err != nil {
		slog.Warn("load user proxies", "user", userID, "err", err)
		return nil
	}
	urls, _ := ParseList(joinLines(raw))
	m.cache(userID, urls)
	return urls
}

// Pool returns the pool to use for a user.
func (m *Manager) Pool(ctx context.Context, userID int64) *Pool {
	urls := m.URLs(ctx, userID)
	if len(urls) == 0 {
		return m.global
	}

	m.mu.Lock()
	set, ok := m.users[userID]
	m.mu.Unlock()
	if ok && set.pool != nil {
		return set.pool
	}
	return m.global
}

// Save replaces the user's proxy list.
func (m *Manager) Save(ctx context.Context, userID int64, urls []string) error {
	m.cache(userID, urls)
	if m.store != nil {
		return m.store.Save(ctx, userID, urls)
	}
	return nil
}

// Delete clears the user's proxy list.
func (m *Manager) Delete(ctx context.Context, userID int64) error {
	m.cache(userID, nil)
	if m.store != nil {
		return m.store.Save(ctx, userID, nil)
	}
	return nil
}

func (m *Manager) cache(userID int64, urls []string) {
	set := &userSet{urls: urls}
	if len(urls) > 0 {
		p := New(m.perProxy)
		p.Load(urls)
		set.pool = p
	}
	m.mu.Lock()
	m.users[userID] = set
	m.mu.Unlock()
}

func joinLines(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}
