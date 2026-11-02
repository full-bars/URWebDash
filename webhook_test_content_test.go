package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

/*
The test button must deliver the style the channel will actually receive.

	A canned line made the test look nothing like the previews.
*/
func TestWebhookTestSendsSelectedFormat(t *testing.T) {
	old := sendDiscordWebhook
	var got string
	sendDiscordWebhook = func(url, content string) (int, error) {
		got = content
		return http.StatusNoContent, nil
	}
	defer func() { sendDiscordWebhook = old }()

	body := "{\"url\":\"https://discord.com/api/webhooks/1/abc\",\"content\":\"**Traffic Spike** ASCII TABLE\"}"
	req := httptest.NewRequest(http.MethodPost, "/api/webhook-test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handleWebhookTest(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(got, "Traffic Spike") || !strings.Contains(got, "ASCII TABLE") {
		t.Fatalf("sent %q, want the supplied payload", got)
	}
}

/* With no content the handler still has to say something useful. */
func TestWebhookTestFallsBackToCannedLine(t *testing.T) {
	old := sendDiscordWebhook
	var got string
	sendDiscordWebhook = func(url, content string) (int, error) {
		got = content
		return http.StatusNoContent, nil
	}
	defer func() { sendDiscordWebhook = old }()

	body := `{"url":"https://discord.com/api/webhooks/1/abc"}`
	req := httptest.NewRequest(http.MethodPost, "/api/webhook-test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handleWebhookTest(rec, req)

	if !strings.Contains(got, "test notification") {
		t.Fatalf("sent %q, want the fallback line", got)
	}
}
