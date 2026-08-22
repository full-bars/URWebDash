package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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

func TestFetchStats(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-jwt" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"paid_bytes_provided":1234567890,"unpaid_bytes_provided":987654321}`)
	}))
	defer ts.Close()

	orig := httpClient.Transport
	defer func() { httpClient.Transport = orig }()
	httpClient.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = ts.Listener.Addr().String()
		return ts.Client().Transport.RoundTrip(req)
	})

	s, err := fetchStats("test-jwt")
	if err != nil {
		t.Fatalf("fetchStats: %v", err)
	}
	if s.PaidBytes != 1234567890 {
		t.Fatalf("paid = %d, want 1234567890", s.PaidBytes)
	}
	if s.UnpaidBytes != 987654321 {
		t.Fatalf("unpaid = %d, want 987654321", s.UnpaidBytes)
	}
}

func TestFetchStats_HTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
	}))
	defer ts.Close()

	orig := httpClient.Transport
	defer func() { httpClient.Transport = orig }()
	httpClient.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = ts.Listener.Addr().String()
		return ts.Client().Transport.RoundTrip(req)
	})

	_, err := fetchStats("jwt")
	if err == nil {
		t.Fatalf("expected error for 502 response")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Fatalf("error = %v, want to contain 502", err)
	}
}

func TestFetchPayouts(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-jwt" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"account_payments":[{"token_amount":12.34,"payout_byte_count":5000,"completed":true}],"account_points":[]}`)
	}))
	defer ts.Close()

	orig := httpClient.Transport
	defer func() { httpClient.Transport = orig }()
	httpClient.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = ts.Listener.Addr().String()
		return ts.Client().Transport.RoundTrip(req)
	})

	r, err := fetchPayouts("test-jwt")
	if err != nil {
		t.Fatalf("fetchPayouts: %v", err)
	}
	if len(r.AccountPayments) != 1 {
		t.Fatalf("got %d payments, want 1", len(r.AccountPayments))
	}
	if r.AccountPayments[0].TokenAmount != 12.34 {
		t.Fatalf("token_amount = %v, want 12.34", r.AccountPayments[0].TokenAmount)
	}
}

func TestFetchPoints(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"account_points":[{"point_value":5000000},{"point_value":7000000}]}`)
	}))
	defer ts.Close()

	orig := httpClient.Transport
	defer func() { httpClient.Transport = orig }()
	httpClient.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = ts.Listener.Addr().String()
		return ts.Client().Transport.RoundTrip(req)
	})

	pts, err := fetchPoints("jwt")
	if err != nil {
		t.Fatalf("fetchPoints: %v", err)
	}
	if len(pts) != 2 {
		t.Fatalf("points count = %v, want 2", len(pts))
	}
	var total int64
	for _, p := range pts {
		total += p.PointValue
	}
	if total != 12000000 {
		t.Fatalf("total points = %v, want 12000000 (12M)", total)
	}
}

func TestReadJWT(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "jwt")
	os.WriteFile(f, []byte(" my-token-123 \n"), 0644)
	t.Setenv("JWT_PATH", f)

	tok, err := readJWT()
	if err != nil {
		t.Fatalf("readJWT: %v", err)
	}
	if tok != "my-token-123" {
		t.Fatalf("token = %q, want %q", tok, "my-token-123")
	}
}

func TestHandlePayoutStats_EmptyCache(t *testing.T) {
	payoutCacheMu.Lock()
	payoutCache = nil
	payoutCacheTime = time.Time{}
	payoutCacheMu.Unlock()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"account_payments":[{"token_amount":5.0,"payout_byte_count":100,"completed":false}],"account_points":[]}`)
	}))
	defer ts.Close()

	orig := httpClient.Transport
	defer func() { httpClient.Transport = orig }()
	httpClient.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = ts.Listener.Addr().String()
		return ts.Client().Transport.RoundTrip(req)
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/payout-stats", nil)
	handlePayoutStats("jwt")(w, r)

	var body struct {
		Payments []payoutRecord `json:"payments"`
		Count    int            `json:"count"`
	}
	json.NewDecoder(w.Body).Decode(&body)
	if body.Count != 1 {
		t.Fatalf("count = %d, want 1", body.Count)
	}
	if body.Payments[0].TokenAmount != 5.0 {
		t.Fatalf("token_amount = %v, want 5.0", body.Payments[0].TokenAmount)
	}
}

