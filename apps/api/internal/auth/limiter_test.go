package auth

import (
	"testing"
	"time"
)

func TestLookupLimiterEnforcesClientGlobalAndMemoryBounds(t *testing.T) {
	now := time.Now()
	limiter := newLookupLimiter(2, 3, 2, time.Minute)
	if !limiter.Allow("client-a", now) || !limiter.Allow("client-a", now) {
		t.Fatal("client allowance was rejected early")
	}
	if limiter.Allow("client-a", now) {
		t.Fatal("client limit admitted request 3")
	}
	if !limiter.Allow("client-b", now) {
		t.Fatal("different client was blocked before the global limit")
	}
	if limiter.Allow("client-c", now) {
		t.Fatal("global limit admitted request 4")
	}
	if _, exists := limiter.clients["client-c"]; exists {
		t.Fatal("globally rejected request created client state")
	}

	now = now.Add(time.Minute)
	if !limiter.Allow("client-c", now) {
		t.Fatal("expired windows did not admit a new client")
	}
	if len(limiter.clients) > 2 {
		t.Fatalf("lookup limiter retained %d clients, want at most 2", len(limiter.clients))
	}
}

func TestLookupLimiterFailsClosedWhenClientMapIsFull(t *testing.T) {
	now := time.Now()
	limiter := newLookupLimiter(1, 10, 2, time.Minute)
	if !limiter.Allow("client-a", now) || !limiter.Allow("client-b", now) {
		t.Fatal("lookup limiter rejected clients before reaching capacity")
	}
	if limiter.Allow("client-c", now) {
		t.Fatal("lookup limiter admitted an untracked client at capacity")
	}
}
