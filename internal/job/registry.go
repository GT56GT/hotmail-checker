// Package job tracks in-flight checks and enforces one job per user.
//
// There is no persistence by design: a dyno restart drops in-flight jobs and
// the user resends. Delivered result files live in Telegram, not on disk.
package job

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrBusy     = errors.New("a job is already running")
	ErrCooldown = errors.New("cooldown active")
)

// Hit is one validated account.
type Hit struct {
	Email   string
	Name    string
	Cookie  string
	Cookies map[string]string
	Proxy   string
}

// Counts is an immutable snapshot of a job's progress.
type Counts struct {
	Live       int
	Dead       int
	Challenged int
	Errors     int
}

// Done reports how many accounts have been processed.
func (c Counts) Done() int { return c.Live + c.Dead + c.Challenged + c.Errors }

// Job is a single user's batch run.
type Job struct {
	ID      string
	UserID  int64
	Total   int
	Source  string
	Started time.Time
	Ctx     context.Context

	live       atomic.Int32
	dead       atomic.Int32
	challenged atomic.Int32
	errors     atomic.Int32

	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once

	mu       sync.Mutex
	hits     []Hit
	deadList []string
	chalList []string
	errs     []string
}

// AddHit records a live account.
func (j *Job) AddHit(h Hit) {
	j.mu.Lock()
	j.hits = append(j.hits, h)
	j.mu.Unlock()
	j.live.Add(1)
}

// AddDead records an expired cookie. Only a short label is retained so a large
// batch does not pin every cookie string in memory.
func (j *Job) AddDead(label string) {
	j.mu.Lock()
	j.deadList = append(j.deadList, label)
	j.mu.Unlock()
	j.dead.Add(1)
}

// AddChallenged records a session that needs consent or re-authentication.
func (j *Job) AddChallenged(reason string) {
	j.mu.Lock()
	j.chalList = append(j.chalList, reason)
	j.mu.Unlock()
	j.challenged.Add(1)
}

// AddErr records a failed check with its reason.
func (j *Job) AddErr(reason string) {
	j.mu.Lock()
	j.errs = append(j.errs, reason)
	j.mu.Unlock()
	j.errors.Add(1)
}

// Counts returns a snapshot for progress rendering.
func (j *Job) Counts() Counts {
	return Counts{
		Live:       int(j.live.Load()),
		Dead:       int(j.dead.Load()),
		Challenged: int(j.challenged.Load()),
		Errors:     int(j.errors.Load()),
	}
}

// Results returns copies of the accumulated lists.
func (j *Job) Results() (hits []Hit, dead []string, challenged []string, errs []string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]Hit(nil), j.hits...),
		append([]string(nil), j.deadList...),
		append([]string(nil), j.chalList...),
		append([]string(nil), j.errs...)
}

// Cancel stops the job's context.
func (j *Job) Cancel() { j.cancel() }

// Done closes when the job finishes, exactly once.
func (j *Job) Done() { j.once.Do(func() { close(j.done) }) }

// Elapsed is the wall time since start.
func (j *Job) Elapsed() time.Duration { return time.Since(j.Started) }

// Registry holds active jobs keyed by user.
type Registry struct {
	mu       sync.Mutex
	active   map[int64]*Job
	lastRun  map[int64]time.Time
	cooldown time.Duration
}

// NewRegistry builds a registry with the given per-user cooldown.
func NewRegistry(cooldown time.Duration) *Registry {
	return &Registry{
		active:   make(map[int64]*Job),
		lastRun:  make(map[int64]time.Time),
		cooldown: cooldown,
	}
}

// Begin starts a job for a user, rejecting when one is already running or the
// cooldown has not elapsed.
func (r *Registry) Begin(parent context.Context, userID int64, total int) (*Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, busy := r.active[userID]; busy {
		return nil, ErrBusy
	}
	if last, ok := r.lastRun[userID]; ok {
		if remaining := r.cooldown - time.Since(last); remaining > 0 {
			return nil, fmt.Errorf("%w: %ds remaining", ErrCooldown, int(remaining.Seconds()))
		}
	}

	ctx, cancel := context.WithCancel(parent)
	j := &Job{
		ID:      fmt.Sprintf("%d-%d", time.Now().UnixNano()/1e6, userID),
		UserID:  userID,
		Total:   total,
		Started: time.Now(),
		Ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
	r.active[userID] = j
	return j, nil
}

// Finish removes the job from the active set and starts the cooldown.
func (r *Registry) Finish(userID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.active, userID)
	r.lastRun[userID] = time.Now()
}

// Cancel stops a running job. Reports whether one was running.
func (r *Registry) Cancel(userID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.active[userID]
	if !ok {
		return false
	}
	j.Cancel()
	return true
}

// Active returns the user's running job, or nil.
func (r *Registry) Active(userID int64) *Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active[userID]
}

// Count reports how many jobs are running.
func (r *Registry) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.active)
}
