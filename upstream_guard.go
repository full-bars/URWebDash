package main

// Upstream failure window.
//
// The dashboard is rendered on the request goroutine, so a chain or coordinator
// lookup that is slow or dead is a page that hangs. Each lookup has a 20s
// timeout and the caches only remember successes, which means a dead upstream
// costs every visitor a full timeout, over and over.
//
// This remembers failures for a short window instead. The first request after a
// failure pays the timeout; the rest of the window is served from cache without
// touching the network. The window is short on purpose: the page should recover
// on its own within a minute of the upstream coming back.

import (
	"sync"
	"time"
)

const upstreamFailureWindow = 60 * time.Second

var upstreamGuard struct {
	mu      sync.Mutex
	retryAt map[string]time.Time
}

// upstreamBlocked reports whether this upstream failed recently enough that the
// request path should not try it again yet.
func upstreamBlocked(name string) bool {
	upstreamGuard.mu.Lock()
	defer upstreamGuard.mu.Unlock()
	if upstreamGuard.retryAt == nil {
		return false
	}
	at, ok := upstreamGuard.retryAt[name]
	if !ok {
		return false
	}
	if time.Now().Before(at) {
		return true
	}
	delete(upstreamGuard.retryAt, name)
	return false
}

// upstreamFailed records a failure so the next window is served without a retry.
func upstreamFailed(name string) {
	upstreamGuard.mu.Lock()
	defer upstreamGuard.mu.Unlock()
	if upstreamGuard.retryAt == nil {
		upstreamGuard.retryAt = make(map[string]time.Time)
	}
	upstreamGuard.retryAt[name] = time.Now().Add(upstreamFailureWindow)
}

// resetUpstreamGuard clears the window; used by tests.
func resetUpstreamGuard() {
	upstreamGuard.mu.Lock()
	defer upstreamGuard.mu.Unlock()
	upstreamGuard.retryAt = nil
}