func TestHandleRefreshPayout_Success(t *testing.T) {
	payoutCacheMu.Lock()
	payoutCache = nil
	payoutCacheTime = time.Time{}
	payoutCacheMu.Unlock()

	payCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payCount++
		fmt.Fprint(w, `{"account_payments":[{"token_amount":9.99,"payout_byte_count":300,"completed":true}],"account_points":[{"point_value":1000000}]}`)
	}))
	defer ts.Close()

	orig := httpClient.Transport
	defer func() { httpClient.Transport = orig }()
	httpClient.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = ts.Listener.Addr().String()
		return ts.Client().Transport.RoundTrip(req)
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/refresh-payout", nil)
	handleRefreshPayout("jwt")(w, r)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body struct {
		Success bool    `json:"success"`
		Count   int     `json:"count"`
		Points  float64 `json:"points"`
	}
	json.NewDecoder(w.Body).Decode(&body)
	if !body.Success {
		t.Fatalf("success = false, want true")
	}
	if body.Count != 1 {
		t.Fatalf("count = %d, want 1", body.Count)
	}
	if body.Points != 1.0 {
		t.Fatalf("points = %v, want 1.0", body.Points)
	}

	payoutCacheMu.RLock()
	if payoutCache[0].TokenAmount != 9.99 {
		t.Fatalf("cache token_amount = %v, want 9.99", payoutCache[0].TokenAmount)
	}
	payoutCacheMu.RUnlock()
}

func TestHandleRefreshPayout_APIError(t *testing.T) {
	payoutCacheMu.Lock()
	payoutCache = nil
	payoutCacheTime = time.Time{}
	payoutCacheMu.Unlock()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer ts.Close()

	orig := httpClient.Transport
	defer func() { httpClient.Transport = orig }()
	httpClient.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = ts.Listener.Addr().String()
		return ts.Client().Transport.RoundTrip(req)
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/refresh-payout", nil)
	handleRefreshPayout("jwt")(w, r)

	if w.Code != 500 {
		t.Fatalf("status = %d, want 500", w.Code)
	}

	payoutCacheMu.RLock()
	if payoutLastError == "" {
		t.Fatalf("payoutLastError not set after API failure")
	}
	payoutCacheMu.RUnlock()
}

