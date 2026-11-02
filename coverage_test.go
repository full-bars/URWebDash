package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// ----------------------------------------------------------------------------
// 1. webhookCSRFCheck (main.go)
// ----------------------------------------------------------------------------

func TestWebhookCSRFCheck_MissingContentType(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/webhook", nil)
	rec := httptest.NewRecorder()

	if webhookCSRFCheck(rec, req) {
		t.Fatal("webhookCSRFCheck should reject request with missing Content-Type")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse response JSON: %v", err)
	}
	if body["error"] != "Content-Type must be application/json" {
		t.Fatalf("error = %q, want 'Content-Type must be application/json'", body["error"])
	}
}

func TestWebhookCSRFCheck_TextPlainRejected(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/webhook", bytes.NewBufferString("hello"))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()

	if webhookCSRFCheck(rec, req) {
		t.Fatal("webhookCSRFCheck should reject text/plain Content-Type")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse response JSON: %v", err)
	}
	if body["error"] != "Content-Type must be application/json" {
		t.Fatalf("error = %q, want 'Content-Type must be application/json'", body["error"])
	}
}

func TestWebhookCSRFCheck_ApplicationJSONAccepted(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/webhook", bytes.NewBufferString(`{"url":"https://discord.com/webhook"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	if !webhookCSRFCheck(rec, req) {
		t.Fatal("webhookCSRFCheck should accept application/json Content-Type")
	}
	if rec.Code == http.StatusForbidden {
		t.Fatalf("status = %d, unexpectedly forbidden", rec.Code)
	}
}

// Would fail if: webhookCSRFCheck failed to compare Origin with Host (e.g. omitted u.Host != r.Host or allowed
// arbitrary cross-origin requests), leaving the webhook endpoint vulnerable to CSRF.
func TestWebhookCSRFCheck_ForeignOriginRejected(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/webhook", bytes.NewBufferString(`{}`))
	req.Host = "localhost:3001"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://evil-attacker.example.com")
	rec := httptest.NewRecorder()

	if webhookCSRFCheck(rec, req) {
		t.Fatal("webhookCSRFCheck should reject foreign Origin")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse response JSON: %v", err)
	}
	if body["error"] != "cross-origin request rejected" {
		t.Fatalf("error = %q, want 'cross-origin request rejected'", body["error"])
	}
}

func TestWebhookCSRFCheck_SameHostOriginAccepted(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/webhook", bytes.NewBufferString(`{}`))
	req.Host = "localhost:3001"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://localhost:3001")
	rec := httptest.NewRecorder()

	if !webhookCSRFCheck(rec, req) {
		t.Fatal("webhookCSRFCheck should accept same-host Origin")
	}
	if rec.Code == http.StatusForbidden {
		t.Fatalf("status = %d, unexpectedly forbidden", rec.Code)
	}
}

func TestWebhookCSRFCheck_NoOriginAccepted(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/webhook", bytes.NewBufferString(`{}`))
	req.Host = "localhost:3001"
	req.Header.Set("Content-Type", "application/json")
	// No Origin header (curl / scripts)
	rec := httptest.NewRecorder()

	if !webhookCSRFCheck(rec, req) {
		t.Fatal("webhookCSRFCheck should accept requests with no Origin header")
	}
	if rec.Code == http.StatusForbidden {
		t.Fatalf("status = %d, unexpectedly forbidden", rec.Code)
	}
}

// ----------------------------------------------------------------------------
// 2. pollWindow and statsInterval (main.go)
// ----------------------------------------------------------------------------

func TestPollWindow_ExactBoundary(t *testing.T) {
	interval := 15 * time.Minute
	boundary := time.Date(2026, 11, 1, 14, 30, 0, 0, time.UTC)
	start, end := pollWindow(boundary, interval)

	if !start.Equal(boundary) {
		t.Fatalf("pollWindow(%v) start = %v, want %v", boundary, start, boundary)
	}
	wantEnd := boundary.Add(interval)
	if !end.Equal(wantEnd) {
		t.Fatalf("pollWindow(%v) end = %v, want %v", boundary, end, wantEnd)
	}
}

