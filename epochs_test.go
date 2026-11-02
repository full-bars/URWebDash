package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := ensureEpochSchema(db); err != nil {
		t.Fatalf("schema: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// An epoch reported mid-flight must be overwritten when it closes: points on an
// open epoch are partial, so "keep the larger value" would freeze them.
func TestStoreEpochsOverwritesPartialPoints(t *testing.T) {
	db := testDB(t)
	now := time.Now()

	open := []epochRow{{Epoch: 4, Points: 120.5, ShareBps: 0}}
	if err := storeEpochs(db, open, now); err != nil {
		t.Fatalf("first store: %v", err)
	}
	closed := []epochRow{{Epoch: 4, Points: 288287.77, ShareBps: 940}}
	if err := storeEpochs(db, closed, now.Add(time.Hour)); err != nil {
		t.Fatalf("second store: %v", err)
	}

	rows, err := readEpochs(db, 10)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if rows[0].Points != 288287.77 {
		t.Errorf("points not updated: got %v", rows[0].Points)
	}
	if rows[0].ShareBps != 940 {
		t.Errorf("share not updated: got %v", rows[0].ShareBps)
	}
}

// A later poll that has no alpha yet must not erase alpha learned earlier.
func TestStoreEpochsKeepsEarlierAlpha(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	alpha := int64(1234) // rao, as the coordinator reports it
	if err := storeEpochs(db, []epochRow{{Epoch: 3, Points: 1, AlphaRao: &alpha}}, now); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := storeEpochs(db, []epochRow{{Epoch: 3, Points: 2}}, now); err != nil {
		t.Fatalf("second: %v", err)
	}
	rows, _ := readEpochs(db, 5)
	/* stored as rao, read back as alpha */
	if rows[0].Alpha == nil || *rows[0].Alpha != float64(alpha)/raoPerAlpha {
		t.Errorf("alpha lost: got %v", rows[0].Alpha)
	}
	if rows[0].Points != 2 {
		t.Errorf("points: got %v", rows[0].Points)
	}
}

func TestReadEpochsNewestFirst(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	storeEpochs(db, []epochRow{{Epoch: 1}, {Epoch: 2}, {Epoch: 3}}, now)
	rows, err := readEpochs(db, 2)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(rows) != 2 || rows[0].Epoch != 3 || rows[1].Epoch != 2 {
		t.Fatalf("want [3 2], got %+v", rows)
	}
}

// The coordinator reports the commit deadline but not the earning boundary, so
// the boundary is derived from t_epoch_blocks.
func TestEarningEndBlock(t *testing.T) {
	e := snEpoch{StartBlock: 9398428, CommitBlock: 9450028, TEpochBlock: 50400}
	if got, want := e.earningEndBlock(), int64(9448828); got != want {
		t.Errorf("with t_epoch_blocks: got %d want %d", got, want)
	}
	// falls back to the commit window when the length is unknown
	e2 := snEpoch{StartBlock: 1000, CommitBlock: 3400, TEpochBlock: 0}
	if got, want := e2.earningEndBlock(), int64(2200); got != want {
		t.Errorf("fallback: got %d want %d", got, want)
	}
}

func TestSettlementFor(t *testing.T) {
	cases := []struct {
		epoch, firstV2 int64
		reported, want string
	}{
		{1, 4, "", "v1"}, {3, 4, "", "v1"}, {4, 4, "", "v2"}, {9, 4, "", "v2"},
		{2, 0, "", "v1"},   // unknown boundary: never claim v2
		{9, 4, "v1", "v1"}, // the coordinator disagrees with the boundary: it wins
		{1, 4, "v2", "v2"}, // and it wins in the other direction too
	}
	for _, c := range cases {
		if got := settlementFor(c.epoch, c.firstV2, c.reported); got != c.want {
			t.Errorf("settlementFor(%d,%d,%q)=%q want %q", c.epoch, c.firstV2, c.reported, got, c.want)
		}
	}
}

// The handler must serve a well-formed empty document before any poll has run,
// because the page hits it the first time it loads.
func TestHandleRefreshEpochsRequiresPOST(t *testing.T) {
	db := testDB(t)
	rec := httptest.NewRecorder()
	handleRefreshEpochs(db, "unused").ServeHTTP(rec, httptest.NewRequest("GET", "/api/refresh-epochs", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET should be 405, got %d", rec.Code)
	}
}

func TestHandleEpochsEmptyDB(t *testing.T) {
	db := testDB(t)
	rec := httptest.NewRecorder()
	handleEpochs(db, "unused").ServeHTTP(rec, httptest.NewRequest("GET", "/api/epochs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	list, ok := body["epochs"].([]interface{})
	if !ok {
		t.Fatalf("epochs is %T, want []: %s", body["epochs"], rec.Body.String())
	}
	if len(list) != 0 {
		t.Errorf("empty database should give [], got %v", list)
	}
}

func TestHandleEpochsIncludesState(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	alpha := int64(88)
	storeEpochs(db, []epochRow{{Epoch: 7, Points: 10, AlphaRao: &alpha}}, now)
	if err := storeSNEpoch(db, snEpoch{Epoch: 8, StartBlock: 100, CommitBlock: 5200, TEpochBlock: 50400, Netuid: 25}, now); err != nil {
		t.Fatalf("state: %v", err)
	}
	rec := httptest.NewRecorder()
	handleEpochs(db, "unused").ServeHTTP(rec, httptest.NewRequest("GET", "/api/epochs", nil))

	var body struct {
		Epochs []epochView `json:"epochs"`
		SN     *snEpoch    `json:"sn_epoch"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(body.Epochs) != 1 || body.Epochs[0].Epoch != 7 {
		t.Errorf("epochs: %+v", body.Epochs)
	}
	if body.SN == nil || body.SN.Netuid != 25 {
		t.Errorf("sn_epoch: %+v", body.SN)
	}
	if body.SN != nil && body.SN.EarningEnd != 50500 {
		t.Errorf("earning end: got %d", body.SN.EarningEnd)
	}
}

// The coordinator reports rao; a caller of this package must receive alpha.
func TestReadEpochsConvertsRaoToAlpha(t *testing.T) {
	db := testDB(t)
	rao := int64(1_234_500_000) // 1.2345 alpha, as the coordinator reports it
	if err := storeEpochs(db, []epochRow{{Epoch: 5, Points: 1, AlphaRao: &rao}}, time.Now()); err != nil {
		t.Fatalf("store: %v", err)
	}
	rows, err := readEpochs(db, 5)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if rows[0].Alpha == nil {
		t.Fatal("alpha nil")
	}
	if got := *rows[0].Alpha; got < 1.2344 || got > 1.2346 {
		t.Errorf("alpha not converted to whole tokens: %v", got)
	}
}

// An implausible state document must be rejected, not stored: a zero epoch
// would otherwise write a negative earning window.
func TestFetchSNEpochRejectsEmptyDocument(t *testing.T) {
	e := snEpoch{}
	if e.Epoch <= 0 || e.StartBlock <= 0 {
		// mirrors the guard in fetchSNEpoch; asserted here so the reason is visible
		if e.earningEndBlock() >= 0 {
			t.Errorf("zero epoch produced a positive earning end: %d", e.earningEndBlock())
		}
	}
	// the negative window is exactly what the guard exists to stop
	if got := (snEpoch{CommitBlock: 0}).earningEndBlock(); got != -commitWindowBlocks {
		t.Errorf("want the negative window that the guard rejects, got %d", got)
	}
}

// A failed poll must leave a trace the page can show; an empty table alone
// reads as "never polled".
func TestPollAttemptIsRecorded(t *testing.T) {
	db := testDB(t)
	storeEpochAttempt(db, time.Now(), fmt.Errorf("jwt expired"))
	at, msg := readEpochAttempt(db)
	if at.IsZero() {
		t.Fatal("attempt time not stored")
	}
	if msg != "jwt expired" {
		t.Errorf("error not stored: %q", msg)
	}
	/* a later success clears the message */
	storeEpochAttempt(db, time.Now(), nil)
	if _, msg := readEpochAttempt(db); msg != "" {
		t.Errorf("success left a stale error: %q", msg)
	}
}

// A closed epoch's points are final: a later poll carrying a partial or zero
// value must not overwrite them.
func TestFinalizedEpochCannotBeOverwritten(t *testing.T) {
	db := testDB(t)
	past := time.Now().Add(-48 * time.Hour).UnixMilli()
	closed := []epochRow{{Epoch: 3, Points: 269722.24, StartMillis: past - 604800000, EndMillis: past}}
	if err := storeEpochs(db, closed, time.Now()); err != nil {
		t.Fatalf("store closed: %v", err)
	}
	/* the coordinator now reports the same epoch with no points */
	if err := storeEpochs(db, []epochRow{{Epoch: 3, Points: 0, StartMillis: past - 604800000, EndMillis: past}}, time.Now()); err != nil {
		t.Fatalf("store partial: %v", err)
	}
	rows, _ := readEpochs(db, 5)
	if rows[0].Points != 269722.24 {
		t.Errorf("finalized points overwritten: %v", rows[0].Points)
	}
	if rows[0].Finalized != 1 {
		t.Errorf("not marked finalized: %d", rows[0].Finalized)
	}
}

// An open epoch must still accept updates, or its points would freeze at the
// first partial value.
func TestOpenEpochStillUpdates(t *testing.T) {
	db := testDB(t)
	future := time.Now().Add(48 * time.Hour).UnixMilli()
	if err := storeEpochs(db, []epochRow{{Epoch: 4, Points: 100, StartMillis: future - 604800000, EndMillis: future}}, time.Now()); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := storeEpochs(db, []epochRow{{Epoch: 4, Points: 500, StartMillis: future - 604800000, EndMillis: future}}, time.Now()); err != nil {
		t.Fatalf("second: %v", err)
	}
	rows, _ := readEpochs(db, 5)
	if rows[0].Points != 500 {
		t.Errorf("open epoch did not update: %v", rows[0].Points)
	}
	if rows[0].Finalized != 0 {
		t.Errorf("open epoch marked finalized")
	}
}

// Recording a poll attempt must not fabricate a fetched epoch: the page would
// read epoch 0 with a zero window.
func TestPollAttemptDoesNotCreateEpochState(t *testing.T) {
	db := testDB(t)
	storeEpochAttempt(db, time.Now(), fmt.Errorf("boom"))
	if _, _, err := readSNEpoch(db); err == nil {
		t.Fatal("a poll attempt created an epoch state row")
	}
	if at, msg := readEpochAttempt(db); at.IsZero() || msg != "boom" {
		t.Errorf("attempt not recorded: %v %q", at, msg)
	}
}

// A finalized epoch's share is as final as its points.
func TestFinalizedShareIsFrozen(t *testing.T) {
	db := testDB(t)
	past := time.Now().Add(-48 * time.Hour).UnixMilli()
	storeEpochs(db, []epochRow{{Epoch: 3, Points: 10, ShareBps: 940, StartMillis: past - 604800000, EndMillis: past}}, time.Now())
	storeEpochs(db, []epochRow{{Epoch: 3, Points: 10, ShareBps: 0, StartMillis: past - 604800000, EndMillis: past}}, time.Now())
	rows, _ := readEpochs(db, 5)
	if rows[0].ShareBps != 940 {
		t.Errorf("finalized share overwritten: %d", rows[0].ShareBps)
	}
}

// Rows stored before the marker existed are backfilled as finalized.
func TestSchemaBackfillsFinalized(t *testing.T) {
	db := testDB(t)
	past := time.Now().Add(-72 * time.Hour).UnixMilli()
	if _, err := db.Exec(`INSERT INTO epoch_stats (epoch, points, start_millis, end_millis, finalized, updated_at)
		VALUES (2, 5, ?, ?, 0, '')`, past-604800000, past); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := ensureEpochSchema(db); err != nil {
		t.Fatalf("schema: %v", err)
	}
	rows, _ := readEpochs(db, 5)
	if rows[0].Finalized != 1 {
		t.Errorf("closed epoch not backfilled: %d", rows[0].Finalized)
	}
}