func TestImportJSON(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("STATS_DB", filepath.Join(tmp, "test.db"))

	db, _ := openDB()
	defer db.Close()

	records := []exportRecord{
		{CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z", PaidBytesProvided: 1000, UnpaidBytes: 500},
		{CreatedAt: "2026-01-01T00:15:00Z", UpdatedAt: "2026-01-01T00:15:00Z", PaidBytesProvided: 2000, UnpaidBytes: 700},
	}
	data, _ := json.Marshal(records)
	f := filepath.Join(tmp, "import.json")
	os.WriteFile(f, data, 0644)

	importJSON(f)

	var count int
	db.QueryRow("SELECT COUNT(*) FROM wallet_stats").Scan(&count)
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
}

func TestImportJSON_Deduplicate(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("STATS_DB", filepath.Join(tmp, "test.db"))

	db, _ := openDB()
	defer db.Close()

	records := []exportRecord{
		{CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z", PaidBytesProvided: 1000, UnpaidBytes: 500},
	}
	data, _ := json.Marshal(records)
	f := filepath.Join(tmp, "import.json")
	os.WriteFile(f, data, 0644)

	importJSON(f)
	importJSON(f)

	var count int
	db.QueryRow("SELECT COUNT(*) FROM wallet_stats").Scan(&count)
	if count != 1 {
		t.Fatalf("count = %d after duplicate import, want 1", count)
	}
}

func TestHandleRefresh_WrongMethod(t *testing.T) {
	payoutCacheMu.Lock()
	payoutCache = nil
	payoutCacheTime = time.Time{}
	payoutCacheMu.Unlock()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"paid_bytes_provided":999,"unpaid_bytes_provided":111,"error":null}`)
	}))
	defer ts.Close()

	orig := httpClient.Transport
	defer func() { httpClient.Transport = orig }()
	httpClient.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = ts.Listener.Addr().String()
		return ts.Client().Transport.RoundTrip(req)
	})

	tmp := t.TempDir()
	t.Setenv("STATS_DB", filepath.Join(tmp, "test.db"))
	db, _ := openDB()
	defer db.Close()

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/refresh", nil)
	handleRefresh("jwt", db)(w, r)
	if w.Code != 405 {
		t.Fatalf("status = %d, want 405 for GET", w.Code)
	}
}

func TestHandleRefresh_APIError(t *testing.T) {
	payoutCacheMu.Lock()
	payoutCache = nil
	payoutCacheTime = time.Time{}
	payoutCacheMu.Unlock()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer ts.Close()

	orig := httpClient.Transport
	defer func() { httpClient.Transport = orig }()
	httpClient.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = ts.Listener.Addr().String()
		return ts.Client().Transport.RoundTrip(req)
	})

	tmp := t.TempDir()
	t.Setenv("STATS_DB", filepath.Join(tmp, "test.db"))
	db, _ := openDB()
	defer db.Close()

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/refresh", nil)
	handleRefresh("jwt", db)(w, r)
	if w.Code != 500 {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if body["error"] == "" {
		t.Fatalf("expected error body, got %v", body)
	}
}

func TestHandleRefreshPayout_ResponseError(t *testing.T) {
	payoutCacheMu.Lock()
	payoutCache = nil
	payoutCacheTime = time.Time{}
	payoutCacheMu.Unlock()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"account_payments":[],"account_points":[],"error":{"message":"unauthorized"}}`)
	}))
	defer ts.Close()

	orig := httpClient.Transport
	defer func() { httpClient.Transport = orig }()
	httpClient.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = ts.Listener.Addr().String()
		return ts.Client().Transport.RoundTrip(req)
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/refresh-payout", nil)
	handleRefreshPayout("jwt")(w, r)
	if w.Code != 500 {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if body["error"] != "unauthorized" {
		t.Fatalf("error = %q, want 'unauthorized'", body["error"])
	}
}

func TestHandleWalletSummary_EmptyDB(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("STATS_DB", filepath.Join(tmp, "test.db"))
	db, _ := openDB()
	defer db.Close()

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/wallet-summary", nil)
	handleWalletSummary(db)(w, r)
	if w.Code != 500 {
		t.Fatalf("status = %d, want 500 for empty db", w.Code)
	}
}

