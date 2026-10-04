package limiter

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a manually advanced clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func mustNew(t *testing.T, rate float64, burst int, opts ...Option) *Limiter {
	t.Helper()
	l, err := New(rate, burst, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func mustAllow(t *testing.T, l *Limiter, key string, cost int) Result {
	t.Helper()
	r, err := l.Allow(key, cost)
	if err != nil {
		t.Fatalf("Allow(%q, %d): %v", key, cost, err)
	}
	return r
}

func TestNewValidates(t *testing.T) {
	for _, tc := range []struct {
		rate  float64
		burst int
	}{{0, 5}, {-1, 5}, {5, 0}, {5, -2}} {
		if _, err := New(tc.rate, tc.burst); err == nil {
			t.Errorf("New(%v, %d) succeeded, want error", tc.rate, tc.burst)
		}
	}
}

func TestBurstThenDeny(t *testing.T) {
	clk := newClock()
	l := mustNew(t, 1, 3, WithClock(clk.Now))

	for i := 0; i < 3; i++ {
		r := mustAllow(t, l, "k", 1)
		if !r.Allowed || r.Remaining != 2-i {
			t.Fatalf("request %d: got %+v, want allowed with %d remaining", i, r, 2-i)
		}
	}
	r := mustAllow(t, l, "k", 1)
	if r.Allowed {
		t.Fatalf("4th request allowed, want denied: %+v", r)
	}
	if r.RetryAfter != time.Second {
		t.Fatalf("RetryAfter = %v, want 1s", r.RetryAfter)
	}
}

func TestRefillOverTime(t *testing.T) {
	clk := newClock()
	l := mustNew(t, 2, 4, WithClock(clk.Now)) // 2 tokens/sec

	for i := 0; i < 4; i++ {
		mustAllow(t, l, "k", 1)
	}
	if mustAllow(t, l, "k", 1).Allowed {
		t.Fatal("bucket should be empty")
	}

	clk.Advance(500 * time.Millisecond) // +1 token
	if r := mustAllow(t, l, "k", 1); !r.Allowed || r.Remaining != 0 {
		t.Fatalf("after 0.5s: %+v, want allowed with 0 remaining", r)
	}
	if mustAllow(t, l, "k", 1).Allowed {
		t.Fatal("only one token should have refilled")
	}

	clk.Advance(time.Hour) // refill is capped at burst
	if r := mustAllow(t, l, "k", 1); !r.Allowed || r.Remaining != 3 {
		t.Fatalf("after long idle: %+v, want allowed with 3 remaining", r)
	}
}

func TestRetryAfterReflectsShortfall(t *testing.T) {
	clk := newClock()
	l := mustNew(t, 2, 4, WithClock(clk.Now))
	mustAllow(t, l, "k", 4) // drain

	r := mustAllow(t, l, "k", 3) // need 3 tokens at 2/s -> 1.5s
	if r.Allowed || r.RetryAfter != 1500*time.Millisecond {
		t.Fatalf("got %+v, want denied with RetryAfter 1.5s", r)
	}
	// Honouring RetryAfter must actually make the request succeed.
	clk.Advance(r.RetryAfter)
	if !mustAllow(t, l, "k", 3).Allowed {
		t.Fatal("request still denied after waiting RetryAfter")
	}
}

func TestDeniedRequestsDoNotSpendTokens(t *testing.T) {
	clk := newClock()
	l := mustNew(t, 1, 2, WithClock(clk.Now))
	mustAllow(t, l, "k", 2)
	for i := 0; i < 10; i++ {
		mustAllow(t, l, "k", 1) // all denied
	}
	clk.Advance(time.Second)
	if !mustAllow(t, l, "k", 1).Allowed {
		t.Fatal("denied requests must not push the bucket further into debt")
	}
}

func TestKeysAreIndependent(t *testing.T) {
	clk := newClock()
	l := mustNew(t, 1, 1, WithClock(clk.Now))
	if !mustAllow(t, l, "a", 1).Allowed {
		t.Fatal("a should be allowed")
	}
	if mustAllow(t, l, "a", 1).Allowed {
		t.Fatal("a should now be denied")
	}
	if !mustAllow(t, l, "b", 1).Allowed {
		t.Fatal("b has its own bucket and should be allowed")
	}
}

func TestCostValidation(t *testing.T) {
	l := mustNew(t, 1, 5)
	for _, cost := range []int{0, -1, 6} {
		if _, err := l.Allow("k", cost); !errors.Is(err, ErrCost) {
			t.Errorf("cost %d: err = %v, want ErrCost", cost, err)
		}
	}
	if r := mustAllow(t, l, "k", 5); !r.Allowed || r.Remaining != 0 {
		t.Errorf("cost == burst should work once: %+v", r)
	}
}

func TestCleanupOnlyEvictsFullyRefilledBuckets(t *testing.T) {
	clk := newClock()
	l := mustNew(t, 1, 10, WithClock(clk.Now)) // needs 10s to refill fully
	mustAllow(t, l, "old", 1)
	clk.Advance(6 * time.Second)
	mustAllow(t, l, "recent", 8) // 2 tokens left
	clk.Advance(5 * time.Second) // old idle 11s (full), recent idle 5s (7 tokens)

	if n := l.Cleanup(); n != 1 {
		t.Fatalf("Cleanup() = %d, want 1", n)
	}
	if l.Len() != 1 || l.Evicted() != 1 {
		t.Fatalf("Len=%d Evicted=%d, want 1 and 1", l.Len(), l.Evicted())
	}

	// "recent" kept its state (2 + 5 refilled - 1 spent = 6). Had it been
	// evicted it would restart full and report 9 remaining.
	if r := mustAllow(t, l, "recent", 1); r.Remaining != 6 {
		t.Fatalf("recent bucket state lost: %+v", r)
	}
}

func TestMaxKeys(t *testing.T) {
	clk := newClock()
	l := mustNew(t, 1, 1, WithClock(clk.Now), WithMaxKeys(2))
	mustAllow(t, l, "a", 1)
	mustAllow(t, l, "b", 1)

	if _, err := l.Allow("c", 1); !errors.Is(err, ErrCapacity) {
		t.Fatalf("err = %v, want ErrCapacity", err)
	}
	// Existing keys keep working at capacity.
	if _, err := l.Allow("a", 1); err != nil {
		t.Fatalf("existing key rejected at capacity: %v", err)
	}

	// Once the old buckets have refilled they are evicted to admit "c".
	clk.Advance(2 * time.Second)
	if r := mustAllow(t, l, "c", 1); !r.Allowed {
		t.Fatalf("new key should be admitted after eviction: %+v", r)
	}
}

// With a frozen clock exactly `burst` requests may succeed no matter how many
// goroutines race; run with -race.
func TestConcurrentAllowIsExact(t *testing.T) {
	clk := newClock()
	const burst, workers, perWorker = 100, 32, 200
	l := mustNew(t, 1, burst, WithClock(clk.Now))

	var allowed atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				if r, _ := l.Allow("shared", 1); r.Allowed {
					allowed.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got != burst {
		t.Fatalf("%d requests allowed, want exactly %d", got, burst)
	}
}

func TestConcurrentManyKeys(t *testing.T) {
	l := mustNew(t, 1000, 10, WithMaxKeys(100_000))
	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				_, _ = l.Allow(fmt.Sprintf("k-%d-%d", w, i%50), 1)
				if i%100 == 0 {
					l.Cleanup()
				}
			}
		}(w)
	}
	wg.Wait()
}

func BenchmarkAllow(b *testing.B) {
	l, _ := New(1e9, 1000)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = l.Allow("bench", 1)
		}
	})
}
