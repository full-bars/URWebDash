package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A cold price cache must not be refreshed from the render path: the page would
// wait on the pool and fx timeouts before painting anything.
func TestRenderIndexDoesNotFetchPriceOnColdCache(t *testing.T) {
	priceCache.mu.Lock()
	priceCache.doc = priceDoc{}
	priceCache.at = time.Time{}
	priceCache.attempt = time.Time{}
	priceCache.mu.Unlock()

	t.Setenv("STATS_DB", filepath.Join(t.TempDir(), "stats.db"))
	db, err := openDB()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	start := time.Now()
	page := string(renderIndex(db, ""))
	elapsed := time.Since(start)

	// would fail if buildDashData went back to cachedPrice(), which fetches on a
	// cold cache: the render would block on two upstreams and this test would
	// take seconds instead of milliseconds
	if elapsed > 2*time.Second {
		t.Errorf("render took %s on a cold cache — it is fetching", elapsed)
	}
	priceCache.mu.Lock()
	attempted := !priceCache.attempt.IsZero()
	fail := priceCache.fail
	priceCache.mu.Unlock()
	if attempted {
		t.Errorf("render path touched the price cache for a refresh (fail=%q)", fail)
	}
	if !strings.Contains(page, `"alphaUsd":0`) {
		t.Error("a cold cache should render an empty price section")
	}
}

// The series is downsampled by time bucket, not by clock minute, so a
// non-default poll interval still produces a chart.
func TestSeriesDownsamplesByTimeBucket(t *testing.T) {
	t.Setenv("STATS_DB", filepath.Join(t.TempDir(), "stats.db"))
	db, err := openDB()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	/* an hourly cadence puts every sample on minute 0, which a minute-based
	   filter drops */
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 24; i++ {
		ts := base.Add(time.Duration(i) * time.Hour).Format(time.RFC3339)
		if _, err := db.Exec(`INSERT INTO wallet_stats (paid_bytes, unpaid_bytes, created_at, updated_at)
			VALUES (?, ?, ?, ?)`, int64(i)*1e9, int64(i)*5e8, ts, ts); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	_, h, _ := buildDashData(db, "")
	// 24 hourly samples cover 12 two-hour buckets
	if len(h.Series) < 10 {
		t.Errorf("series has %d points over 24 hours — downsampling dropped it to nothing", len(h.Series))
	}
	if len(h.Series) > 13 {
		t.Errorf("series has %d points, expected about one per 2-hour bucket", len(h.Series))
	}
}

// The payout cache is replaced on the refresh path; the render path reads it.
// Run with -race to prove the copy happens under the lock.
func TestRenderIndexWhilePayoutCacheReplaced(t *testing.T) {
	t.Setenv("STATS_DB", filepath.Join(t.TempDir(), "stats.db"))
	db, err := openDB()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			/* the writer replaces the header, exactly as handleRefreshPayout does */
			payoutCacheMu.Lock()
			payoutCache = []payoutRecord{{PaymentID: "p", TokenAmount: float64(i), Completed: true}}
			payoutCacheMu.Unlock()
		}
	}()

	for i := 0; i < 40; i++ {
		page := string(renderIndex(db, ""))
		if !strings.Contains(page, "data-theme=") {
			t.Fatal("page did not render")
		}
	}
	close(stop)
	wg.Wait()
}

// The epoch estimate anchors to the last COMPLETED epoch. An open epoch row can
// carry a future end_millis, which would pin the estimate at the start block.
func TestEpochEstimateIgnoresOpenEpochRow(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "epochs.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := ensureEpochSchema(db); err != nil {
		t.Fatalf("schema: %v", err)
	}
	now := time.Now()
	/* epoch 4 is the live one with a future end; epoch 3 finished 3 days ago */
	if err := storeEpochs(db, []epochRow{
		{Epoch: 4, Points: 0, StartMillis: now.Add(-24 * time.Hour).UnixMilli(), EndMillis: now.Add(48 * time.Hour).UnixMilli()},
		{Epoch: 3, Points: 10, StartMillis: now.Add(-8 * 24 * time.Hour).UnixMilli(), EndMillis: now.Add(-3 * 24 * time.Hour).UnixMilli()},
	}, now); err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := storeSNEpoch(db, snEpoch{
		Epoch: 4, StartBlock: 1000, CommitBlock: 6000, Finalize: 7000, TEpochBlock: 5040,
	}, now); err != nil {
		t.Fatalf("sn epoch: %v", err)
	}

	d, _, _ := buildDashData(db, "")
	if d.Epoch.Head <= d.Epoch.StartBlock {
		t.Errorf("head estimate pinned at the start block (%d) — the open epoch's future end was used", d.Epoch.Head)
	}
	elapsedDays := float64(d.Epoch.Head-d.Epoch.StartBlock) * blockSeconds / 86400
	if elapsedDays < 2 || elapsedDays > 4 {
		t.Errorf("derived %.1f days elapsed, expected about 3 from the completed epoch", elapsedDays)
	}
}