// Would fail if: pollWindow used ceiling or round-to-nearest instead of Truncate, causing samples
// immediately before a boundary to map to the upcoming window rather than the previous window.
func TestPollWindow_JustBeforeBoundary(t *testing.T) {
	interval := 15 * time.Minute
	boundary := time.Date(2026, 11, 1, 14, 30, 0, 0, time.UTC)
	justBefore := boundary.Add(-1 * time.Second) // 14:29:59
	start, end := pollWindow(justBefore, interval)

	wantStart := boundary.Add(-interval) // 14:15:00
	if !start.Equal(wantStart) {
		t.Fatalf("pollWindow(%v) start = %v, want %v", justBefore, start, wantStart)
	}
	if !end.Equal(boundary) {
		t.Fatalf("pollWindow(%v) end = %v, want %v", justBefore, end, boundary)
	}
}

func TestPollWindow_IntervalShorterAndLongerThanOffset(t *testing.T) {
	// Base is 14:00:00 UTC, target is 14:12:00 UTC (12 min offset from hour boundary).
	target := time.Date(2026, 11, 1, 14, 12, 0, 0, time.UTC)

	// Shorter interval than the 12m offset: 5m.
	// 14:12:00 must land in [14:10:00, 14:15:00).
	shortInterval := 5 * time.Minute
	sStart, sEnd := pollWindow(target, shortInterval)
	wantSStart := time.Date(2026, 11, 1, 14, 10, 0, 0, time.UTC)
	wantSEnd := time.Date(2026, 11, 1, 14, 15, 0, 0, time.UTC)
	if !sStart.Equal(wantSStart) || !sEnd.Equal(wantSEnd) {
		t.Fatalf("pollWindow(%v, 5m) = [%v, %v), want [%v, %v)", target, sStart, sEnd, wantSStart, wantSEnd)
	}

	// Longer interval than the 12m offset: 30m.
	// 14:12:00 must land in [14:00:00, 14:30:00).
	longInterval := 30 * time.Minute
	lStart, lEnd := pollWindow(target, longInterval)
	wantLStart := time.Date(2026, 11, 1, 14, 0, 0, 0, time.UTC)
	wantLEnd := time.Date(2026, 11, 1, 14, 30, 0, 0, time.UTC)
	if !lStart.Equal(wantLStart) || !lEnd.Equal(wantLEnd) {
		t.Fatalf("pollWindow(%v, 30m) = [%v, %v), want [%v, %v)", target, lStart, lEnd, wantLStart, wantLEnd)
	}
}

func TestStatsInterval_Default(t *testing.T) {
	t.Setenv("STATS_INTERVAL", "")
	if got := statsInterval(); got != 15*time.Minute {
		t.Fatalf("statsInterval() = %v, want default 15m", got)
	}
}

func TestStatsInterval_ValidOverride(t *testing.T) {
	t.Setenv("STATS_INTERVAL", "5m")
	if got := statsInterval(); got != 5*time.Minute {
		t.Fatalf("statsInterval() = %v, want 5m", got)
	}
}

func TestStatsInterval_TooSmallIgnored(t *testing.T) {
	for _, val := range []string{"30s", "0s", "59s", "-1m"} {
		t.Setenv("STATS_INTERVAL", val)
		if got := statsInterval(); got != 15*time.Minute {
			t.Fatalf("statsInterval() with %q = %v, want default 15m", val, got)
		}
	}
}

func TestStatsInterval_GarbageIgnored(t *testing.T) {
	for _, val := range []string{"invalid", "not-a-duration", "15minutes", "foo"} {
		t.Setenv("STATS_INTERVAL", val)
		if got := statsInterval(); got != 15*time.Minute {
			t.Fatalf("statsInterval() with %q = %v, want default 15m", val, got)
		}
	}
}

// ----------------------------------------------------------------------------
// 3. cleanupDB (main.go)
// ----------------------------------------------------------------------------

