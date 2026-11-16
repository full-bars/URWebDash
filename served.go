package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// Served bytes.
//
// GET /account/epochs reports the bytes the network served that the weekly
// operator-block points are shared by:
//
//	epochs[].served_byte_count   one figure per finalized epoch
//	current_epoch                the open epoch so far
//	current_block                the open Sunday-to-Sunday block so far
//
// The running figures follow live traffic only at the server's rollup cadence
// and sit two to three hours behind it. Each carries as_of_millis, the time the
// traffic is counted through. That makes every sample a point on the server's
// clock instead of ours, so the bytes between two distinct as-of times are a
// real interval with a known span. block_samples keeps one row per as-of time
// and the intervals are derived on read.
//
// Nothing here touches wallet_stats.

// servedEpochWire is current_epoch as the server sends it.
type servedEpochWire struct {
	Epoch           int64 `json:"epoch"`
	StartMillis     int64 `json:"start_millis"`
	EndMillis       int64 `json:"end_millis"`
	ServedByteCount int64 `json:"served_byte_count"`
	AsOfMillis      int64 `json:"as_of_millis"`
}

// servedBlockWire is current_block as the server sends it.
type servedBlockWire struct {
	BlockNumber     int64 `json:"block_number"`
	StartMillis     int64 `json:"start_millis"`
	EndMillis       int64 `json:"end_millis"`
	ServedByteCount int64 `json:"served_byte_count"`
	AsOfMillis      int64 `json:"as_of_millis"`
	Epoch           int64 `json:"epoch"`
}

// servedSample is one stored reading of the open block (and, when the server
// sent it, the open epoch) at an as-of time.
type servedSample struct {
	AsOfMillis       int64  `json:"asOfMillis"`
	BlockNumber      int64  `json:"blockNumber"`
	BlockStartMillis int64  `json:"blockStartMillis"`
	BlockEndMillis   int64  `json:"blockEndMillis"`
	BlockEpoch       int64  `json:"blockEpoch"`
	BlockBytes       int64  `json:"blockBytes"`
	Epoch            *int64 `json:"epoch"`
	EpochBytes       *int64 `json:"epochBytes"`
	EpochStartMillis *int64 `json:"epochStartMillis"`
	EpochEndMillis   *int64 `json:"epochEndMillis"`
}

// Interval kinds.
const (
	servedKindInterval   = "interval"   // bytes between two samples of one block
	servedKindRollover   = "rollover"   // the block changed, so the bytes in between are unknown
	servedKindCorrection = "correction" // the figure went backwards: the server revised it
)

// servedInterval is the traffic between two consecutive as-of times.
type servedInterval struct {
	FromMillis  int64   `json:"fromMillis"`
	ToMillis    int64   `json:"toMillis"`
	BlockNumber int64   `json:"blockNumber"`
	Kind        string  `json:"kind"`
	Bytes       *int64  `json:"bytes"`       // nil unless Kind is "interval"
	BytesPerSec float64 `json:"bytesPerSec"` // 0 unless Kind is "interval"
}

