package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// handleStatus is what an uptime check and the page's footer read. It must
// always answer with a version and an uptime, and never with an empty document.
func TestHandleStatusShape(t *testing.T) {
	rec := httptest.NewRecorder()
	handleStatus(rec, httptest.NewRequest("GET", "/api/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	if _, ok := body["version"]; !ok {
		t.Errorf("no version: %s", rec.Body.String())
	}
	// would fail if the handler stopped reporting uptime, which the status
	// card renders directly
	if up, ok := body["uptime"].(string); !ok || up == "" {
		t.Errorf("no uptime: %s", rec.Body.String())
	}
}

// The embedded page is the whole UI: a wrong content type or a missing body
// leaves every browser showing a blank tab.
func TestHandleIndexServesPage(t *testing.T) {
	rec := httptest.NewRecorder()
	handleIndex(nil, "")(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content type %q", ct)
	}
	body := rec.Body.String()
	if len(body) < 1000 {
		t.Fatalf("body is %d bytes", len(body))
	}
	if !strings.Contains(body, "<html") {
		t.Errorf("body does not look like the page")
	}
}

// Any path other than "/" must 404 rather than serve the dashboard, or every
// missing asset becomes 200 with an HTML body.
func TestHandleIndexRejectsOtherPaths(t *testing.T) {
	rec := httptest.NewRecorder()
	handleIndex(nil, "")(rec, httptest.NewRequest("GET", "/nope.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", rec.Code)
	}
}

func TestHandleDashDataReturnsJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	handleDashData(nil, "")(rec, httptest.NewRequest("GET", "/api/dash-data", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content type %q", ct)
	}
	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if _, ok := res["brand"]; !ok {
		t.Errorf("expected brand in dash-data response")
	}
}

// The webhook URL comes from a file written by the dashboard when no env var is
// set. A missing file must read as empty, not as an error the caller has to
// special-case.
func TestDiscordWebhookFileURL(t *testing.T) {
	dir := t.TempDir()
	/* stateDir prefers URWEBDASH_HOME over HOME: without pinning both, this test
	   would write into a real operator's saved webhook file */
	t.Setenv("HOME", dir)
	t.Setenv("URWEBDASH_HOME", dir)
	t.Setenv("DISCORD_WEBHOOK_URL", "")

	if got := discordWebhookFileURL(); got != "" {
		t.Errorf("missing file should read empty, got %q", got)
	}

	path := discordWebhookPath()
	if err := writeFileAll(path, "https://discord.example/hook\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := discordWebhookFileURL(); got != "https://discord.example/hook" {
		t.Errorf("want trimmed url, got %q", got)
	}
}

// fetchEpochs is the only place the coordinator's epoch document is decoded.
// Point the base at a local server and check the fields the page depends on,
// including that alpha is carried as rao and that the settlement version is
// passed through.
func TestFetchEpochsParsesDocument(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("authorization header %q", got)
		}
		w.Write([]byte(`{"epochs":[
			{"epoch":4,"start_millis":1000,"end_millis":2000,"points":12.5,"share_bps":940,
			 "alpha_rao":1234500000,"settlement_version":"v2"},
			{"epoch":3,"start_millis":0,"end_millis":1000,"points":7,"share_bps":0}
		]}`))
	}))
	t.Cleanup(srv.Close)
	old := coordBaseURL
	coordBaseURL = srv.URL
	t.Cleanup(func() { coordBaseURL = old })

	rows, err := fetchEpochs("tok", 4)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if rows[0].Epoch != 4 || rows[0].Points != 12.5 || rows[0].ShareBps != 940 {
		t.Errorf("row 0: %+v", rows[0])
	}
	if rows[0].AlphaRao == nil || *rows[0].AlphaRao != 1234500000 {
		t.Errorf("alpha rao not carried: %v", rows[0].AlphaRao)
	}
	if rows[0].Settlement != "v2" {
		t.Errorf("settlement: %q", rows[0].Settlement)
	}
	// the boundary decides the label when the coordinator says nothing
	if rows[1].Settlement != "v1" {
		t.Errorf("settlement fallback: %q", rows[1].Settlement)
	}
}

// A coordinator that answers with an error must surface it, not return an empty
// list that the page reads as "no epochs".
func TestFetchEpochsSurfacesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	old := coordBaseURL
	coordBaseURL = srv.URL
	t.Cleanup(func() { coordBaseURL = old })

	if _, err := fetchEpochs("tok", 4); err == nil {
		t.Fatal("expected an error for a 401")
	}
}

// fetchSNEpoch validates what it received; a document missing the geometry must
// be rejected rather than stored as epoch zero.
func TestFetchSNEpochAcceptsAndRejects(t *testing.T) {
	cases := []struct {
		name string
		body string
		ok   bool
	}{
		{"real shape", `{"epoch":4,"start_block":100,"commit_deadline_block":5200,"finalize_block":6400,"t_epoch_blocks":4000,"netuid":25}`, true},
		{"empty object", `{}`, false},
		{"negative window", `{"epoch":4,"start_block":1000,"commit_deadline_block":900,"finalize_block":1200}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(c.body))
			}))
			defer srv.Close()
			old := coordBaseURL
			coordBaseURL = srv.URL
			defer func() { coordBaseURL = old }()

			_, err := fetchSNEpoch("tok")
			if c.ok && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if !c.ok && err == nil {
				t.Error("expected rejection")
			}
		})
	}
}

// A database written by an older release has no finalized column; opening it
// must add the column rather than failing, or the upgrade bricks the dashboard.
func TestEnsureEpochSchemaOnLegacyDatabase(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE epoch_stats (
		epoch INTEGER PRIMARY KEY, points REAL NOT NULL DEFAULT 0,
		share_bps INTEGER NOT NULL DEFAULT 0, alpha_rao INTEGER,
		settlement TEXT NOT NULL DEFAULT '', start_millis INTEGER NOT NULL DEFAULT 0,
		end_millis INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("legacy schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO epoch_stats (epoch, points, start_millis, end_millis, updated_at)
		VALUES (1, 9, ?, ?, '')`, time.Now().Add(-14*24*time.Hour).UnixMilli(), time.Now().Add(-7*24*time.Hour).UnixMilli()); err != nil {
		t.Fatalf("legacy row: %v", err)
	}

	if err := ensureEpochSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	rows, err := readEpochs(db, 5)
	if err != nil {
		t.Fatalf("read after migrate: %v", err)
	}
	// would fail if the backfill stopped marking clock-closed epochs as final:
	// a later poll could then overwrite a settled epoch's points
	if len(rows) != 1 || rows[0].Finalized != 1 {
		t.Errorf("legacy closed epoch not finalized: %+v", rows)
	}
}

// writeFileAll is a tiny helper so the test does not depend on setup.go's
// installer paths.
func writeFileAll(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}