// Would fail if: cleanupDB deleted all rows indiscriminately, omitted the modulo interval check (% ivSec != 0),
// or failed to constrain deletion to today's date boundary.
func TestCleanupDB_RemovesOffScheduleTodayKeepsYesterday(t *testing.T) {
	t.Setenv("STATS_DB", filepath.Join(t.TempDir(), "cleanup_test.db"))
	t.Setenv("STATS_INTERVAL", "") // default 15m -> 900s interval
	db, err := openDB()
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")

	// Today on-schedule row: 04:30:00 UTC -> epoch seconds % 900 == 0
	onScheduleToday := today + "T04:30:00Z"
	// Today off-schedule row: 04:30:01 UTC -> epoch seconds % 900 == 1 != 0
	offScheduleToday := today + "T04:30:01Z"
	// Yesterday row: 04:30:01 UTC (off-schedule seconds, but from yesterday -> must survive)
	yesterdayRow := yesterday + "T04:30:01Z"

	insertRow := func(ts string) {
		if _, err := db.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(100, 200, ?, ?)", ts, ts); err != nil {
			t.Fatalf("insertRow(%s): %v", ts, err)
		}
	}

	insertRow(onScheduleToday)
	insertRow(offScheduleToday)
	insertRow(yesterdayRow)

	captureStdout(t, cleanupDB)

	rows, err := db.Query("SELECT created_at FROM wallet_stats ORDER BY created_at")
	if err != nil {
		t.Fatalf("query wallet_stats: %v", err)
	}
	defer rows.Close()

	remains := make(map[string]bool)
	for rows.Next() {
		var ts string
		if err := rows.Scan(&ts); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		remains[ts] = true
	}

	if !remains[onScheduleToday] {
		t.Errorf("cleanupDB unexpectedly deleted on-schedule row for today: %s", onScheduleToday)
	}
	if remains[offScheduleToday] {
		t.Errorf("cleanupDB kept off-schedule row for today: %s", offScheduleToday)
	}
	if !remains[yesterdayRow] {
		t.Errorf("cleanupDB unexpectedly deleted row from yesterday: %s", yesterdayRow)
	}
	if len(remains) != 2 {
		t.Fatalf("remaining row count = %d, want 2", len(remains))
	}
}

// ----------------------------------------------------------------------------
// 4. importJSON (main.go)
// ----------------------------------------------------------------------------

func TestImportJSON_ValidExport(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("STATS_DB", filepath.Join(tmp, "test_import.db"))

	records := []exportRecord{
		{
			UserID:            "user-1",
			NetworkName:       "net-alpha",
			PaidBytesProvided: 100000,
			UnpaidBytes:       50000,
			CreatedAt:         "2026-11-01T10:00:00Z",
			UpdatedAt:         "2026-11-01T10:00:00Z",
		},
		{
			UserID:            "user-1",
			NetworkName:       "net-alpha",
			PaidBytesProvided: 200000,
			UnpaidBytes:       75000,
			CreatedAt:         "2026-11-01T10:15:00Z",
			UpdatedAt:         "2026-11-01T10:15:00Z",
		},
	}
	data, err := json.Marshal(records)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	jsonFile := filepath.Join(tmp, "export.json")
	if err := os.WriteFile(jsonFile, data, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	captureStdout(t, func() {
		importJSON(jsonFile)
	})

	db, err := openDB()
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM wallet_stats").Scan(&count); err != nil {
		t.Fatalf("query count: %v", err)
	}
	if count != 2 {
		t.Fatalf("imported count = %d, want 2", count)
	}

	var paid, unpaid int64
	var net string
	if err := db.QueryRow("SELECT paid_bytes, unpaid_bytes, network_name FROM wallet_stats WHERE created_at = '2026-11-01T10:00:00Z'").Scan(&paid, &unpaid, &net); err != nil {
		t.Fatalf("scan row: %v", err)
	}
	if paid != 100000 || unpaid != 50000 || net != "net-alpha" {
		t.Fatalf("row mismatch: paid=%d, unpaid=%d, net=%q", paid, unpaid, net)
	}
}

func TestImportJSON_MissingFile(t *testing.T) {
	if os.Getenv("BE_CHILD_IMPORT_MISSING") == "1" {
		childDB := filepath.Join(os.TempDir(), fmt.Sprintf("child_missing_%d.db", os.Getpid()))
		os.Setenv("STATS_DB", childDB)
		defer os.Remove(childDB)
		importJSON(filepath.Join(os.TempDir(), fmt.Sprintf("nonexistent_%d.json", os.Getpid())))
		t.Fatal("importJSON should have exited")
		return
	}

	t.Setenv("STATS_DB", filepath.Join(t.TempDir(), "parent.db"))
	cmd := exec.Command(os.Args[0], "-test.run=^TestImportJSON_MissingFile$")
	cmd.Env = append(os.Environ(), "BE_CHILD_IMPORT_MISSING=1")
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit code 1, got err=%v, out=%s", err, string(out))
	}
	if bytes.Contains(out, []byte("panic:")) {
		t.Fatalf("unexpected panic in output: %s", string(out))
	}
	if !bytes.Contains(out, []byte("read:")) {
		t.Fatalf("expected error containing 'read:', got: %s", string(out))
	}
}

