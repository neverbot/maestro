package identity

import (
	"fmt"
	"testing"
	"time"
)

func TestLimiterBlocksAfterMaxAttempts(t *testing.T) {
	l := NewLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !l.Allow("designer@studio.com") {
			t.Fatalf("attempt %d was blocked too early", i+1)
		}
	}
	if l.Allow("designer@studio.com") {
		t.Fatal("the fourth attempt should have been blocked")
	}
	if !l.Allow("someone-else@studio.com") {
		t.Fatal("a different key must not be affected")
	}
}

func TestLimiterResetsOnSuccess(t *testing.T) {
	l := NewLimiter(2, time.Minute)
	l.Allow("k")
	l.Allow("k")
	l.Reset("k")
	if !l.Allow("k") {
		t.Fatal("Reset should clear the recorded attempts")
	}
}

func TestLimiterForgetsAfterWindow(t *testing.T) {
	l := NewLimiter(1, 10*time.Millisecond)
	l.Allow("k")
	if l.Allow("k") {
		t.Fatal("second attempt inside the window should be blocked")
	}
	time.Sleep(20 * time.Millisecond)
	if !l.Allow("k") {
		t.Fatal("the window should have expired")
	}
}

// TestLimiterDoesNotLeakStaleKeysForever guards against unbounded memory
// growth: a caller that keys the limiter by an attacker-controlled value
// (an attempted email, say) can be fed an endless stream of distinct keys,
// each queried once. If nothing ever forgets a key once its window has
// passed, that alone is a memory-exhaustion denial of service, entirely
// independent of the max/window accounting above. This test creates many
// distinct keys, lets their window elapse, then drives enough fresh traffic
// to cross a sweep boundary, and checks that the stale entries were
// actually removed from the internal map rather than merely handled
// correctly by Allow.
func TestLimiterDoesNotLeakStaleKeysForever(t *testing.T) {
	l := NewLimiter(1, 20*time.Millisecond)

	staleCount := sweepEvery * 3
	for i := 0; i < staleCount; i++ {
		l.Allow(fmt.Sprintf("stale-%d", i))
	}

	time.Sleep(30 * time.Millisecond) // let every key above fall outside the window

	// Cross a sweep boundary with fresh traffic so the periodic sweep runs.
	for i := 0; i < sweepEvery; i++ {
		l.Allow(fmt.Sprintf("fresh-%d", i))
	}

	l.mu.Lock()
	n := len(l.attempts)
	l.mu.Unlock()

	if n > 2*sweepEvery {
		t.Fatalf("attempts map holds %d keys after stale entries should have been swept, want at most %d", n, 2*sweepEvery)
	}
}
