package ratelimit

import (
	"testing"
	"time"
)

func TestBurstThenRefill(t *testing.T) {
	now := time.Unix(1700000000, 0)
	l := New(1, 3) // 3 immediately, then one per second
	l.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if !l.Allow("a") {
			t.Fatalf("event %d should be allowed within the burst", i)
		}
	}
	if l.Allow("a") {
		t.Fatal("fourth event should be denied")
	}
	// A different key has its own bucket.
	if !l.Allow("b") {
		t.Fatal("a fresh key should be allowed")
	}
	now = now.Add(time.Second)
	if !l.Allow("a") {
		t.Fatal("one token should have refilled after a second")
	}
	if l.Allow("a") {
		t.Fatal("only one token refilled, so the next should be denied")
	}
}

func TestRefillCapsAtBurst(t *testing.T) {
	now := time.Unix(1700000000, 0)
	l := New(10, 2)
	l.now = func() time.Time { return now }
	l.Allow("a")
	now = now.Add(time.Hour) // would refill 36000 tokens if uncapped
	if !l.Allow("a") || !l.Allow("a") {
		t.Fatal("burst should be available after a long idle period")
	}
	if l.Allow("a") {
		t.Fatal("refill must cap at the burst size")
	}
}

func TestZeroRateDisables(t *testing.T) {
	l := New(0, 0)
	for i := 0; i < 100; i++ {
		if !l.Allow("a") {
			t.Fatal("a zero rate must disable limiting entirely")
		}
	}
	if l.Len() != 0 {
		t.Fatal("a disabled limiter should not allocate buckets")
	}
}

func TestSweepDropsIdleBuckets(t *testing.T) {
	now := time.Unix(1700000000, 0)
	l := New(1, 2)
	l.now = func() time.Time { return now }
	l.Allow("a")
	l.Allow("b")
	if l.Len() != 2 {
		t.Fatalf("want 2 buckets, got %d", l.Len())
	}
	now = now.Add(sweepEvery + time.Minute)
	l.Allow("c") // triggers the sweep
	if l.Len() != 1 {
		t.Fatalf("idle buckets should be swept, got %d", l.Len())
	}
}

func TestNilLimiterAllows(t *testing.T) {
	var l *Limiter
	if !l.Allow("a") {
		t.Fatal("a nil limiter must allow everything")
	}
}