func TestImportJSON_MalformedJSON(t *testing.T) {
	if os.Getenv("BE_CHILD_IMPORT_MALFORMED") == "1" {
		badFile := filepath.Join(os.TempDir(), fmt.Sprintf("malformed_%d.json", os.Getpid()))
		if err := os.WriteFile(badFile, []byte("{not-valid-json"), 0644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(badFile)
		childDB := filepath.Join(os.TempDir(), fmt.Sprintf("child_malformed_%d.db", os.Getpid()))
		os.Setenv("STATS_DB", childDB)
		defer os.Remove(childDB)
		importJSON(badFile)
		t.Fatal("importJSON should have exited")
		return
	}

	t.Setenv("STATS_DB", filepath.Join(t.TempDir(), "parent.db"))
	cmd := exec.Command(os.Args[0], "-test.run=^TestImportJSON_MalformedJSON$")
	cmd.Env = append(os.Environ(), "BE_CHILD_IMPORT_MALFORMED=1")
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit code 1, got err=%v, out=%s", err, string(out))
	}
	if bytes.Contains(out, []byte("panic:")) {
		t.Fatalf("unexpected panic in output: %s", string(out))
	}
	if !bytes.Contains(out, []byte("parse:")) {
		t.Fatalf("expected error containing 'parse:', got: %s", string(out))
	}
}

// ----------------------------------------------------------------------------
// 5. networkNameFromJWT (main.go)
// ----------------------------------------------------------------------------

func TestNetworkNameFromJWT_ValidToken(t *testing.T) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"network_name":"uranium-mainnet","user_id":"u-123"}`))
	sig := base64.RawURLEncoding.EncodeToString([]byte("signature-data"))
	token := header + "." + payload + "." + sig

	got := networkNameFromJWT(token)
	if got != "uranium-mainnet" {
		t.Fatalf("networkNameFromJWT() = %q, want 'uranium-mainnet'", got)
	}
}

func TestNetworkNameFromJWT_MalformedTokens(t *testing.T) {
	cases := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"no dot", "single-segment-token"},
		{"invalid base64 payload", "header.not_base64_%%%!.sig"},
		{"non-json payload", "header." + base64.RawURLEncoding.EncodeToString([]byte("plain text not json")) + ".sig"},
		{"missing network_name claim", "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"other_field":"abc"}`)) + ".sig"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := networkNameFromJWT(tc.token)
			if got != "" {
				t.Fatalf("networkNameFromJWT(%q) = %q, want empty string fallback", tc.token, got)
			}
		})
	}
}

// ----------------------------------------------------------------------------
// 6. firstV2Epoch (epochs.go)
// ----------------------------------------------------------------------------

func TestFirstV2Epoch_Default(t *testing.T) {
	t.Setenv("EPOCH_FIRST_V2", "")
	if got := firstV2Epoch(); got != 4 {
		t.Fatalf("firstV2Epoch() = %d, want default 4", got)
	}
}

func TestFirstV2Epoch_Override(t *testing.T) {
	t.Setenv("EPOCH_FIRST_V2", "7")
	if got := firstV2Epoch(); got != 7 {
		t.Fatalf("firstV2Epoch() = %d, want 7", got)
	}
}

// Would fail if: ParseInt failure caused firstV2Epoch to return 0 (unhandled error default)
// instead of falling back to the documented default (4).
func TestFirstV2Epoch_GarbageFallback(t *testing.T) {
	for _, val := range []string{"garbage", "abc", "7xyz", "12.34", "-"} {
		t.Setenv("EPOCH_FIRST_V2", val)
		if got := firstV2Epoch(); got != 4 {
			t.Fatalf("firstV2Epoch() with %q = %d, want default 4", val, got)
		}
	}
}
