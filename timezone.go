package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	_ "time/tzdata" // zone data inside the binary, so any zone name loads on any host
)

// Alerts are text, and Discord does not localize text inside a code block, so
// the server has to write the time itself. The page reports the browser's
// timezone once and the server keeps it in the state directory. With none
// saved, the host's own zone is used.

func timezonePath() string { return filepath.Join(stateDir(), "timezone") }

// displayLocation is the zone alerts are written in.
func displayLocation() *time.Location {
	if b, err := os.ReadFile(timezonePath()); err == nil {
		if loc, err := time.LoadLocation(strings.TrimSpace(string(b))); err == nil {
			return loc
		}
	}
	return time.Local
}

// alertClock writes a time the way alerts show it: the local clock and the
// zone's short name, such as "11:51 PST".
func alertClock(t time.Time) string { return t.In(displayLocation()).Format("15:04 MST") }

// handleTimezone: GET returns the zone in use, POST saves one the page sends.
func handleTimezone(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		json.NewEncoder(w).Encode(map[string]string{"timezone": displayLocation().String()})
	case http.MethodPost:
		if !webhookCSRFCheck(w, r) {
			return
		}
		var req struct {
			Timezone string `json:"timezone"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req); err != nil {
			jsonError(w, "bad request")
			return
		}
		name := strings.TrimSpace(req.Timezone)
		if _, err := time.LoadLocation(name); err != nil || name == "" || name == "Local" {
			jsonError(w, "unknown timezone")
			return
		}
		if err := os.MkdirAll(stateDir(), 0700); err != nil {
			jsonError(w, "could not save")
			return
		}
		if err := os.WriteFile(timezonePath(), []byte(name+"\n"), 0600); err != nil {
			jsonError(w, "could not save")
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"timezone": name})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
