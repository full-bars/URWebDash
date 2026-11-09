package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandleWebhookStyle_GetAndSet(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("URWEBDASH_HOME", tmp)

	// Default style is embed
	req := httptest.NewRequest(http.MethodGet, "/api/webhook-style", nil)
	rec := httptest.NewRecorder()
	handleWebhookStyle(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status %d, want 200", rec.Code)
	}
	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if res["style"] != "embed" {
		t.Fatalf("expected style embed, got %v", res["style"])
	}

	// POST new style: hero-card
	body := `{"style":"hero-card"}`
	postReq := httptest.NewRequest(http.MethodPost, "/api/webhook-style", strings.NewReader(body))
	postReq.Header.Set("Content-Type", "application/json")
	postRec := httptest.NewRecorder()
	handleWebhookStyle(postRec, postReq)
	if postRec.Code != http.StatusOK {
		t.Fatalf("POST status %d, want 200: %s", postRec.Code, postRec.Body.String())
	}

	// Verify file was written
	saved, err := os.ReadFile(filepath.Join(tmp, "webhook_style"))
	if err != nil {
		t.Fatalf("failed to read webhook_style file: %v", err)
	}
	if strings.TrimSpace(string(saved)) != "hero-card" {
		t.Fatalf("expected hero-card in file, got %q", string(saved))
	}

	// GET again should return hero-card
	getReq2 := httptest.NewRequest(http.MethodGet, "/api/webhook-style", nil)
	getRec2 := httptest.NewRecorder()
	handleWebhookStyle(getRec2, getReq2)
	var res2 map[string]any
	json.Unmarshal(getRec2.Body.Bytes(), &res2)
	if res2["style"] != "hero-card" {
		t.Fatalf("expected style hero-card, got %v", res2["style"])
	}
}

func TestHandleWebhookStyle_RejectsInvalidStyle(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("URWEBDASH_HOME", tmp)

	body := `{"style":"invalid-random-style"}`
	req := httptest.NewRequest(http.MethodPost, "/api/webhook-style", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handleWebhookStyle(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("expected error status for invalid style, got %d", rec.Code)
	}
}

func TestFormatSpikeAlert_AllStylesProduceValidJSON(t *testing.T) {
	styles := []string{
		"box-table", "ascii-table", "plain-lines", "embed",
		"quote", "story", "bullets", "local-clock", "hero-card", "ansi",
	}
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	for _, s := range styles {
		out := formatSpikeAlert(s, 15*time.Minute, 1.45, 742.29, now)
		var m map[string]any
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatalf("style %q did not produce valid JSON: %v (output: %q)", s, err, out)
		}
		if _, hasEmbeds := m["embeds"]; !hasEmbeds {
			if _, hasContent := m["content"]; !hasContent {
				t.Fatalf("style %q output has neither embeds nor content: %q", s, out)
			}
		}
	}
}
