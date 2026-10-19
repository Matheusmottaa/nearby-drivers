package matching

import (
	"testing"
	"time"
)

func TestBreakerOpensAndProbes(t *testing.T) {
	now := time.Unix(0, 0)
	b := NewBreaker(func() time.Time { return now })

	for range breakerFailures - 1 {
		if !b.Allow() {
			t.Fatal("closed breaker refused a call")
		}
		b.Failure()
	}
	if !b.Allow() {
		t.Fatal("breaker opened before the fifth failure")
	}
	b.Failure()
	if b.State() != breakerOpen {
		t.Fatal("fifth failure did not open the breaker")
	}
	if b.Allow() {
		t.Fatal("breaker stayed closed after five failures")
	}

	now = now.Add(breakerCooldown)
	if !b.Allow() {
		t.Fatal("half-open probe was refused")
	}
	if b.State() != breakerHalf {
		t.Fatal("probe did not leave the breaker half-open")
	}
	if b.Allow() {
		t.Fatal("a second call was admitted while the probe was in flight")
	}
	b.Failure()
	if b.Allow() {
		t.Fatal("a failed probe left the breaker closed")
	}

	now = now.Add(breakerCooldown)
	if !b.Allow() {
		t.Fatal("probe after the second cooldown was refused")
	}
	b.Success()
	if b.State() != breakerClosed {
		t.Fatal("success did not close the breaker")
	}
	if !b.Allow() {
		t.Fatal("success did not close the breaker")
	}
}
