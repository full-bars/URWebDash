package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func timeZero() time.Time { return time.Time{} }

func TestUpstreamGuardWindow(t *testing.T) {
	resetUpstreamGuard()
	if upstreamBlocked("x") {
		t.Fatal("a fresh guard must not block")
	}
	upstreamFailed("x")
	if !upstreamBlocked("x") {
		t.Fatal("a recent failure must block the retry")
	}
	resetUpstreamGuard()
	if upstreamBlocked("x") {
		t.Fatal("reset must clear the window")
	}
}

/*
the point of the window: a dead upstream costs ONE timeout, not one per

	request. Two consecutive lookups must produce a single network attempt.
*/
func TestFailedLookupIsNotRetriedWithinTheWindow(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	t.Setenv("BT_RPC_URL", srv.URL)
	resetUpstreamGuard()
	balanceCache.mu.Lock()
	balanceCache.key = ""
	balanceCache.at = timeZero()
	balanceCache.mu.Unlock()

	const ck = "5GbD4Vk6cASzfgkywuGkGHszPSr1s6gx9y9fFBDjLV2q1GWS"
	fetchBalances(ck)
	fetchBalances(ck)

	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("upstream hit %d times, want 1 within the failure window", got)
	}
	resetUpstreamGuard()
}
