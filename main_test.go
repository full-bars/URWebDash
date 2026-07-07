package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPathOverrides(t *testing.T) {
	t.Setenv("STATS_DB", "/tmp/test_stats.db")
	t.Setenv("JWT_PATH", "/tmp/test_jwt")
	if p := dbPath(); p != "/tmp/test_stats.db" {
		t.Fatalf("dbPath() = %q, want /tmp/test_stats.db", p)
	}
	if p := jwtPath(); p != "/tmp/test_jwt" {
		t.Fatalf("jwtPath() = %q, want /tmp/test_jwt", p)
	}
}

func TestPathDefaults(t *testing.T) {
	os.Unsetenv("STATS_DB")
	os.Unsetenv("JWT_PATH")
	home, _ := os.UserHomeDir()
	wantDB := filepath.Join(home, ".urnetwork", "wallet_stats.db")
	if p := dbPath(); p != wantDB {
		t.Fatalf("dbPath() = %q, want %q", p, wantDB)
	}
	wantJWT := filepath.Join(home, ".urnetwork", "jwt")
	if p := jwtPath(); p != wantJWT {
		t.Fatalf("jwtPath() = %q, want %q", p, wantJWT)
	}
}

func TestJSONError(t *testing.T) {
	w := httptest.NewRecorder()
	jsonError(w, "test error")
	resp := w.Result()
	if resp.StatusCode != 500 {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	if body["error"] != "test error" {
		t.Fatalf("error = %q, want %q", body["error"], "test error")
	}
}

func TestHandleStatus(t *testing.T) {
	Version = "v0.0.1"
	startTime = startTime.Add(-1 * time.Hour)
	defer func() { startTime = time.Now() }()

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/status", nil)
	handleStatus(w, r)

	resp := w.Result()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	if body["version"] != "v0.0.1" {
		t.Fatalf("version = %q, want v0.0.1", body["version"])
	}
	if !strings.Contains(body["uptime"], "1h") {
		t.Fatalf("uptime = %q, want to contain 1h", body["uptime"])
	}
}

func TestOpenDB_CreatesTables(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("STATS_DB", filepath.Join(tmp, "test.db"))

	db, err := openDB()
	if err != nil {
		t.Fatalf("openDB() = %v", err)
	}
	defer db.Close()

	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		rows.Scan(&name)
		tables = append(tables, name)
	}

	if !contains(tables, "wallet_stats") {
		t.Fatalf("missing wallet_stats table; got %v", tables)
	}
	if !contains(tables, "payout_stats") {
		t.Fatalf("missing payout_stats table; got %v", tables)
	}
}

func TestInsertAndQueryWalletStats(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("STATS_DB", filepath.Join(tmp, "test.db"))

	db, err := openDB()
	if err != nil {
		t.Fatalf("openDB() = %v", err)
	}
	defer db.Close()

	_, err = db.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(?, ?, ?, ?)",
		1000, 500, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM wallet_stats").Scan(&count)
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
}

func TestHandleWalletSummary(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("STATS_DB", filepath.Join(tmp, "test.db"))

	db, err := openDB()
	if err != nil {
		t.Fatalf("openDB() = %v", err)
	}
	defer db.Close()

	for i := 0; i < 3; i++ {
		ts := "2026-01-01T00:0" + string(rune('0'+i)) + ":00Z"
		db.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(?, ?, ?, ?)",
			1000*int64(i+1), 500*int64(i+1), ts, ts)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/wallet-summary", nil)
	handleWalletSummary(db)(w, r)

	resp := w.Result()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&body)
	if body["count"].(float64) != 3 {
		t.Fatalf("count = %v, want 3", body["count"])
	}
	if body["paid_bytes"].(float64) != 3000 {
		t.Fatalf("paid_bytes = %v, want 3000", body["paid_bytes"])
	}
}

func TestHandleClear(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("STATS_DB", filepath.Join(tmp, "test.db"))

	db, err := openDB()
	if err != nil {
		t.Fatalf("openDB() = %v", err)
	}
	defer db.Close()

	db.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(1, 2, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/clear", nil)
	handleClear(db)(w, r)

	var count int
	db.QueryRow("SELECT COUNT(*) FROM wallet_stats").Scan(&count)
	if count != 0 {
		t.Fatalf("count after clear = %d, want 0", count)
	}
}

func TestHandleWalletStats_ReverseOrder(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("STATS_DB", filepath.Join(tmp, "test.db"))

	db, err := openDB()
	if err != nil {
		t.Fatalf("openDB() = %v", err)
	}
	defer db.Close()

	for i := 0; i < 5; i++ {
		ts := "2026-01-01T00:0" + string(rune('0'+i)) + ":00Z"
		db.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(?, ?, ?, ?)",
			100, 50, ts, ts)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/wallet-stats", nil)
	handleWalletStats(db)(w, r)

	var body struct {
		Entries []struct {
			CreatedAt string `json:"created_at"`
		} `json:"entries"`
	}
	json.NewDecoder(w.Body).Decode(&body)

	if len(body.Entries) < 2 {
		t.Fatalf("got %d entries, want >=2", len(body.Entries))
	}
	if body.Entries[0].CreatedAt <= body.Entries[1].CreatedAt {
		t.Fatalf("entries not reversed: first=%q, second=%q",
			body.Entries[0].CreatedAt, body.Entries[1].CreatedAt)
	}
}

func TestHandleWalletStats_PartialScanFailure(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("STATS_DB", filepath.Join(tmp, "test.db"))

	db, err := openDB()
	if err != nil {
		t.Fatalf("openDB() = %v", err)
	}
	defer db.Close()

	db.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(1, 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/wallet-stats", nil)
	handleWalletStats(db)(w, r)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
