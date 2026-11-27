package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrNoProxy is returned when every proxy is quarantined.
var ErrNoProxy = errors.New("no healthy proxy available")

const (
	maxFails   = 3
	quarantine = 2 * time.Minute
)

// Entry is one proxy with its health state.
type Entry struct {
	URL string

	mu        sync.Mutex
	fails     int
	dead      bool
	deadUntil time.Time
}

func (e *Entry) available() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.dead {
		return true
	}
	if time.Now().After(e.deadUntil) {
		e.dead = false
		e.fails = 0
		return true
	}
	return false
}

func (e *Entry) healthy() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return !e.dead
}

// Pool round-robins across proxies and caps concurrent use per proxy.
type Pool struct {
	mu       sync.RWMutex
	entries  []*Entry
	next     atomic.Uint64
	sems     sync.Map // proxy URL -> chan struct{}
	perProxy int
}

// New builds a pool. perProxy is the simultaneous check cap per proxy.
func New(perProxy int) *Pool {
	if perProxy < 1 {
		perProxy = 4
	}
	return &Pool{perProxy: perProxy}
}

// Len reports the number of loaded proxies.
func (p *Pool) Len() int {
	if p == nil {
		return 0
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.entries)
}

// Stats returns total and currently healthy counts.
func (p *Pool) Stats() (total, healthy int) {
	if p == nil {
		return 0, 0
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, e := range p.entries {
		if e.healthy() {
			healthy++
		}
	}
	return len(p.entries), healthy
}

// Load replaces the pool contents. Entries are normalised; invalid ones are
// skipped.
func (p *Pool) Load(raw []string) {
	seen := make(map[string]struct{}, len(raw))
	entries := make([]*Entry, 0, len(raw))

	for _, line := range raw {
		u, err := Normalise(line)
		if err != nil {
			continue
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		entries = append(entries, &Entry{URL: u})
	}

	p.mu.Lock()
	p.entries = entries
	p.mu.Unlock()
}

// Fetch downloads a proxy list. One entry per line; blank lines and
// comments are ignored.
func (p *Pool) Fetch(ctx context.Context, listURL string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("proxy list returned %d", resp.StatusCode)
	}

	var lines []string
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return err
	}

	urls, _ := ParseList(strings.Join(lines, "\n"))
	p.Load(urls)
	slog.Info("proxy list refreshed", "count", p.Len())
	return nil
}

// RefreshLoop re-fetches the proxy list on an interval until ctx is done.
func (p *Pool) RefreshLoop(ctx context.Context, listURL string, interval time.Duration) {
	if listURL == "" || interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := p.Fetch(ctx, listURL); err != nil {
				slog.Warn("proxy refresh failed", "err", err)
			}
		}
	}
}

// Acquire reserves one slot on a healthy proxy. The returned release func must
// be called exactly once. With no proxies configured it returns a direct entry.
func (p *Pool) Acquire(ctx context.Context) (*Entry, func(), error) {
	for attempt := 0; attempt < 6; attempt++ {
		if p.Len() == 0 {
			return &Entry{}, func() {}, nil
		}

		e := p.pick()
		if e == nil {
			select {
			case <-time.After(250 * time.Millisecond):
				continue
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}

		sem := p.semaphore(e.URL)
		select {
		case sem <- struct{}{}:
			return e, func() { <-sem }, nil
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	return nil, nil, ErrNoProxy
}

// Report records the outcome of a check against an entry.
func (p *Pool) Report(e *Entry, ok bool) {
	if e == nil || e.URL == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if ok {
		e.fails = 0
		e.dead = false
		return
	}
	e.fails++
	if e.fails >= maxFails {
		e.dead = true
		e.deadUntil = time.Now().Add(quarantine)
	}
}

func (p *Pool) pick() *Entry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := len(p.entries)
	if n == 0 {
		return nil
	}
	start := int(p.next.Add(1) % uint64(n))
	for i := 0; i < n; i++ {
		e := p.entries[(start+i)%n]
		if e.available() {
			return e
		}
	}
	return nil
}

func (p *Pool) semaphore(key string) chan struct{} {
	if v, ok := p.sems.Load(key); ok {
		return v.(chan struct{})
	}
	ch := make(chan struct{}, p.perProxy)
	actual, _ := p.sems.LoadOrStore(key, ch)
	return actual.(chan struct{})
}
