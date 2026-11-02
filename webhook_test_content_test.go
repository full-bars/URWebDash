package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

/*
The test button must show what the channel will actually receive.

The page's style previews are design studies: one is an embed, and posting an
embed as message content prints the JSON into the channel. So the handler builds
its own sample and posts it as a request body.
*/
func TestWebhookTestPostsAnEmbedBody(t *testing.T) {
	old := sendDiscordWebhookJSON
	var got string
	sendDiscordWebhookJSON = func(url, body string) (int, error) {
		got = body
		return http.StatusNoContent, nil
	}
	defer func() { sendDiscordWebhookJSON = old }()

	body := `{"url":"https://discord.com/api/webhooks/1/abc","content":"{\"embeds\":[]}"}`
	req := httptest.NewRequest(http.MethodPost, "/api/webhook-test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handleWebhookTest(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

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
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(got), &sent); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, got)
	}
	if sent.Content != "" {
		t.Fatalf("body carried content %q; an embed belongs in embeds", sent.Content)
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

/* Whatever the preview posted must not leak into the message. */
func TestWebhookTestIgnoresPreviewPayload(t *testing.T) {
	old := sendDiscordWebhookJSON
	var got string
	sendDiscordWebhookJSON = func(url, body string) (int, error) {
		got = body
		return http.StatusNoContent, nil
	}
	defer func() { sendDiscordWebhookJSON = old }()

	body := `{"url":"https://discord.com/api/webhooks/1/abc","content":"\u001b[2;37m ANSI PREVIEW"}`
	req := httptest.NewRequest(http.MethodPost, "/api/webhook-test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handleWebhookTest(rec, req)

	if strings.Contains(got, "ANSI PREVIEW") {
		t.Fatalf("sent %q, which carries the preview payload", got)
	}
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("sent %q, which carries ANSI escapes", got)
	}
	if !strings.Contains(got, "embeds") {
		t.Fatalf("sent %q, want an embed body", got)
	}
}
