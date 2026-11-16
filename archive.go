package main

import (
	"database/sql"
	"errors"
	"sync"
	"time"
)

// The paid and unpaid counters stopped carrying information when USDC payouts
// ended and the unpaid part was zeroed. The rows after that are all zeros, and
// pages of them bury the row where it happened. The archive the page shows ends
// one row after the drop, so the transition is visible and nothing after it is.
// The rows stay in the database, and the poller still writes them.

var archiveCut struct {
	sync.Mutex
	value   string
	found   bool
	checked time.Time
}

// archiveEnd returns the created_at the archive ends at, or "" when the history
// holds no drop of a tenth or more of the counter. A found cut is kept. A missing one is
// looked for again after ten minutes.
func archiveEnd(db *sql.DB) string {
	archiveCut.Lock()
	defer archiveCut.Unlock()
	if archiveCut.found {
		return archiveCut.value
	}
	if !archiveCut.checked.IsZero() && time.Since(archiveCut.checked) < 10*time.Minute {
		return ""
	}
	archiveCut.checked = time.Now()
	var dropAt string
	err := db.QueryRow(`SELECT created_at FROM (
			SELECT created_at,
			       paid_bytes + unpaid_bytes AS total,
			       LAG(paid_bytes + unpaid_bytes) OVER (ORDER BY created_at) AS prev_total
			FROM wallet_stats)
		WHERE prev_total IS NOT NULL AND total < prev_total * 0.9 AND prev_total - total >= 1000000000
		ORDER BY created_at DESC LIMIT 1`).Scan(&dropAt)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return ""
	}
	var next string
	if db.QueryRow(`SELECT created_at FROM wallet_stats WHERE created_at > ? ORDER BY created_at ASC LIMIT 1`, dropAt).Scan(&next) != nil {
		// the drop is the newest row. Show up to it, and look again next time:
		// the row after it has not been written yet, and caching this would
		// hide it for good.
		archiveCut.checked = time.Time{}
		return dropAt
	}
	archiveCut.value, archiveCut.found = next, true
	return next
}

// resetArchiveEnd forgets the cut, for tests that open a fresh database.
func resetArchiveEnd() {
	archiveCut.Lock()
	archiveCut.value, archiveCut.found, archiveCut.checked = "", false, time.Time{}
	archiveCut.Unlock()
}
