package attempts

import (
	"context"
	"sync"
	"time"

	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth"
)

const (
	defaultCapacity = 10_000
	defaultLimit    = 5
	defaultWindow   = time.Minute
)

type entry struct {
	start            time.Time
	failed, inflight int
}

type Config struct {
	Now      func() time.Time
	Capacity int
	Limit    int
	Window   time.Duration
}

type Limiter struct {
	mu       sync.Mutex
	entries  map[string]*entry
	now      func() time.Time
	capacity int
	limit    int
	window   time.Duration
}

// New defaults nonpositive settings to five attempts per minute and 10,000 identities.
func New(config Config) *Limiter {
	now, capacity := config.Now, config.Capacity
	if now == nil {
		now = time.Now
	}
	if capacity <= 0 {
		capacity = defaultCapacity
	}
	limit, window := config.Limit, config.Window
	if limit <= 0 {
		limit = defaultLimit
	}
	if window <= 0 {
		window = defaultWindow
	}
	return &Limiter{entries: make(map[string]*entry), now: now, capacity: capacity, limit: limit, window: window}
}

func (l *Limiter) Begin(ctx context.Context, email string) (func(bool), time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	value := l.entries[email]
	if value != nil && l.expired(value, now) {
		delete(l.entries, email)
		value = nil
	}
	if value == nil {
		if len(l.entries) >= l.capacity {
			for key, item := range l.entries {
				if l.expired(item, now) {
					delete(l.entries, key)
				}
			}
		}
		if len(l.entries) >= l.capacity {
			return nil, l.window, auth.ErrRateLimited
		}
		value = &entry{start: now}
		l.entries[email] = value
	}
	if value.failed+value.inflight >= l.limit {
		return nil, value.start.Add(l.window).Sub(now), auth.ErrRateLimited
	}
	value.inflight++
	var once sync.Once
	finish := func(failed bool) {
		once.Do(func() { l.complete(email, value, failed) })
	}
	return finish, 0, nil
}

func (l *Limiter) expired(value *entry, now time.Time) bool {
	return now.Before(value.start) || !now.Before(value.start.Add(l.window))
}

func (l *Limiter) complete(email string, value *entry, failed bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Pointer identity prevents late completions from charging a replacement window.
	if l.entries[email] != value {
		return
	}
	value.inflight--
	if failed {
		value.failed++
	}
	if value.failed+value.inflight == 0 {
		delete(l.entries, email)
	}
}
