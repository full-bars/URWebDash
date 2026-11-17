package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestUpdateVersions(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want int
		ok   bool
	}{
		{"v0.0.9", "v0.0.10", -1, true}, {"v1.2.3", "v1.2.3", 0, true}, {"v2.0.0", "v1.99.99", 1, true}, {"dev", "v1.0.0", 0, false}, {"v1.0.0", "junk", 0, false},
	} {
		a, ao := parseUpdateVersion(tt.a)
		b, bo := parseUpdateVersion(tt.b)
		if (ao && bo) != tt.ok {
			t.Errorf("validity %q %q", tt.a, tt.b)
			continue
		}
		if ao && bo && compareUpdateVersion(a, b) != tt.want {
			t.Errorf("compare %q %q", tt.a, tt.b)
		}
	}
}
func TestUpdateResponse(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		status     int
		ok         bool
	}{
		{"good", `{"tag_name":"v0.0.26","html_url":"https://github.com/full-bars/URWebDash/releases/tag/v0.0.26"}`, 200, true},
		{"tag", `{"tag_name":"v1.0.0-rc","html_url":"https://github.com/full-bars/URWebDash/releases/tag/v1"}`, 200, false},
		{"host", `{"tag_name":"v1.0.0","html_url":"https://evil.example/releases/tag/v1"}`, 200, false},
		{"large", strings.Repeat(" ", 1048577), 200, false}, {"json", "{", 200, false}, {"status", "{}", 500, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("User-Agent") != "urwebdash" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("headers")
				}
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer s.Close()
			_, err := fetchUpdate(s.Client(), s.URL)
			if (err == nil) != tt.ok {
				t.Fatalf("error: %v", err)
			}
		})
	}
}
func TestUpdateTimeout(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer s.Close()
	client := s.Client()
	client.Timeout = 30 * time.Millisecond
	if _, err := fetchUpdate(client, s.URL); err == nil {
		t.Fatal("expected timeout")
	}
}
func TestUpdateCacheRetryAndOptOut(t *testing.T) {
	var n atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) > 1 {
			w.WriteHeader(500)
			return
		}
		fmt.Fprint(w, `{"tag_name":"v0.0.26","html_url":"https://github.com/full-bars/URWebDash/releases/tag/v0.0.26"}`)
	}))
	defer s.Close()
	c := newUpdateChecker("v0.0.25", true, s.URL)
	if err := c.check(); err != nil {
		t.Fatal(err)
	}
	before := c.snapshot()
	if err := c.check(); err == nil {
		t.Fatal("expected failure")
	}
	if c.snapshot() != before {
		t.Fatal("lost good answer")
	}
	for i, want := range []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute, time.Hour, time.Hour} {
		if got := updateBackoff(i); got != want {
			t.Errorf("backoff %d: %s", i, got)
		}
	}
	for _, value := range []string{"off", "OFF", "0", "False", "NO"} {
		t.Setenv("UPDATE_CHECK", value)
		d := newUpdateChecker("v0.0.25", updateEnabled(), s.URL)
		d.start()
		_ = d.check()
	}
	dev := newUpdateChecker("dev", true, s.URL)
	dev.start()
	_ = dev.check()
	if n.Load() != 2 {
		t.Fatal("disabled or dev made request")
	}
}
func TestUpdateURLOverride(t *testing.T) {
	for _, tt := range []struct {
		url string
		ok  bool
	}{{"http://localhost:123/a", true}, {"http://127.0.0.1:3/a", true}, {"http://localhost.evil/a", false}, {"http://127.0.0.1.evil/a", false}, {"https://localhost/a", false}, {"http://example.com", false}} {
		if validUpdateTestURL(tt.url) != tt.ok {
			t.Errorf("%s", tt.url)
		}
		t.Setenv("UPDATE_CHECK_URL", tt.url)
		want := updateAPIURL
		if tt.ok {
			want = tt.url
		}
		if got := updateURL(); got != want {
			t.Errorf("override %q: got %q", tt.url, got)
		}
	}
}
func TestUpdateHandler(t *testing.T) {
	for _, tt := range []struct {
		name, version      string
		enabled, available bool
		latest             string
	}{{"available", "v0.0.25", true, true, "v0.0.26"}, {"current", "v0.0.26", true, false, "v0.0.26"}, {"disabled", "v0.0.25", false, false, ""}, {"dev", "dev", true, false, ""}} {
		t.Run(tt.name, func(t *testing.T) {
			c := newUpdateChecker(tt.version, tt.enabled, "")
			if tt.latest != "" {
				c.result.Latest = tt.latest
				c.result.Available = tt.available
			}
			rr := httptest.NewRecorder()
			c.handle(rr, httptest.NewRequest("GET", "/api/update", nil))
			var got updateResult
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || got.Enabled != tt.enabled || got.Current != tt.version || got.Available != tt.available || got.Latest != tt.latest {
				t.Fatalf("%s %v", rr.Body.String(), err)
			}
			rr = httptest.NewRecorder()
			c.handle(rr, httptest.NewRequest("POST", "/api/update", nil))
			if rr.Code != 405 {
				t.Fatal(rr.Code)
			}
		})
	}
}
func TestIndexHTML_UpdateBadge(t *testing.T) {
	for _, s := range []string{`id="update-badge"`, `id="update-dismiss"`, `textContent`, `setAttribute`, `urwebdash.update.dismissed`, `/api/update`, `noopener noreferrer`} {
		if !strings.Contains(indexHTML, s) {
			t.Errorf("missing %s", s)
		}
	}
	code := sliceBetween(t, indexHTML, "function updateShouldShow(", "/* ---- theme set ---- */")
	for _, marker := range []string{"link.textContent=data.latest", "link.setAttribute('href',data.url)", "localStorage.setItem('urwebdash.update.dismissed'", "catch(_)", "update-dismiss", "badge.hidden=true"} {
		if !strings.Contains(code, marker) {
			t.Errorf("missing update logic %q", marker)
		}
	}
	if strings.Contains(code, "innerHTML") {
		t.Error("update logic must not use innerHTML")
	}
}
func TestIndexHTML_UpdateLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	code := funcBody(t, indexHTML, "function updateShouldShow(")
	runNode(t, node, code+`if(!updateShouldShow({available:true,latest:'v0.0.10'},'v0.0.9')||updateShouldShow({available:true,latest:'v0.0.9'},'v0.0.10')||updateShouldShow({available:false},'')) throw Error('badge logic');`)
}