func TestHandleWalletStats_ChangeBytes(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("STATS_DB", filepath.Join(tmp, "test.db"))

	db, err := openDB()
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()

	entries := []struct {
		paid   int64
		unpaid int64
		ts     string
	}{
		{1000, 500, "2026-07-20T05:00:00Z"},
		{1000, 800, "2026-07-20T05:15:00Z"},
		{1000, 1200, "2026-07-20T05:30:00Z"},
		{1100, 1500, "2026-07-20T05:45:00Z"},
	}
	for _, e := range entries {
		_, err := db.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(?, ?, ?, ?)",
			e.paid, e.unpaid, e.ts, e.ts)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/wallet-stats", nil)
	handleWalletStats(db)(w, r)

	var resp struct {
		Entries []struct {
			PaidBytes   int64  `json:"paid_bytes"`
			UnpaidBytes int64  `json:"unpaid_bytes"`
			CreatedAt   string `json:"created_at"`
			ChangeBytes int64  `json:"change_bytes"`
		} `json:"entries"`
		Count int `json:"count"`
		Total int `json:"total"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Count != 4 {
		t.Fatalf("count = %d, want 4", resp.Count)
	}

	// Entries should be newest first
	if resp.Entries[0].CreatedAt != "2026-07-20T05:45:00Z" {
		t.Fatalf("first entry = %q, want newest first", resp.Entries[0].CreatedAt)
	}

	// change_bytes = total(paid+unpaid) - previous total
	// row 1 (05:00): 1500 total, no previous → change_bytes = 0
	// row 2 (05:15): 1800 total, prev 1500 → change_bytes = 300
	// row 3 (05:30): 2200 total, prev 1800 → change_bytes = 400
	// row 4 (05:45): 2600 total, prev 2200 → change_bytes = 400
	// reversed (newest first): 05:45(400), 05:30(400), 05:15(300), 05:00(0)
	expected := []struct {
		ts          string
		changeBytes int64
	}{
		{"2026-07-20T05:45:00Z", 400},
		{"2026-07-20T05:30:00Z", 400},
		{"2026-07-20T05:15:00Z", 300},
		{"2026-07-20T05:00:00Z", 0},
	}
	for i, exp := range expected {
		if resp.Entries[i].CreatedAt != exp.ts {
			t.Errorf("entry[%d] timestamp = %q, want %q", i, resp.Entries[i].CreatedAt, exp.ts)
		}
		if resp.Entries[i].ChangeBytes != exp.changeBytes {
			t.Errorf("entry[%d] change_bytes = %d, want %d (ts=%s)", i, resp.Entries[i].ChangeBytes, exp.changeBytes, exp.ts)
		}
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// --- payout notification store ---

func resetNotifyStore(t *testing.T, storePath string) {
	t.Helper()
	t.Setenv("PAYOUT_NOTIFY_STORE", storePath)
	os.Remove(storePath)
	os.Remove(storePath + ".tmp")
	notifyStoreMu.Lock()
	defer notifyStoreMu.Unlock()
	notifyStore = nil
	notifyStorePath = ""
	notifyStoreSeeded = false
}

func notifyTestPayouts() []payoutRecord {
	return []payoutRecord{
		{PaymentID: "pay-1", TxHash: "tx-1111111111111111111111111111111111111111111111111111111111111111", TokenAmount: 12.34, PayoutByteCount: 5000000000, Completed: true, Blockchain: "solana"},
		{PaymentID: "pay-2", TxHash: "tx-2222222222222222222222222222222222222222222222222222222222222222", TokenAmount: 5.00, PayoutByteCount: 2000000000, Completed: false, Blockchain: "solana"},
	}
}

func TestNotifyStoreColdStartSeedsWithoutNotifying(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "notified.json")
	resetNotifyStore(t, storePath)

	var sent []string
	oldSend := notifySend
	notifySend = func(content string) { sent = append(sent, content) }
	defer func() { notifySend = oldSend }()

	fresh := notifyTestPayouts()
	syncPayoutNotifyStore(fresh, true) // cold start via the explicit refresh path

	if len(sent) != 0 {
		t.Fatalf("cold start sent %d notifications, want 0 (seed only)", len(sent))
	}
	if _, err := os.Stat(storePath); err != nil {
		t.Fatalf("store file not written after seeding: %v", err)
	}
	// Identical re-fetch must stay silent.
	syncPayoutNotifyStore(fresh, true)
	if len(sent) != 0 {
		t.Fatalf("dedup failed: %d notifications after re-fetch", len(sent))
	}
}

func TestNotifyStoreRestartDoesNotReannounce(t *testing.T) {
	// Exact reported bug: process restart wipes the in-memory cache, and the
	// old code re-announced every payout with a tx_hash as brand new.
	storePath := filepath.Join(t.TempDir(), "notified.json")
	resetNotifyStore(t, storePath)

	// First run seeds the baseline.
	syncPayoutNotifyStore(notifyTestPayouts(), true)

	// Simulate restart: package state reset, store file still on disk.
	notifyStoreMu.Lock()
	notifyStore = nil
	notifyStorePath = ""
	notifyStoreSeeded = false
	notifyStoreMu.Unlock()

	var sent []string
	oldSend := notifySend
	notifySend = func(content string) { sent = append(sent, content) }
	defer func() { notifySend = oldSend }()

	syncPayoutNotifyStore(notifyTestPayouts(), true)
	if len(sent) != 0 {
		t.Fatalf("restart re-announced %d payouts, want 0: %v", len(sent), sent)
	}
}

func TestNotifyStoreNewPaymentAndCompletion(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "notified.json")
	resetNotifyStore(t, storePath)

	syncPayoutNotifyStore(notifyTestPayouts(), true) // seed

	var sent []string
	oldSend := notifySend
	notifySend = func(content string) { sent = append(sent, content) }
	defer func() { notifySend = oldSend }()

	// New payout appears (new payment_id + tx_hash).
	fresh := append(notifyTestPayouts(), payoutRecord{PaymentID: "pay-3", TxHash: "tx-3333333333333333333333333333333333333333333333333333333333333333", TokenAmount: 99.0, PayoutByteCount: 7000000000, Completed: false, Blockchain: "solana"})
	syncPayoutNotifyStore(fresh, true)
	if len(sent) != 1 || !strings.Contains(sent[0], "💰 **New Payout**") || !strings.Contains(sent[0], "⏳ Pending") {
		t.Fatalf("new payout notification wrong: %v", sent)
	}

	// Existing pending payment completes.
	sent = nil
	fresh[1].Completed = true // pay-2
	syncPayoutNotifyStore(fresh, true)
	if len(sent) != 1 || !strings.Contains(sent[0], "✅ **Payout Completed**") {
		t.Fatalf("completion notification wrong: %v", sent)
	}

	// No further changes -> silent.
	sent = nil
	syncPayoutNotifyStore(fresh, true)
	if len(sent) != 0 {
		t.Fatalf("no-change fetch notified: %v", sent)
	}
}

func TestNotifyStorePathOverride(t *testing.T) {
	resetNotifyStore(t, filepath.Join(t.TempDir(), "notified.json"))
	t.Setenv("PAYOUT_NOTIFY_STORE", "/tmp/custom_notify.json")
	notifyStoreMu.Lock()
	notifyStorePath = ""
	notifyStoreMu.Unlock()
	if p := notifyStoreFile(); p != "/tmp/custom_notify.json" {
		t.Fatalf("notifyStoreFile() = %q, want override", p)
	}
}

func TestNotifyStoreEmptyFirstFetchDoesNotSeed(t *testing.T) {
	// A transient upstream glitch returning zero payouts on cold start must
	// NOT become the permanent baseline (that would re-announce everything
	// on the next good fetch).
	storePath := filepath.Join(t.TempDir(), "notified.json")
	resetNotifyStore(t, storePath)

	var sent []string
	oldSend := notifySend
	notifySend = func(content string) { sent = append(sent, content) }
	defer func() { notifySend = oldSend }()

	// Cold start with an empty response: no seed file, no notifications.
	syncPayoutNotifyStore(nil, true)
	if len(sent) != 0 {
		t.Fatalf("empty first fetch notified: %v", sent)
	}
	if _, err := os.Stat(storePath); !os.IsNotExist(err) {
		t.Fatalf("empty first fetch wrote a seed file (err=%v)", err)
	}

	// Next fetch has real payouts: seeds silently, still no notifications.
	syncPayoutNotifyStore(notifyTestPayouts(), true)
	if len(sent) != 0 {
		t.Fatalf("seed fetch notified: %v", sent)
	}
	if _, err := os.Stat(storePath); err != nil {
		t.Fatalf("seed file missing after good fetch: %v", err)
	}

	// Identical re-fetch stays silent (baseline took).
	syncPayoutNotifyStore(notifyTestPayouts(), true)
	if len(sent) != 0 {
		t.Fatalf("re-fetch notified after seed: %v", sent)
	}
}

func TestNotifyStoreEmptyFileReseeds(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "notified.json")
	resetNotifyStore(t, storePath)

	// A valid-but-empty store file must be treated as unseeded.
	if err := os.WriteFile(storePath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	var sent []string
	oldSend := notifySend
	notifySend = func(content string) { sent = append(sent, content) }
	defer func() { notifySend = oldSend }()

	syncPayoutNotifyStore(notifyTestPayouts(), true)
	if len(sent) != 0 {
		t.Fatalf("empty store file caused notifications: %v", sent)
	}
	// The re-seeded baseline persists (file now non-empty).
	b, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if !strings.Contains(string(b), "pay-1") {
		t.Fatalf("store not re-seeded after empty file: %s", b)
	}
}

func TestNotifyStoreTxHashChangeReannounces(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "notified.json")
	resetNotifyStore(t, storePath)

	fresh := notifyTestPayouts()
	syncPayoutNotifyStore(fresh, true) // seed

	var sent []string
	oldSend := notifySend
	notifySend = func(content string) { sent = append(sent, content) }
	defer func() { notifySend = oldSend }()

	// Same payment_id, different tx_hash (re-planned payout): announce again.
	fresh[0].TxHash = "tx-9999999999999999999999999999999999999999999999999999999999999999"
	syncPayoutNotifyStore(fresh, true)
	if len(sent) != 1 || !strings.Contains(sent[0], "💰 **New Payout**") {
		t.Fatalf("tx_hash change not re-announced: %v", sent)
	}
}

func TestNotifyStoreIgnoresNoTxHash(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "notified.json")
	resetNotifyStore(t, storePath)

	var sent []string
	oldSend := notifySend
	notifySend = func(content string) { sent = append(sent, content) }
	defer func() { notifySend = oldSend }()

	// Payments without a tx_hash never seed or announce.
	noTx := []payoutRecord{{PaymentID: "pay-x", TokenAmount: 1.0, Completed: false, Blockchain: "solana"}}
	syncPayoutNotifyStore(noTx, true)
	if len(sent) != 0 {
		t.Fatalf("no-tx_hash payments notified: %v", sent)
	}
	if _, err := os.Stat(storePath); !os.IsNotExist(err) {
		t.Fatalf("no-tx_hash payments wrote a seed file")
	}

	// Mix: only the tx-hashed one seeds; the no-tx one is ignored.
	mixed := append(notifyTestPayouts(), payoutRecord{PaymentID: "pay-x", TokenAmount: 1.0, Completed: false, Blockchain: "solana"})
	syncPayoutNotifyStore(mixed, true)
	if len(sent) != 0 {
		t.Fatalf("mixed fetch notified: %v", sent)
	}
	syncPayoutNotifyStore(mixed, true)
	if len(sent) != 0 {
		t.Fatalf("re-fetch notified: %v", sent)
	}
}

func TestOpenDB_BusyTimeoutOnAllPooledConnections(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("STATS_DB", filepath.Join(tmp, "test.db"))

	db, err := openDB()
	if err != nil {
		t.Fatalf("openDB() = %v", err)
	}
	defer db.Close()

	// Grab several distinct connections from the pool and confirm each one
	// carries the busy_timeout pragma. A pool of 4 allows us to probe 4.
	var want int64 = 5000
	for i := 0; i < 4; i++ {
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatalf("db.Conn(%d) = %v", i, err)
		}
		var got int64
		if err := conn.QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&got); err != nil {
			conn.Close()
			t.Fatalf("query busy_timeout on conn %d: %v", i, err)
		}
		conn.Close()
		if got != want {
			t.Fatalf("conn %d busy_timeout = %d, want %d", i, got, want)
		}
	}
}

func TestOpenDB_DBPathPlain(t *testing.T) {
	t.Setenv("STATS_DB", "/tmp/plain-paths.db")
	if p := dbPath(); p != "/tmp/plain-paths.db" {
		t.Fatalf("dbPath() = %q, want plain path", p)
	}
	d := dbDSN()
	if !strings.HasPrefix(d, "file:///tmp/plain-paths.db") {
		t.Fatalf("dbDSN() = %q, want file: URI for the path", d)
	}
	if !strings.Contains(d, "_pragma=busy_timeout%285000%29") {
		t.Fatalf("dbDSN() = %q, want percent-encoded _pragma busy_timeout query", d)
	}
	// A literal '?' or '#' in the path must not split the DSN query.
	t.Setenv("STATS_DB", "/tmp/odd?name#.db")
	d2 := dbDSN()
	if strings.Count(d2, "?") != 1 {
		t.Fatalf("dbDSN() = %q, want exactly one '?' (the query separator)", d2)
	}
	if !strings.Contains(d2, "_pragma=busy_timeout%285000%29") {
		t.Fatalf("dbDSN() = %q, wanted _pragma to survive a '?'/'#' in the path", d2)
	}
}
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	return buf.String()
}

func TestHandleWalletSummary_ScanErrorIsLoggedAndHidden(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("STATS_DB", filepath.Join(tmp, "test.db"))

	db, err := openDB()
	if err != nil {
		t.Fatalf("openDB() = %v", err)
	}
	defer db.Close()

	// Insert a row whose paid_bytes value cannot be converted to int64,
	// forcing row.Scan to fail with an error other than sql.ErrNoRows.
	_, err = db.Exec(
		"INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(?, ?, ?, ?)",
		"not-a-number", 500, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("insert malformed row: %v", err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/wallet-summary", nil)

	output := captureStdout(t, func() {
		handleWalletSummary(db)(w, r)
	})

	if w.Code != 500 {
		t.Fatalf("status = %d, want 500", w.Code)
	}

	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if body["error"] != "no data yet" {
		t.Fatalf("error = %q, want %q (internal scan error must not leak to client)", body["error"], "no data yet")
	}

	if !strings.Contains(output, "[wallet-summary] scan error:") {
		t.Fatalf("expected scan error to be logged to stdout, got: %q", output)
	}
}

func TestHandleWalletSummary_EmptyDB_DoesNotLogScanError(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("STATS_DB", filepath.Join(tmp, "test.db"))

	db, err := openDB()
	if err != nil {
		t.Fatalf("openDB() = %v", err)
	}
	defer db.Close()

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/wallet-summary", nil)

	output := captureStdout(t, func() {
		handleWalletSummary(db)(w, r)
	})

	if w.Code != 500 {
		t.Fatalf("status = %d, want 500 for empty db", w.Code)
	}
	// sql.ErrNoRows is the expected "no data imported yet" case and must
	// not be logged as if it were an unexpected scan failure.
	if strings.Contains(output, "[wallet-summary] scan error:") {
		t.Fatalf("sql.ErrNoRows should not be logged as a scan error, got: %q", output)
	}
}

func TestIndexHTML_ClearsErrorBannerAfterSuccessfulSummaryFetch(t *testing.T) {
	// Regression test: loadWalletStats() previously left any error banner
	// from a prior failed load on screen even after a subsequent successful
	// fetch, because it only ever called showError() with a message, never
	// to clear it. The fix calls showError(null) immediately after
	// confirming summary.error is unset, before any DOM updates happen.
	pattern := regexp.MustCompile(`(?s)if\s*\(summary\.error\)\s*throw new Error\(summary\.error\);\s*showError\(null\);\s*document\.getElementById\('paid-data'\)`)
	if !pattern.MatchString(indexHTML) {
		t.Fatalf("expected showError(null) to run in loadWalletStats() right after the summary.error check and before rendering paid-data")
	}
}

func TestIndexHTML_ErrorBannerNotClearedBeforeErrorCheck(t *testing.T) {
	// Guard against a regression where showError(null) is hoisted above the
	// summary.error check, which would clear the banner even when the
	// summary fetch actually failed.
	badPattern := regexp.MustCompile(`(?s)showError\(null\);\s*if\s*\(summary\.error\)\s*throw new Error\(summary\.error\);`)
	if badPattern.MatchString(indexHTML) {
		t.Fatalf("showError(null) must not run before the summary.error check")
	}
}

// sendDiscordNotification posts to the resolved webhook URL in a goroutine.
// These tests point it at a local httptest server and count real posts.
type spikeCountServer struct {
	mu  sync.Mutex
	n   int
	URL string
}

func (c *spikeCountServer) add()       { c.mu.Lock(); c.n++; c.mu.Unlock() }
func (c *spikeCountServer) count() int { c.mu.Lock(); defer c.mu.Unlock(); return c.n }

func newSpikeCountServer() (*spikeCountServer, func()) {
	s := &spikeCountServer{}
	hh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.add()
		w.WriteHeader(204)
	}))
	s.URL = hh.URL
	return s, hh.Close
}

// The gap guard must skip a backfill: when the previous stored row is older
// than 30 minutes (the poller dropped windows), a huge delta is not a genuine
// 15-minute burst and must not alert.
func TestCheckTrafficSpike_GapGuardSkipsOldRow(t *testing.T) {
	srv, close := newSpikeCountServer()
	defer close()
	t.Setenv("DISCORD_WEBHOOK_URL", srv.URL)
	t.Setenv("STATS_DB", filepath.Join(t.TempDir(), "t.db"))
	db, _ := openDB()
	defer db.Close()
	// Insert an old previous row (04:15) then a newest row (06:00) so the
	// 04:15 row is the OFFSET-1 "previous" checkTrafficSpike reads.
	db.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(0,1294821013940,'2026-08-22T04:15:01Z','2026-08-22T04:15:01Z')")
	db.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(0,1313080594146,'2026-08-22T06:00:00Z','2026-08-22T06:00:00Z')")

	out := captureStdout(t, func() {
		checkTrafficSpike(db, 1313080594146, time.Date(2026, 8, 22, 6, 0, 0, 0, time.UTC))
	})
	if srv.count() != 0 {
		t.Fatalf("gap guard should skip backfill, server saw %d post(s)", srv.count())
	}
	if !strings.Contains(out, "traffic spike skipped") {
		t.Fatalf("expected skip log, got %q", out)
	}
}

func TestCheckTrafficSpike_RecentWindowFires(t *testing.T) {
	srv, close := newSpikeCountServer()
	defer close()
	t.Setenv("DISCORD_WEBHOOK_URL", srv.URL)
	t.Setenv("STATS_DB", filepath.Join(t.TempDir(), "t.db"))
	db, _ := openDB()
	defer db.Close()
	db.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(0,1000000000,'2026-08-22T05:45:00Z','2026-08-22T05:45:00Z')")
	// newest row 10 min after the previous -> recent window.
	db.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(0,19000000000,'2026-08-22T05:55:00Z','2026-08-22T05:55:00Z')")

	checkTrafficSpike(db, 19000000000, time.Date(2026, 8, 22, 5, 55, 0, 0, time.UTC))
	for i := 0; i < 50 && srv.count() == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if srv.count() != 1 {
		t.Fatalf("recent over-threshold delta should fire, server saw %d post(s)", srv.count())
	}
}

func TestCheckTrafficSpike_SubThresholdDoesNotFire(t *testing.T) {
	srv, close := newSpikeCountServer()
	defer close()
	t.Setenv("DISCORD_WEBHOOK_URL", srv.URL)
	t.Setenv("STATS_DB", filepath.Join(t.TempDir(), "t.db"))
	db, _ := openDB()
	defer db.Close()
	db.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(0,1000000000,'2026-08-22T05:45:00Z','2026-08-22T05:45:00Z')")
	db.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(0,1500000000,'2026-08-22T05:55:00Z','2026-08-22T05:55:00Z')")

	checkTrafficSpike(db, 1500000000, time.Date(2026, 8, 22, 5, 55, 0, 0, time.UTC))
	if srv.count() != 0 {
		t.Fatalf("sub-threshold delta should not fire, server saw %d post(s)", srv.count())
	}
}
