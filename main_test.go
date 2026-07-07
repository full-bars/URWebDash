package main

import (
	"encoding/json"
	"fmt"
	"net/http"
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
	if pts != 12.0 {
		t.Fatalf("points = %v, want 12.0 (12M / 1e6)", pts)
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
		Success bool  `json:"success"`
		Count   int   `json:"count"`
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

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
