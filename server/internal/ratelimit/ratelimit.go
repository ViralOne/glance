// Package ratelimit is a small in-memory token bucket keyed by string, used
// to cap the unauthenticated collect endpoint and the login form.
//
// Buckets are held in a map guarded by one mutex and swept periodically, so
// the memory cost is bounded by the number of distinct keys seen in a sweep
// window rather than by traffic. Nothing is persisted: a restart forgives
// everyone, which is the right trade for an analytics box.
package ratelimit

import (
	"sync"
	"time"
)

type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter allows a burst of events per key and refills at a steady rate.
type Limiter struct {
	rate  float64 // tokens per second
	burst float64

	mu      sync.Mutex
	buckets map[string]*bucket
	swept   time.Time
	now     func() time.Time
}

func New(ratePerSecond float64, burst int) *Limiter {
	if ratePerSecond <= 0 || burst <= 0 {
		ratePerSecond, burst = 0, 0
	}
	return &Limiter{rate: ratePerSecond, burst: float64(burst), buckets: map[string]*bucket{}, now: time.Now}
}

// sweepEvery drops buckets that have been idle long enough to be full again.
const sweepEvery = 5 * time.Minute

// Allow reports whether one event for key is within the limit, consuming a
// token when it is.
func (l *Limiter) Allow(key string) bool {
	if l == nil || l.rate <= 0 {
		return true
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.swept) > sweepEvery {
		l.sweep(now)
		l.swept = now
	}
	b, ok := l.buckets[key]
	if !ok {
		// A fresh key starts full, minus the event we are about to allow.
		l.buckets[key] = &bucket{tokens: l.burst - 1, last: now}
		return true
	}
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweep removes buckets that have refilled completely; they carry no state.
func (l *Limiter) sweep(now time.Time) {
	full := time.Duration(l.burst/l.rate*float64(time.Second)) + time.Second
	for k, b := range l.buckets {
		if now.Sub(b.last) > full {
			delete(l.buckets, k)
		}
	}
}

// Len reports how many buckets are held, for tests and the status endpoint.
func (l *Limiter) Len() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
