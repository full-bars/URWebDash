package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// updateAPIURL can be replaced by tests.
var updateAPIURL = "https://api.github.com/repos/full-bars/URWebDash/releases/latest"
var updateVersionPattern = regexp.MustCompile(`^v([0-9]+)\.([0-9]+)\.([0-9]+)$`)

const updateReleasePrefix = "https://github.com/full-bars/URWebDash/releases/"

type updateVersion [3]*big.Int

func parseUpdateVersion(s string) (updateVersion, bool) {
	var v updateVersion
	parts := updateVersionPattern.FindStringSubmatch(s)
	if parts == nil {
		return v, false
	}
	for i := range v {
		v[i], _ = new(big.Int).SetString(parts[i+1], 10)
	}
	return v, true
}
func compareUpdateVersion(a, b updateVersion) int {
	for i := range a {
		if n := a[i].Cmp(b[i]); n != 0 {
			return n
		}
	}
	return 0
}

type updateResult struct {
	Enabled   bool   `json:"enabled"`
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"available"`
	URL       string `json:"url"`
	CheckedAt string `json:"checkedAt"`
}
type updateRelease struct {
	Tag string `json:"tag_name"`
	URL string `json:"html_url"`
}

func validUpdateTestURL(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && u.Scheme == "http" && u.User == nil && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1")
}
func updateEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("UPDATE_CHECK"))) {
	case "off", "0", "false", "no":
		return false
	}
	return true
}
func updateURL() string { // UPDATE_CHECK_URL is a localhost-only test override.
	if v := os.Getenv("UPDATE_CHECK_URL"); v != "" && validUpdateTestURL(v) {
		return v
	}
	return updateAPIURL
}
func fetchUpdate(client *http.Client, endpoint string) (updateRelease, error) {
	var release updateRelease
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return release, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "urwebdash")
	resp, err := client.Do(req)
	if err != nil {
		return release, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return release, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1048577))
	if err != nil {
		return release, err
	}
	if len(body) > 1048576 {
		return release, errors.New("response too large")
	}
	if err = json.Unmarshal(body, &release); err != nil {
		return release, errors.New("invalid JSON")
	}
	if _, ok := parseUpdateVersion(release.Tag); !ok || !strings.HasPrefix(release.URL, updateReleasePrefix) {
		return release, errors.New("invalid release")
	}
	return release, nil
}

type updateChecker struct {
	mu       sync.RWMutex
	result   updateResult
	endpoint string
	client   *http.Client
}

func newUpdateChecker(version string, enabled bool, endpoint string) *updateChecker {
	return &updateChecker{result: updateResult{Enabled: enabled, Current: version}, endpoint: endpoint, client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *updateChecker) snapshot() updateResult { c.mu.RLock(); defer c.mu.RUnlock(); return c.result }
func (c *updateChecker) check() error {
	old := c.snapshot()
	current, ok := parseUpdateVersion(old.Current)
	if !old.Enabled || !ok {
		return nil
	}
	release, err := fetchUpdate(c.client, c.endpoint)
	if err != nil {
		return err
	}
	latest, _ := parseUpdateVersion(release.Tag)
	c.mu.Lock()
	c.result.Latest = release.Tag
	c.result.URL = release.URL
	c.result.Available = compareUpdateVersion(latest, current) > 0
	c.result.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	c.mu.Unlock()
	return nil
}
func updateBackoff(n int) time.Duration {
	if n >= 4 {
		return time.Hour
	}
	return 5 * time.Minute * time.Duration(1<<n)
}
func (c *updateChecker) start() {
	if !c.result.Enabled {
		return
	}
	if _, ok := parseUpdateVersion(c.result.Current); !ok {
		return
	}
	go func() {
		time.Sleep(30 * time.Second)
		failures := 0
		for {
			err := c.check()
			var delay time.Duration
			if err != nil {
				if failures == 0 {
					fmt.Println("[update] release check failed")
				}
				delay = updateBackoff(failures)
				failures++
			} else {
				failures = 0
				delay = 6*time.Hour + time.Duration(rand.Int63n(int64(10*time.Minute)+1))
			}
			time.Sleep(delay)
		}
	}()
}
func (c *updateChecker) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(c.snapshot())
}