func ensureServedSchema(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS block_samples (
		as_of_millis INTEGER PRIMARY KEY,
		block_number INTEGER NOT NULL,
		block_start_millis INTEGER NOT NULL DEFAULT 0,
		block_end_millis INTEGER NOT NULL DEFAULT 0,
		block_epoch INTEGER NOT NULL DEFAULT 0,
		block_bytes INTEGER NOT NULL,
		epoch INTEGER,
		epoch_bytes INTEGER,
		fetched_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("served schema: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS served_anchor (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		block_start_millis INTEGER NOT NULL,
		legacy_bytes INTEGER NOT NULL,
		method TEXT NOT NULL DEFAULT 'drop',
		computed_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("served schema: %w", err)
	}
	if err := addColumnIfMissing(db, "served_anchor", "method", "TEXT NOT NULL DEFAULT 'drop'"); err != nil {
		return err
	}
	for _, c := range []string{"epoch_start_millis", "epoch_end_millis"} {
		if err := addColumnIfMissing(db, "block_samples", c, "INTEGER"); err != nil {
			return err
		}
	}
	return addColumnIfMissing(db, "epoch_stats", "served_bytes", "INTEGER")
}

// plausible rejects a reading no real server sends. A bad one must not become
// the baseline every later interval is measured from.
func (b servedBlockWire) plausible() error {
	switch {
	case b.AsOfMillis <= 0:
		return fmt.Errorf("current_block: no as_of_millis")
	case b.BlockNumber <= 0:
		return fmt.Errorf("current_block: no block_number")
	case b.ServedByteCount < 0:
		return fmt.Errorf("current_block: negative served_byte_count %d", b.ServedByteCount)
	}
	return nil
}

// storeServedSample records a reading. It reports whether the row is new, so
// only the writer that inserted it goes on to check it for a spike, the same
// rule wallet_stats follows when a manual refresh races the poller.
func storeServedSample(db *sql.DB, blk servedBlockWire, cur *servedEpochWire, now time.Time) (bool, error) {
	if err := blk.plausible(); err != nil {
		return false, err
	}
	var epoch, epochBytes, epochStart, epochEnd interface{}
	if cur != nil && cur.ServedByteCount >= 0 && cur.Epoch > 0 {
		epoch, epochBytes = cur.Epoch, cur.ServedByteCount
		if cur.StartMillis > 0 && cur.EndMillis > cur.StartMillis {
			epochStart, epochEnd = cur.StartMillis, cur.EndMillis
		}
	}
	res, err := db.Exec(`INSERT INTO block_samples
		(as_of_millis, block_number, block_start_millis, block_end_millis, block_epoch,
		 block_bytes, epoch, epoch_bytes, epoch_start_millis, epoch_end_millis, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(as_of_millis) DO NOTHING`,
		blk.AsOfMillis, blk.BlockNumber, blk.StartMillis, blk.EndMillis, blk.Epoch,
		blk.ServedByteCount, epoch, epochBytes, epochStart, epochEnd, now.UTC().Format(time.RFC3339))
	if err != nil {
		return false, fmt.Errorf("store block sample: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// storeEpochServed records the per-epoch served bytes of finalized epochs.
func storeEpochServed(db *sql.DB, acc epochAPIAccount) error {
	for _, e := range acc.Epochs {
		if e.ServedByteCount == nil || *e.ServedByteCount < 0 {
			continue
		}
		if _, err := db.Exec(`UPDATE epoch_stats SET served_bytes = ? WHERE epoch = ?`,
			*e.ServedByteCount, e.Epoch); err != nil {
			return fmt.Errorf("store epoch %d served bytes: %w", e.Epoch, err)
		}
	}
	return nil
}

// ingestServed stores everything the served-byte fields of one response carry.
// The epoch rows must already be stored, so their served figure has a row to
// land on. A server that predates the fields sends none and nothing happens.
func ingestServed(db *sql.DB, acc epochAPIAccount, now time.Time) error {
	if err := storeEpochServed(db, acc); err != nil {
		return err
	}
	if acc.CurrentBlock == nil {
		return nil
	}
	fresh, err := storeServedSample(db, *acc.CurrentBlock, acc.CurrentEpoch, now)
	if err != nil {
		return err
	}
	if fresh {
		fmt.Printf("[served] block %d: %d bytes counted through %s\n",
			acc.CurrentBlock.BlockNumber, acc.CurrentBlock.ServedByteCount,
			time.UnixMilli(acc.CurrentBlock.AsOfMillis).UTC().Format("2006-01-02 15:04"))
		checkServedSpike(db, acc.CurrentBlock.AsOfMillis, now)
	}
	return nil
}

func scanServedSamples(rows *sql.Rows) ([]servedSample, error) {
	defer rows.Close()
	var out []servedSample
	for rows.Next() {
		var s servedSample
		var epoch, epochBytes, epochStart, epochEnd sql.NullInt64
		if err := rows.Scan(&s.AsOfMillis, &s.BlockNumber, &s.BlockStartMillis, &s.BlockEndMillis,
			&s.BlockEpoch, &s.BlockBytes, &epoch, &epochBytes, &epochStart, &epochEnd); err != nil {
			return nil, err
		}
		if epochStart.Valid {
			v := epochStart.Int64
			s.EpochStartMillis = &v
		}
		if epochEnd.Valid {
			v := epochEnd.Int64
			s.EpochEndMillis = &v
		}
		if epoch.Valid {
			v := epoch.Int64
			s.Epoch = &v
		}
		if epochBytes.Valid {
			v := epochBytes.Int64
			s.EpochBytes = &v
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

const servedSampleColumns = `as_of_millis, block_number, block_start_millis, block_end_millis,
	block_epoch, block_bytes, epoch, epoch_bytes, epoch_start_millis, epoch_end_millis`

// readServedSamples returns the newest limit samples, oldest first.
func readServedSamples(db *sql.DB, limit int) ([]servedSample, error) {
	if limit <= 0 {
		limit = 1
	}
	rows, err := db.Query(`SELECT `+servedSampleColumns+` FROM (
		SELECT * FROM block_samples ORDER BY as_of_millis DESC LIMIT ?) ORDER BY as_of_millis ASC`, limit)
	if err != nil {
		return nil, err
	}
	return scanServedSamples(rows)
}

// servedIntervals turns consecutive samples into intervals. Two samples of one
// block give the bytes served between their as-of times. A change of block
// leaves a gap the dashboard cannot measure, and a figure that went backwards
// is a revision by the server, so neither is shown as traffic.
func servedIntervals(samples []servedSample) []servedInterval {
	out := make([]servedInterval, 0, len(samples))
	for i := 1; i < len(samples); i++ {
		prev, cur := samples[i-1], samples[i]
		iv := servedInterval{FromMillis: prev.AsOfMillis, ToMillis: cur.AsOfMillis, BlockNumber: cur.BlockNumber}
		switch {
		case cur.BlockNumber != prev.BlockNumber:
			iv.Kind = servedKindRollover
		case cur.BlockBytes < prev.BlockBytes:
			iv.Kind = servedKindCorrection
		default:
			iv.Kind = servedKindInterval
			b := cur.BlockBytes - prev.BlockBytes
			iv.Bytes = &b
			if span := time.Duration(cur.AsOfMillis-prev.AsOfMillis) * time.Millisecond; span > 0 {
				iv.BytesPerSec = float64(b) / span.Seconds()
			}
		}
		out = append(out, iv)
	}
	return out
}

// servedWindow is the traffic between an earlier sample and the newest one.
type servedWindow struct {
	Bytes      int64 `json:"bytes"`
	FromMillis int64 `json:"fromMillis"`
	ToMillis   int64 `json:"toMillis"`
}

// servedPayload is what the page and /api/served receive.
type servedPayload struct {
	Block     *servedSample    `json:"block"`     // newest sample, nil before the first
	Intervals []servedInterval `json:"intervals"` // newest last
	Samples   int              `json:"samples"`
	Last24h   *servedWindow    `json:"last24h"` // nil until a sample from a day ago in the same block exists
	Total     *servedTotal     `json:"total"`   // everything the server has counted so far
	Weeks     []servedWeek     `json:"weeks"`   // one entry per week, oldest first
}

// servedWeek is the bytes served in one Sunday-to-Sunday week.
type servedWeek struct {
	StartMillis int64 `json:"startMillis"`
	Bytes       int64 `json:"bytes"`
	Partial     bool  `json:"partial"` // the week is still running
}

func servedSunday(t time.Time) time.Time {
	d := t.UTC().Truncate(24 * time.Hour)
	return d.AddDate(0, 0, -int(d.Weekday()))
}

// legacyTotalAt reads the old paid plus unpaid counter at a moment. A reading
// much older than the moment (six hours, or two poll windows if that is longer)
// is a gap in the history and gives no figure, so a week next to a gap is left
// out instead of guessed.
func legacyTotalAt(db *sql.DB, at time.Time) (int64, bool) {
	var created string
	var total int64
	err := db.QueryRow(`SELECT created_at, paid_bytes + unpaid_bytes FROM wallet_stats
		WHERE created_at <= ? ORDER BY created_at DESC LIMIT 1`, at.UTC().Format(time.RFC3339)).Scan(&created, &total)
	if err != nil {
		return 0, false
	}
	when, perr := time.Parse(time.RFC3339, created)
	// the newest reading may be up to one poll window old, so allow two windows
	maxGap := 6 * time.Hour
	if g := 2 * statsInterval(); g > maxGap {
		maxGap = g
	}
	if perr != nil || at.Sub(when) > maxGap {
		return 0, false
	}
	return total, true
}

// servedWeeks lays one bar per week side by side. Weeks the old counter holds
// come from its growth between two Sundays. Later weeks come from the server:
// each finalized epoch is one week, the open epoch's finished week is the week
// before the current one, and the current week is partial.
func servedWeeks(db *sql.DB, newest servedSample) ([]servedWeek, error) {
	byStart := map[int64]servedWeek{}
	anchorPt, hasAnchor, err := servedAnchor(db)
	anchorStart := anchorPt.BlockStart
	if err != nil {
		return nil, err
	}
	var first string
	if err := db.QueryRow(`SELECT MIN(created_at) FROM wallet_stats`).Scan(&first); err == nil && first != "" {
		if t, perr := time.Parse(time.RFC3339, first); perr == nil {
			start := servedSunday(t)
			if start.Before(t.Add(-12 * time.Hour)) {
				start = start.AddDate(0, 0, 7)
			}
			limit := time.UnixMilli(newest.BlockStartMillis)
			if hasAnchor {
				limit = time.UnixMilli(anchorStart)
			}
			for b := start; b.Before(limit); b = b.AddDate(0, 0, 7) {
				from, ok1 := legacyTotalAt(db, b)
				to, ok2 := legacyTotalAt(db, b.AddDate(0, 0, 7))
				if ok1 && ok2 && to >= from {
					byStart[b.UnixMilli()] = servedWeek{StartMillis: b.UnixMilli(), Bytes: to - from}
				}
			}
		}
	}
	minEnd := int64(0)
	if hasAnchor {
		minEnd = anchorStart + int64(servedBlockLength/time.Millisecond)
	}
	openEpoch := int64(-1)
	if newest.Epoch != nil {
		openEpoch = *newest.Epoch
	}
	rows, err := db.Query(`SELECT end_millis, served_bytes FROM epoch_stats
		WHERE finalized = 1 AND served_bytes IS NOT NULL AND epoch <> ? AND end_millis >= ?`, openEpoch, minEnd)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var end, bytes int64
		if err := rows.Scan(&end, &bytes); err != nil {
			rows.Close()
			return nil, err
		}
		start := servedSunday(time.UnixMilli(end)).AddDate(0, 0, -7)
		byStart[start.UnixMilli()] = servedWeek{StartMillis: start.UnixMilli(), Bytes: bytes}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if newest.EpochBytes != nil && *newest.EpochBytes > 0 {
		start := newest.BlockStartMillis - int64(servedBlockLength/time.Millisecond)
		byStart[start] = servedWeek{StartMillis: start, Bytes: *newest.EpochBytes}
	}
	byStart[newest.BlockStartMillis] = servedWeek{StartMillis: newest.BlockStartMillis, Bytes: newest.BlockBytes, Partial: true}
	out := make([]servedWeek, 0, len(byStart))
	for _, w := range byStart {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartMillis < out[j].StartMillis })
	return out, nil
}

// servedTotal is the running total of everything served. The old paid plus
// unpaid counter and the server's weekly figures count the same bytes: for the
// weeks the two overlap they agree to within a fraction of a gigabyte. So the
// total is the old counter at a week boundary, plus every week the server has
// counted since. When the old counter never had its USDC cutover, the total is
// the server's figures alone.
type servedTotal struct {
	Bytes          int64  `json:"bytes"`
	LegacyBytes    int64  `json:"legacyBytes"`    // the old counter at AnchorMillis, 0 without one
	AnchorMillis   int64  `json:"anchorMillis"`   // the week start the old counter is read at
	CountedByEpoch int64  `json:"countedByEpoch"` // the server's weeks since then
	AnchorMethod   string `json:"anchorMethod"`   // "agree" when both counters matched for the week, else "drop"
}

const servedBlockLength = 7 * 24 * time.Hour

// servedAnchorPoint is where the old counter is joined to the server's weeks.
type servedAnchorPoint struct {
	BlockStart  int64  // a Sunday 00:00 UTC, in milliseconds
	LegacyBytes int64  // the old counter at that moment
	Method      string // "agree" or "drop"
}

const (
	anchorAgree = "agree"
	anchorDrop  = "drop"
)

// servedAnchor finds the week boundary the old counter is joined to, and keeps
// it. The old paid plus unpaid counter and the server's weekly figures count the
// same bytes, so the best join is the latest week where the two agree: the
// counter's growth across the week matches that week's served_byte_count. A
// week the old counter cannot be read for (a gap, or after its USDC cutover)
// never agrees, so the join falls back to an older agreeing week and the
// server's later weeks fill the rest. When no week agrees, the last Sunday
// before the old counter dropped by a tenth or more is used, and that is
// labelled as the weaker rule. With neither, there is no join.
func servedAnchor(db *sql.DB) (servedAnchorPoint, bool, error) {
	var p servedAnchorPoint
	err := db.QueryRow(`SELECT block_start_millis, legacy_bytes, method FROM served_anchor WHERE id = 1`).Scan(&p.BlockStart, &p.LegacyBytes, &p.Method)
	if err == nil && p.Method == anchorAgree {
		return p, true, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, false, err
	}
	stored := err == nil
	if found, ok, err := anchorByAgreement(db); err != nil {
		return p, false, err
	} else if ok {
		return storeAnchor(db, found, true)
	}
	if stored {
		return p, true, nil
	}
	found, ok, err := anchorByDrop(db)
	if err != nil || !ok {
		return p, false, err
	}
	return storeAnchor(db, found, false)
}

func storeAnchor(db *sql.DB, p servedAnchorPoint, replace bool) (servedAnchorPoint, bool, error) {
	verb := "INSERT OR IGNORE"
	if replace {
		verb = "INSERT OR REPLACE"
	}
	_, err := db.Exec(verb+` INTO served_anchor (id, block_start_millis, legacy_bytes, method, computed_at)
		VALUES (1, ?, ?, ?, ?)`, p.BlockStart, p.LegacyBytes, p.Method, time.Now().UTC().Format(time.RFC3339))
	return p, err == nil, err
}

// anchorByAgreement compares, week by week, the old counter's growth with the
// server's figure for the same week, newest week first.
func anchorByAgreement(db *sql.DB) (servedAnchorPoint, bool, error) {
	rows, err := db.Query(`SELECT end_millis, served_bytes FROM epoch_stats
		WHERE finalized = 1 AND served_bytes IS NOT NULL ORDER BY end_millis DESC`)
	if err != nil {
		return servedAnchorPoint{}, false, err
	}
	type wk struct{ end, bytes int64 }
	var weeks []wk
	for rows.Next() {
		var w wk
		if err := rows.Scan(&w.end, &w.bytes); err != nil {
			rows.Close()
			return servedAnchorPoint{}, false, err
		}
		weeks = append(weeks, w)
	}
	if err := rows.Close(); err != nil {
		return servedAnchorPoint{}, false, err
	}
	for _, w := range weeks {
		if w.bytes < 100_000_000 {
			continue // a week with almost nothing in it proves nothing
		}
		blockEnd := servedSunday(time.UnixMilli(w.end))
		blockStart := blockEnd.AddDate(0, 0, -7)
		from, ok1 := legacyTotalAt(db, blockStart)
		to, ok2 := legacyTotalAt(db, blockEnd)
		if !ok1 || !ok2 {
			continue
		}
		diff := (to - from) - w.bytes
		if diff < 0 {
			diff = -diff
		}
		tolerance := w.bytes / 100 // one percent, with a floor for tiny weeks
		if tolerance < 50_000_000 {
			tolerance = 50_000_000
		}
		if diff <= tolerance {
			return servedAnchorPoint{BlockStart: blockEnd.UnixMilli(), LegacyBytes: to, Method: anchorAgree}, true, nil
		}
	}
	return servedAnchorPoint{}, false, nil
}

// anchorByDrop is the weaker rule: the last Sunday before the old counter fell
// by a tenth or more (and at least a gigabyte), which a lifetime total never
// does on its own. The size is relative so it fits a small account as well as a
// large one.
func anchorByDrop(db *sql.DB) (servedAnchorPoint, bool, error) {
	var lastGood string
	err := db.QueryRow(`SELECT prev_at FROM (
			SELECT created_at,
			       paid_bytes + unpaid_bytes AS total,
			       LAG(created_at) OVER (ORDER BY created_at) AS prev_at,
			       LAG(paid_bytes + unpaid_bytes) OVER (ORDER BY created_at) AS prev_total
			FROM wallet_stats)
		WHERE prev_total IS NOT NULL AND total * 10 <= prev_total * 9 AND prev_total - total >= 1000000000
		ORDER BY created_at DESC LIMIT 1`).Scan(&lastGood)
	if errors.Is(err, sql.ErrNoRows) {
		return servedAnchorPoint{}, false, nil
	}
	if err != nil {
		return servedAnchorPoint{}, false, err
	}
	t, perr := time.Parse(time.RFC3339, lastGood)
	if perr != nil {
		return servedAnchorPoint{}, false, perr
	}
	start := servedSunday(t)
	total, ok := legacyTotalAt(db, start)
	if !ok {
		return servedAnchorPoint{}, false, nil
	}
	return servedAnchorPoint{BlockStart: start.UnixMilli(), LegacyBytes: total, Method: anchorDrop}, true, nil
}

func servedTotalSoFar(db *sql.DB, newest servedSample) (*servedTotal, error) {
	anchorPt, hasAnchor, err := servedAnchor(db)
	anchorStart, legacy := anchorPt.BlockStart, anchorPt.LegacyBytes
	if err != nil {
		return nil, err
	}
	/* the open epoch is not a finalized row, but exclude it anyway so a row
	   stored late can never be added next to its own running figure */
	openEpoch := int64(-1)
	if newest.Epoch != nil {
		openEpoch = *newest.Epoch
	}
	/* a week the old counter already holds ends before the anchor plus one
	   week, so only epochs whose window ends later are added to it */
	minEnd := int64(0)
	if hasAnchor {
		minEnd = anchorStart + int64(servedBlockLength/time.Millisecond)
	}
	/* a week can arrive twice here: a response that carries the epoch
	   figures but no current_block leaves the newest sample on an older
	   week, and once that week finalizes its figure is added next to the
	   sample's partial bytes. Only add an epoch whose block ended at or
	   before the start of the newest sample's block. The check reads the
	   block boundary, not the epoch's own window: a finalized epoch normally
	   ends after the current block began, and that is fine. */
	rows, err := db.Query(`SELECT end_millis, served_bytes FROM epoch_stats
		WHERE finalized = 1 AND served_bytes IS NOT NULL AND epoch <> ? AND end_millis >= ?`,
		openEpoch, minEnd)
	if err != nil {
		return nil, err
	}
	var counted int64
	for rows.Next() {
		var end, bytes int64
		if err := rows.Scan(&end, &bytes); err != nil {
			rows.Close()
			return nil, err
		}
		if newest.BlockStartMillis > 0 &&
			servedSunday(time.UnixMilli(end)).UnixMilli() > newest.BlockStartMillis {
			continue
		}
		counted += bytes
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// The anchor already holds completed blocks before it. The open epoch's
	// figure holds finished weeks before the current block, not that block.
	if !hasAnchor || newest.BlockStartMillis <= 0 || newest.BlockStartMillis >= anchorStart {
		counted += newest.BlockBytes
	}
	if newest.EpochBytes != nil && (!hasAnchor || newest.BlockStartMillis <= 0 || newest.BlockStartMillis > anchorStart) {
		counted += *newest.EpochBytes
	}
	t := &servedTotal{Bytes: legacy + counted, CountedByEpoch: counted}
	if hasAnchor {
		t.LegacyBytes, t.AnchorMillis, t.AnchorMethod = legacy, anchorStart, anchorPt.Method
	}
	return t, nil
}

// servedLast24h measures the newest sample against the latest one at least 24
// hours before it. Both must be in the same block: a block change leaves bytes
// nobody measured, so it gives no figure.
func servedLast24h(db *sql.DB, newest servedSample) (*servedWindow, error) {
	cutoff := newest.AsOfMillis - int64(24*time.Hour/time.Millisecond)
	rows, err := db.Query(`SELECT `+servedSampleColumns+` FROM block_samples
		WHERE as_of_millis <= ? ORDER BY as_of_millis DESC LIMIT 1`, cutoff)
	if err != nil {
		return nil, err
	}
	older, err := scanServedSamples(rows)
	if err != nil || len(older) == 0 {
		return nil, err
	}
	o := older[0]
	if o.BlockNumber != newest.BlockNumber || newest.BlockBytes < o.BlockBytes {
		return nil, nil
	}
	/* a baseline much older than a day would be shown as "last 24 hours" and
	   count several days. Allow a few poll windows of slack and no more. */
	slack := 3 * time.Hour
	if s := 3 * statsInterval(); s > slack {
		slack = s
	}
	if time.Duration(cutoff-o.AsOfMillis)*time.Millisecond > slack {
		return nil, nil
	}
	return &servedWindow{Bytes: newest.BlockBytes - o.BlockBytes, FromMillis: o.AsOfMillis, ToMillis: newest.AsOfMillis}, nil
}

// servedHistoryDays is how far back the page's intervals reach. It is a span
// of time, not a count, so a shorter STATS_INTERVAL does not shorten it. The
// cap only bounds the payload for a very short interval.
const (
	servedHistoryDays = 9
	servedSamplesCap  = 5000
)

// readServedSamplesSince returns the samples of the last days before the newest
// one, oldest first, at most limit of them (the newest ones when over).
func readServedSamplesSince(db *sql.DB, days, limit int) ([]servedSample, error) {
	rows, err := db.Query(`SELECT `+servedSampleColumns+` FROM (
		SELECT * FROM block_samples
		WHERE as_of_millis >= (SELECT COALESCE(MAX(as_of_millis), 0) FROM block_samples) - ?
		ORDER BY as_of_millis DESC LIMIT ?) ORDER BY as_of_millis ASC`,
		int64(days)*int64(24*time.Hour/time.Millisecond), limit)
	if err != nil {
		return nil, err
	}
	return scanServedSamples(rows)
}

func buildServedPayload(db *sql.DB) (servedPayload, error) {
	p := servedPayload{Intervals: []servedInterval{}, Weeks: []servedWeek{}}
	samples, err := readServedSamplesSince(db, servedHistoryDays, servedSamplesCap)
	if err != nil {
		return p, err
	}
	p.Samples = len(samples)
	if len(samples) > 0 {
		last := samples[len(samples)-1]
		p.Block = &last
		if w, err := servedLast24h(db, last); err != nil {
			return p, err
		} else {
			p.Last24h = w
		}
		if t, err := servedTotalSoFar(db, last); err != nil {
			return p, err
		} else {
			p.Total = t
		}
		if w, err := servedWeeks(db, last); err != nil {
			return p, err
		} else {
			p.Weeks = w
		}
	}
	p.Intervals = servedIntervals(samples)
	return p, nil
}

func handleServed(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := buildServedPayload(db)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(p)
	}
}

// servedSpikeWindow is the span the alert threshold is quoted for: one poll
// window, which is STATS_INTERVAL. An interval of another length, such as two
// windows after a missed poll, is compared at its one-window equivalent.
func servedSpikeWindow() time.Duration { return statsInterval() }

// windowWords says the poll window in words for the page, such as "hour" or
// "15 minutes".
func windowWords(d time.Duration) string {
	switch {
	case d == time.Hour:
		return "hour"
	case d%time.Hour == 0:
		return fmt.Sprintf("%d hours", int(d/time.Hour))
	case d%time.Minute == 0:
		return fmt.Sprintf("%d minutes", int(d/time.Minute))
	}
	return d.String()
}

// checkServedSpike alerts when the traffic between the sample at asOf and the
// one before it, scaled to one poll window, passes the spike threshold. It
// is delayed by the server's rollup, and the message says so.
func checkServedSpike(db *sql.DB, asOf int64, now time.Time) {
	rows, err := db.Query(`SELECT `+servedSampleColumns+` FROM block_samples
		WHERE as_of_millis <= ? ORDER BY as_of_millis DESC LIMIT 2`, asOf)
	if err != nil {
		return
	}
	pair, err := scanServedSamples(rows)
	if err != nil || len(pair) < 2 {
		return
	}
	iv := servedIntervals([]servedSample{pair[1], pair[0]})
	if len(iv) != 1 || iv[0].Kind != servedKindInterval || iv[0].Bytes == nil {
		return
	}
	span := time.Duration(iv[0].ToMillis-iv[0].FromMillis) * time.Millisecond
	/* the guard has to scale with the poll window: a longer STATS_INTERVAL
	   puts consecutive samples further apart, so a fixed ceiling would skip
	   every live interval at once. A gap past three poll windows is a
	   backfill lump, not a live burst. */
	liveSpan := backfillSkipSpan()
	if w := 3 * statsInterval(); w > liveSpan {
		liveSpan = w
	}
	if span <= 0 || span > liveSpan {
		return
	}
	scaled := float64(*iv[0].Bytes) * float64(servedSpikeWindow()) / float64(span)
	if scaled <= float64(spikeThreshold()) {
		return
	}
	body := formatServedSpikeAlert(webhookStyle(), span, *iv[0].Bytes, pair[0].BlockBytes,
		time.UnixMilli(iv[0].ToMillis).UTC(), now)
	sendDiscordNotificationJSON(body)
}

// formatServedSpikeAlert writes the alert in the chosen style. The time is the
// as-of time, since that is when the server counted the traffic, shown in the
// zone the page reported.
func formatServedSpikeAlert(style string, span time.Duration, deltaBytes, blockBytes int64, asOf, now time.Time) string {
	return formatSpikeAlert(style, span, float64(deltaBytes)/1e9, float64(blockBytes)/1e9, asOf)
}

// pollServed fetches the served-byte figures on their own. It asks for two
// epochs only, since the running figures do not depend on the limit.
func pollServed(db *sql.DB, token string) error {
	epochPollMu.Lock()
	defer epochPollMu.Unlock()
	acc, err := fetchAccountEpochs(token, 2)
	if err != nil {
		return err
	}
	if acc.CurrentBlock == nil && len(acc.Epochs) > 0 && acc.Epochs[0].ServedByteCount == nil {
		return errors.New("/account/epochs carries no served-byte figures yet")
	}
	return ingestServed(db, acc, time.Now())
}

// alertSample is what the Webhook tab's examples are filled with: the newest
// measured interval, so an example looks like the alert you would get.
type alertSample struct {
	OK      bool    `json:"ok"`
	DeltaGB float64 `json:"deltaGB"`
	TotalGB float64 `json:"totalGB"`
	Span    string  `json:"span"`
	Clock   string  `json:"clock"`
	Unix    int64   `json:"unix"`
	ISO     string  `json:"iso"`
	// Bodies is the alert exactly as it would be sent, for every style. With no
	// samples yet it carries example figures.
	Bodies map[string]string `json:"bodies"`
}

var alertStyles = []string{"box-table", "ascii-table", "plain-lines", "embed", "quote", "story", "bullets", "local-clock", "hero-card", "ansi"}

func withBodies(a alertSample, at time.Time) alertSample {
	span := parseSpanLabel(a.Span)
	a.Bodies = map[string]string{}
	for _, st := range alertStyles {
		a.Bodies[st] = formatSpikeAlert(st, span, a.DeltaGB, a.TotalGB, at)
	}
	return a
}

func buildAlertSample(db *sql.DB) (alertSample, error) {
	samples, err := readServedSamples(db, 8)
	if err != nil {
		return alertSample{}, err
	}
	ivs := servedIntervals(samples)
	for i := len(ivs) - 1; i >= 0; i-- {
		iv := ivs[i]
		if iv.Kind != servedKindInterval || iv.Bytes == nil {
			continue
		}
		at := time.UnixMilli(iv.ToMillis)
		var total int64
		for _, s := range samples {
			if s.AsOfMillis == iv.ToMillis {
				total = s.BlockBytes
			}
		}
		return withBodies(alertSample{
			OK: true, DeltaGB: float64(*iv.Bytes) / 1e9, TotalGB: float64(total) / 1e9,
			Span:  spanLabel(time.Duration(iv.ToMillis-iv.FromMillis) * time.Millisecond),
			Clock: alertClock(at), Unix: at.Unix(), ISO: at.UTC().Format("2006-01-02T15:04:05.000Z"),
		}, at), nil
	}
	now := time.Now()
	// No measured interval yet: example figures over the real poll window
	return withBodies(alertSample{DeltaGB: 1.45, TotalGB: 742.29, Span: spanLabel(statsInterval()), Clock: alertClock(now), Unix: now.Unix(),
		ISO: now.UTC().Format("2006-01-02T15:04:05.000Z")}, now), nil
}

func handleAlertSample(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, err := buildAlertSample(db)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(a)
	}
}
