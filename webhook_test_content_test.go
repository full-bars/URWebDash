package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

/*
The test button must show the alert the channel will actually receive.

The page's style previews are design studies with placeholder text, and the ANSI
one posts raw escape bytes that Discord renders as gibberish. So the handler sends
its own sample built from the real template, and ignores whatever the preview
would have posted.
*/
func TestWebhookTestSendsTheRealAlert(t *testing.T) {
	old := sendDiscordWebhook
	var got string
	sendDiscordWebhook = func(url, content string) (int, error) {
		got = content
		return http.StatusNoContent, nil
	}
	defer func() { sendDiscordWebhook = old }()

	body := `{"url":"https://discord.com/api/webhooks/1/abc","content":"**Traffic Spike** ANSI PREVIEW PAYLOAD"}`
	req := httptest.NewRequest(http.MethodPost, "/api/webhook-test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handleWebhookTest(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(got, "Traffic Spike") {
		t.Fatalf("sent %q, want the traffic-spike alert", got)
	}
	if !strings.Contains(got, "Total Unpaid") || !strings.Contains(got, "At (UTC)") {
		t.Fatalf("sent %q, want the real alert rows", got)
	}
	/* the preview payload must not survive into the message */
	if strings.Contains(got, "ANSI PREVIEW PAYLOAD") {
		t.Fatalf("sent %q, which still carries the preview payload", got)
	}
}

/* A caller that sends nothing still gets the same real alert, not a canned line. */
func TestWebhookTestWithoutPayloadStillSendsTheRealAlert(t *testing.T) {
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

	if !strings.Contains(got, "Traffic Spike") {
		t.Fatalf("sent %q, want the traffic-spike alert", got)
	}
	/* no raw ANSI escape bytes: Discord would print them literally */
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("sent %q, which carries ANSI escapes", got)
	}
}
