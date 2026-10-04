// Package limiter implements a per-key token-bucket rate limiter.
//
// Each key owns a bucket holding up to burst tokens that refills continuously
// at rate tokens per second. A check spends tokens; if the bucket can't cover
// the cost the request is denied and told how long to wait.
package limiter

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// ErrCapacity means the limiter is tracking MaxKeys keys and none are idle
	// enough to evict, so a new key can't be admitted.
	ErrCapacity = errors.New("limiter: key capacity exhausted")
	// ErrCost means the requested cost is below 1 or above the burst size
	// (such a request could never succeed).
	ErrCost = errors.New("limiter: cost must be between 1 and burst")
)

// Result is the outcome of a single check.
type Result struct {
	Allowed    bool
	Remaining  int           // whole tokens left after this check
	RetryAfter time.Duration // how long until the cost would fit; 0 if allowed
}

type bucket struct {
	tokens float64
	last   time.Time // when tokens was last brought up to date
}

// Limiter is safe for concurrent use.
type Limiter struct {
	rate    float64 // tokens per second
	burst   float64
	maxKeys int
	now     func() time.Time

	// A bucket idle for longer than refill is guaranteed to be full, i.e.
	// indistinguishable from a brand-new one, so it can be dropped losslessly.
	refill time.Duration

	mu      sync.Mutex
	buckets map[string]*bucket
	evicted atomic.Uint64
}

// Option customises a Limiter.
type Option func(*Limiter)

// WithClock replaces time.Now (for tests).
func WithClock(now func() time.Time) Option {
	return func(l *Limiter) { l.now = now }
}

// WithMaxKeys caps how many distinct keys are tracked (0 = unlimited).
func WithMaxKeys(n int) Option {
	return func(l *Limiter) { l.maxKeys = n }
}

// New returns a limiter that refills rate tokens per second up to burst.
func New(rate float64, burst int, opts ...Option) (*Limiter, error) {
	if !(rate > 0) || math.IsInf(rate, 0) {
		return nil, fmt.Errorf("limiter: rate must be a positive number, got %v", rate)
	}
	if burst < 1 {
		return nil, fmt.Errorf("limiter: burst must be at least 1, got %d", burst)
	}
	l := &Limiter{
		rate:    rate,
		burst:   float64(burst),
		now:     time.Now,
		buckets: make(map[string]*bucket),
		refill:  time.Duration(math.Ceil(float64(burst) / rate * float64(time.Second))),
	}
	for _, opt := range opts {
		opt(l)
	}
	return l, nil
}

// Rate returns the refill rate in tokens per second.
func (l *Limiter) Rate() float64 { return l.rate }

// Burst returns the bucket size.
func (l *Limiter) Burst() int { return int(l.burst) }

// Allow spends cost tokens from key's bucket if it has them.
func (l *Limiter) Allow(key string, cost int) (Result, error) {
	if cost < 1 || float64(cost) > l.burst {
		return Result{}, ErrCost
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		if l.maxKeys > 0 && len(l.buckets) >= l.maxKeys {
			l.sweepLocked(now)
			if len(l.buckets) >= l.maxKeys {
				return Result{}, ErrCapacity
			}
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	} else {
		if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
			b.tokens = math.Min(l.burst, b.tokens+elapsed*l.rate)
		}
		b.last = now
	}

	c := float64(cost)
	if b.tokens >= c {
		b.tokens -= c
		return Result{Allowed: true, Remaining: int(math.Floor(b.tokens))}, nil
	}
	wait := time.Duration(math.Ceil((c - b.tokens) / l.rate * float64(time.Second)))
	return Result{Allowed: false, Remaining: int(math.Floor(b.tokens)), RetryAfter: wait}, nil
}

// Len returns the number of keys currently tracked.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// Evicted returns how many idle keys have been dropped so far.
func (l *Limiter) Evicted() uint64 { return l.evicted.Load() }

// Cleanup drops buckets that have fully refilled and returns how many.
func (l *Limiter) Cleanup() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sweepLocked(l.now())
}

func (l *Limiter) sweepLocked(now time.Time) int {
	n := 0
	for k, b := range l.buckets {
		if now.Sub(b.last) >= l.refill {
			delete(l.buckets, k)
			n++
		}
	}
	l.evicted.Add(uint64(n))
	return n
}

// RunJanitor calls Cleanup every interval until ctx is cancelled.
func (l *Limiter) RunJanitor(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.Cleanup()
		}
	}
}
