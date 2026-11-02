package main

import (
	"os"
	"path/filepath"
	"testing"
)

/*
The prime runs at startup and re-reads the whole payment list. Announcing from

	it turned every restart into a burst of alerts for payments already known, so
	it must seed silently and must not touch the store at all for fixture data.
*/
func TestPrimeDoesNotNotify(t *testing.T) {
	dir := t.TempDir()

	oldSend := notifySend
	var sent []string
	notifySend = func(content string) { sent = append(sent, content) }
	defer func() { notifySend = oldSend }()

	fixture := filepath.Join(dir, "payouts.json")
	body := `{"account_payments":[{"payment_id":"p1","tx_hash":"hash-one","completed":true,"token_amount":13.25}]}`
	if err := os.WriteFile(fixture, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PAYOUT_FIXTURE_FILE", fixture)

	notifyStoreMu.Lock()
	notifyStore = map[string]notifyRecord{}
	notifyStoreSeeded = false
	notifyStoreMu.Unlock()
	t.Cleanup(func() {
		notifyStoreMu.Lock()
		notifyStore = nil
		notifyStoreSeeded = false
		notifyStoreMu.Unlock()
	})

	if _, err := primePayoutCache("test-token"); err != nil {
		t.Fatalf("prime failed: %v", err)
	}
	if len(sent) != 0 {
		t.Fatalf("prime sent %d notifications, want 0", len(sent))
	}

	notifyStoreMu.Lock()
	defer notifyStoreMu.Unlock()
	if _, ok := notifyStore["p1"]; ok {
		t.Fatal("fixture payment reached the notification store")
	}
}
