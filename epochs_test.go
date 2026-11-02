package main

import (
	"database/sql"
	"encoding/json"
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
	alpha := 1234.0
	if err := storeEpochs(db, []epochRow{{Epoch: 3, Points: 1, Alpha: &alpha}}, now); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := storeEpochs(db, []epochRow{{Epoch: 3, Points: 2}}, now); err != nil {
		t.Fatalf("second: %v", err)
	}
	rows, _ := readEpochs(db, 5)
	if rows[0].Alpha == nil || *rows[0].Alpha != alpha {
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
		want           string
	}{
		{1, 4, "v1"}, {3, 4, "v1"}, {4, 4, "v2"}, {9, 4, "v2"},
		{2, 0, "v1"}, // unknown boundary: never claim v2
	}
	for _, c := range cases {
		if got := settlementFor(c.epoch, c.firstV2); got != c.want {
			t.Errorf("settlementFor(%d,%d)=%q want %q", c.epoch, c.firstV2, got, c.want)
		}
	}
}

// The handler must serve a well-formed empty document before any poll has run,
// because the page hits it the first time it loads.
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
	if _, ok := body["epochs"]; !ok {
		t.Errorf("no epochs key: %s", rec.Body.String())
	}
}

func TestHandleEpochsIncludesState(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	alpha := 88.0
	storeEpochs(db, []epochRow{{Epoch: 7, Points: 10, Alpha: &alpha}}, now)
	if err := storeSNEpoch(db, snEpoch{Epoch: 8, StartBlock: 100, CommitBlock: 5200, TEpochBlock: 50400, Netuid: 25}, now); err != nil {
		t.Fatalf("state: %v", err)
	}
	rec := httptest.NewRecorder()
	handleEpochs(db, "unused").ServeHTTP(rec, httptest.NewRequest("GET", "/api/epochs", nil))

	var body struct {
		Epochs []epochRow `json:"epochs"`
		SN     *snEpoch   `json:"sn_epoch"`
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
