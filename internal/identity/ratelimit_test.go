package identity

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/neverbot/maestro/internal/assert"
)

func TestLimiterBlocksAfterMaxAttempts(t *testing.T) {
	t.Parallel()
	l := NewLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		assert.Must(t, l.Allowed("designer@example.test"), "attempt %d was blocked too early", i+1)
		l.Record("designer@example.test")
	}
	assert.Must(t, !l.Allowed("designer@example.test"), "the fourth attempt should have been blocked")
	assert.Must(t, l.Allowed("someone-else@example.test"), "a different key must not be affected")
}

func TestLimiterNormalizesKeyCasingAndWhitespace(t *testing.T) {
	t.Parallel()
	l := NewLimiter(1, time.Minute)
	l.Record("Bob@Example.test")
	assert.Must(t, !l.Allowed(" bob@example.test "), "a differently-cased, differently-spaced key must share the same budget")
}

func TestLimiterAllowedDoesNotCharge(t *testing.T) {
	t.Parallel()
	l := NewLimiter(1, time.Minute)
	for i := 0; i < 5; i++ {
		assert.Must(t, l.Allowed("k"), "Allowed call %d should never itself spend the budget", i+1)
	}
}

func TestLimiterForgetsAfterWindow(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	l := NewLimiter(1, 10*time.Millisecond)
	l.now = clock.now

	l.Record("k")
	assert.Must(t, !l.Allowed("k"), "second attempt inside the window should be blocked")
	clock.advance(20 * time.Millisecond)
	assert.Must(t, l.Allowed("k"), "the window should have expired")
}

// TestLimiterSweepsStaleKeys checks that a sweep boundary actually removes
// keys whose window has fully elapsed, using a fake clock so the outcome
// is exact rather than a loose bound that could pass by coincidence even
// without a working sweep.
func TestLimiterSweepsStaleKeys(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	l := NewLimiter(1, time.Minute)
	l.now = clock.now

	for i := 0; i < sweepEvery; i++ {
		l.Record(fmt.Sprintf("stale-%d", i))
	}

	clock.advance(2 * time.Minute) // past the window

	// Cross exactly one more sweep boundary with fresh traffic.
	for i := 0; i < sweepEvery; i++ {
		l.Record(fmt.Sprintf("fresh-%d", i))
	}

	l.mu.Lock()
	n := len(l.attempts)
	l.mu.Unlock()

	assert.Must(t, n == sweepEvery, "attempts map holds %d keys after a sweep boundary, want exactly %d (only the fresh ones)", n, sweepEvery)
}

// TestLimiterConcurrentAccess exercises Allowed and Record from many
// goroutines against both a key they all share and keys unique to each
// goroutine, under -race. Nothing here asserts on counts: the point is
// that the race detector finds no unsynchronized access to the map.
func TestLimiterConcurrentAccess(t *testing.T) {
	t.Parallel()
	l := NewLimiter(1000, time.Minute)
	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			own := fmt.Sprintf("own-key-%d", g)
			for i := 0; i < 50; i++ {
				l.Allowed("shared-key")
				l.Record("shared-key")
				l.Allowed(own)
				l.Record(own)
			}
		}(g)
	}
	wg.Wait()
}

// fakeClock lets tests advance time deterministically instead of sleeping,
// which on a loaded CI runner can make a real-clock window test flake in a
// way that is not reproducible locally.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Now()}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}
