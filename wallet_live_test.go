package main

import (
	"os"
	"strings"
	"testing"
)

/* Not shipped as a unit test: run with the real token to prove the API identity
   and the chain read together. Skipped unless BT_LIVE=1. */
func TestLiveWalletIdentityAndBalance(t *testing.T) {
	if os.Getenv("BT_LIVE") != "1" {
		t.Skip("set BT_LIVE=1 to hit the live API and chain")
	}
	raw, err := os.ReadFile(os.Getenv("HOME") + "/.urnetwork/jwt")
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(string(raw))
	rec, err := fetchSNWallet(token)
	if err != nil {
		t.Fatalf("sn/wallet: %v", err)
	}
	t.Logf("coldkey=%s scope=%s epochs %d..%d", rec.ColdkeySS58, rec.ConsentScope, rec.FromEpoch, rec.ThroughEpoch)
	if !strings.HasPrefix(rec.ColdkeySS58, "5") {
		t.Fatalf("coldkey does not look like ss58: %s", rec.ColdkeySS58)
	}
	bal := fetchBalances(rec.ColdkeySS58)
	if !bal.Known {
		t.Fatalf("balance unknown")
	}
	t.Logf("TAO free = %.6f", bal.TaoFree)
}
