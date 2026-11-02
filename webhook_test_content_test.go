package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

/*
The test button must show what the channel will actually receive, which means it
has to post the format the operator picked. A canned sample cannot answer "does
this style survive Discord?", and that is the only reason the button exists.

Two payload shapes arrive from the page: the embed formats send a JSON object,
every other format sends markdown content.
*/

// sendTest posts a body to the test handler and returns what would have been
// sent to Discord.
func sendTest(t *testing.T, body string) string {
	t.Helper()
	old := sendDiscordWebhookJSON
	var got string
	sendDiscordWebhookJSON = func(url, b string) (int, error) {
		got = b
		return http.StatusNoContent, nil
	}
	t.Cleanup(func() { sendDiscordWebhookJSON = old })

	req := httptest.NewRequest(http.MethodPost, "/api/webhook-test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handleWebhookTest(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	return got
}

// An embed payload is posted as the embed it is, not stringified into content.
func TestWebhookTestPostsSelectedEmbedPayload(t *testing.T) {
	embed := `{"embeds":[{"title":"\ud83d\uded2 Hero card","description":"**+1.45 GB** in 15 min","color":6524924}]}`
	enc, err := json.Marshal(map[string]string{
		"url":     "https://discord.com/api/webhooks/1/abc",
		"content": embed,
	})
	if err != nil {
		t.Fatal(err)
	}

	got := sendTest(t, string(enc))

	var sent struct {
		Embeds []struct {
			Title       string `json:"title"`
			Description string `json:"description"`
		} `json:"embeds"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(got), &sent); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, got)
	}
	if sent.Content != "" {
		t.Fatalf("body carried content %q; an embed belongs in embeds", sent.Content)
	}
	if len(sent.Embeds) != 1 {
		t.Fatalf("sent %d embeds, want 1 (%q)", len(sent.Embeds), got)
	}
	if !strings.Contains(sent.Embeds[0].Title, "Hero card") {
		t.Fatalf("title %q, want the selected format's title", sent.Embeds[0].Title)
	}
}

// A markdown payload is posted as message content, byte for byte.
func TestWebhookTestPostsSelectedContentPayload(t *testing.T) {
	md := "\U0001f6eb **Traffic Spike**\n```\n+------------------+------------+\n| 15m Delta        |  +1.45 GB  |\n+------------------+------------+\n```"
	enc, err := json.Marshal(map[string]string{
		"url":     "https://discord.com/api/webhooks/1/abc",
		"content": md,
	})
	if err != nil {
		t.Fatal(err)
	}

	got := sendTest(t, string(enc))

	var sent struct {
		Content string            `json:"content"`
		Embeds  []json.RawMessage `json:"embeds"`
	}
	if err := json.Unmarshal([]byte(got), &sent); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, got)
	}
	if sent.Content != md {
		t.Fatalf("content %q, want the selected format's payload %q", sent.Content, md)
	}
	if len(sent.Embeds) != 0 {
		t.Fatalf("sent %d embeds, want none", len(sent.Embeds))
	}
}

// ANSI colour codes are stripped: Discord prints them literally, so a preview
// carrying terminal escapes would land as garbage.
func TestWebhookTestStripsAnsiFromPreview(t *testing.T) {
	enc, err := json.Marshal(map[string]string{
		"url":     "https://discord.com/api/webhooks/1/abc",
		"content": "\x1b[2;37m ANSI PREVIEW \x1b[0m",
	})
	if err != nil {
		t.Fatal(err)
	}

	got := sendTest(t, string(enc))

	if strings.Contains(got, "\x1b[") {
		t.Fatalf("sent %q, which carries ANSI escapes", got)
	}
	if !strings.Contains(got, "ANSI PREVIEW") {
		t.Fatalf("sent %q, want the readable text preserved", got)
	}
}

// With no payload the handler still posts a real alert, so the button proves
// delivery even from a page that sent nothing.
func TestWebhookTestFallsBackToSampleEmbed(t *testing.T) {
	enc, err := json.Marshal(map[string]string{
		"url": "https://discord.com/api/webhooks/1/abc",
	})
	if err != nil {
		t.Fatal(err)
	}

	got := sendTest(t, string(enc))

	var sent struct {
		Embeds []struct {
			Title  string `json:"title"`
			Fields []struct {
				Name string `json:"name"`
			} `json:"fields"`
			Footer struct {
				Text string `json:"text"`
			} `json:"footer"`
		} `json:"embeds"`
	}
	if err := json.Unmarshal([]byte(got), &sent); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, got)
	}
	if len(sent.Embeds) != 1 {
		t.Fatalf("sent %d embeds, want 1", len(sent.Embeds))
	}
	if !strings.Contains(sent.Embeds[0].Title, "Traffic Spike") {
		t.Fatalf("title %q, want the traffic-spike alert", sent.Embeds[0].Title)
	}
	if len(sent.Embeds[0].Fields) != 3 {
		t.Fatalf("sent %d fields, want 3", len(sent.Embeds[0].Fields))
	}
	if !strings.Contains(sent.Embeds[0].Footer.Text, "spike threshold") {
		t.Fatalf("footer %q, want the threshold", sent.Embeds[0].Footer.Text)
	}
}
