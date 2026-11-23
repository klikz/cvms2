// Package loginlimit blocks brute-force login attempts.
package loginlimit

import (
	"sync"
	"time"
)

type entry struct {
	fails        int
	first        time.Time
	blockedUntil time.Time
}

// Limiter counts failed attempts per key (e.g. ip|username).
type Limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	block  time.Duration
	m      map[string]*entry
}

// New creates a limiter: after max failures within window, the key is
// blocked for block.
func New(max int, window, block time.Duration) *Limiter {
	if max <= 0 {
		max = 5
	}
	if window <= 0 {
		window = 15 * time.Minute
	}
	if block <= 0 {
		block = 15 * time.Minute
	}
	return &Limiter{max: max, window: window, block: block, m: map[string]*entry{}}
}

// Blocked reports whether key is blocked and for how long.
func (l *Limiter) Blocked(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.m[key]
	if e == nil {
		return false, 0
	}
	if d := time.Until(e.blockedUntil); d > 0 {
		return true, d
	}
	return false, 0
}

// Fail records a failed attempt. It returns true if the key became blocked.
func (l *Limiter) Fail(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	e := l.m[key]
	if e == nil || now.Sub(e.first) > l.window {
		e = &entry{first: now}
		l.m[key] = e
	}
	e.fails++
	if e.fails >= l.max {
		e.blockedUntil = now.Add(l.block)
		e.fails = 0
		e.first = now
		return true
	}
	// opportunistic cleanup
	if len(l.m) > 10000 {
		for k, v := range l.m {
			if now.Sub(v.first) > l.window && now.After(v.blockedUntil) {
				delete(l.m, k)
			}
		}
	}
	return false
}

// Success clears the key.
func (l *Limiter) Success(key string) {
	l.mu.Lock()
	delete(l.m, key)
	l.mu.Unlock()
}
