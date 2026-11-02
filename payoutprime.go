package main

// The page is rendered on the server and makes no client fetch, so nothing
// fills the payout cache on an ordinary visit: the old page used to call
// /api/payout-stats from the browser. These helpers prime it at serve startup,
// keep it warm, and let a render ask for a refresh without waiting for it.
//
// The cache itself stays guarded by payoutCacheMu; readers copy the slice under
// the lock, exactly as the API handlers do.

import (
	"fmt"
	"sync"
	"time"
)

var (
	payoutPrimeMu       sync.Mutex
	payoutPrimeInFlight bool
	payoutPrimeLast     time.Time
)

// minPayoutPrimeGap stops a burst of page loads from firing a fetch each.
const minPayoutPrimeGap = 30 * time.Second

// primePayoutCache refreshes the payout cache once. It reports whether a
// refresh actually ran, so callers can tell "already fresh" from "failed".
func primePayoutCache(token string) (bool, error) {
	payoutPrimeMu.Lock()
	if payoutPrimeInFlight {
		payoutPrimeMu.Unlock()
		return false, nil
	}
	if time.Since(payoutPrimeLast) < minPayoutPrimeGap {
		payoutPrimeMu.Unlock()
		return false, nil
	}
	payoutPrimeInFlight = true
	payoutPrimeLast = time.Now()
	payoutPrimeMu.Unlock()

	defer func() {
		payoutPrimeMu.Lock()
		payoutPrimeInFlight = false
		payoutPrimeMu.Unlock()
	}()

	resp, err := fetchPayouts(token)
	if err != nil {
		payoutCacheMu.Lock()
		payoutLastError = err.Error()
		payoutCacheMu.Unlock()
		return true, err
	}
	if resp.Error != nil {
		payoutCacheMu.Lock()
		payoutLastError = resp.Error.Message
		payoutCacheMu.Unlock()
		return true, fmt.Errorf("%s", resp.Error.Message)
	}

	/* points and reliability are a separate endpoint; the payout table is still
	   worth showing without them */
	pointsByPayment := map[string]float64{}
	reliabilityByPayment := map[string]float64{}
	var totalPoints float64
	if pointEntries, perr := fetchPoints(token); perr == nil {
		for _, pe := range pointEntries {
			pointsByPayment[pe.AccountPaymentID] += float64(pe.PointValue) / 1e6
			totalPoints += float64(pe.PointValue) / 1e6
			if pe.Event == "payout_reliability" {
				reliabilityByPayment[pe.AccountPaymentID] += float64(pe.PointValue) / 1e6
			}
		}
	}
	for i := range resp.AccountPayments {
		p := &resp.AccountPayments[i]
		p.PointsEarned = pointsByPayment[p.PaymentID]
		p.ReliabilityPts = reliabilityByPayment[p.PaymentID]
	}

	payoutCacheMu.Lock()
	payoutCache = resp.AccountPayments
	payoutCacheTime = time.Now()
	payoutLastError = ""
	payoutLastUpdate = time.Now().UTC().Format(time.RFC3339)
	payoutLastPoints = totalPoints
	payoutCacheMu.Unlock()

	syncPayoutNotifyStore(resp.AccountPayments, true)
	return true, nil
}

// startPayoutPrimer keeps the cache warm for as long as the server runs. The
// first refresh happens immediately so the first visitor sees data.
func startPayoutPrimer(token string) {
	if token == "" {
		return
	}
	go func() {
		if _, err := primePayoutCache(token); err != nil {
			fmt.Printf("[payout] prime failed: %v\n", err)
		}
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			if _, err := primePayoutCache(token); err != nil {
				fmt.Printf("[payout] prime failed: %v\n", err)
			}
		}
	}()
}

// primePayoutCacheAsync asks for a refresh without blocking a render: a cold
// page load starts the fetch and shows the section empty this time.
func primePayoutCacheAsync(token string) {
	if token == "" {
		return
	}
	payoutCacheMu.RLock()
	empty := len(payoutCache) == 0
	payoutCacheMu.RUnlock()
	if !empty {
		return
	}
	go func() {
		if _, err := primePayoutCache(token); err != nil {
			fmt.Printf("[payout] prime failed: %v\n", err)
		}
	}()
}
