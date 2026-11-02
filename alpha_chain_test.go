package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

/* Captured live from finney: the four storages that describe one position. */
const (
	fxStaking = "0x0456a9aee6291bd03ab6d36d4d13e2bebae7cd403518066c72fba1b417d6ddd748"
	fxAlpha   = "0x602290ad6f2a93641100000000000000f3ffffffffffffff"
	fxShares  = "0x00cde63e8b2b948f0a00000000000000f7ffffffffffffff"
	fxPool    = "0x5ecc1688b75b0100"
)

const fixtureColdkey = "5GbD4Vk6cASzfgkywuGkGHszPSr1s6gx9y9fFBDjLV2q1GWS"

// stubChain serves the fixture values, keyed by which storage item was asked
// for, so the test exercises the real key construction rather than a mock of it.
func stubChain(t *testing.T, overrides map[string]string) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	items := map[string]string{
		"StakingHotkeys":      fxStaking,
		"AlphaV2":             fxAlpha,
		"TotalHotkeySharesV2": fxShares,
		"TotalHotkeyAlpha":    fxPool,
	}
	for k, v := range overrides {
		items[k] = v
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		result := "0x"
		if req.Method == "state_getStorage" && len(req.Params) > 0 {
			if s, ok := req.Params[0].(string); ok {
				key, _ := hex.DecodeString(strings.TrimPrefix(s, "0x"))
				for item, val := range items {
					if bytes.HasPrefix(key, subtensorKey(item, nil, nil)) {
						result = val
						break
					}
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("BT_RPC_URL", srv.URL)
	return srv, &hits
}

func TestAlphaStakeFromChain(t *testing.T) {
	stubChain(t, nil)
	resetUpstreamGuard()
	got, err := alphaStakeForColdkey(fixtureColdkey, 25)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got < 62.9648 || got > 62.9649 {
		t.Fatalf("alpha = %.10f, want 62.9648", got)
	}
}

func TestAlphaStakeRefusesMissingPool(t *testing.T) {
	stubChain(t, map[string]string{"TotalHotkeyAlpha": "0x"})
	resetUpstreamGuard()
	if _, err := alphaStakeForColdkey(fixtureColdkey, 25); err == nil {
		t.Fatal("a position without pool alpha must be an error, not a smaller total")
	}
}

func TestAlphaStakeRefusesTruncatedHotkeys(t *testing.T) {
	/* declares one hotkey but carries only ten bytes */
	stubChain(t, map[string]string{"StakingHotkeys": "0x04" + strings.Repeat("ab", 10)})
	resetUpstreamGuard()
	if _, err := alphaStakeForColdkey(fixtureColdkey, 25); err == nil {
		t.Fatal("a truncated hotkey vector must be an error, not zero stake")
	}
}

func TestAlphaStakeNoPositions(t *testing.T) {
	stubChain(t, map[string]string{"StakingHotkeys": "0x00"})
	resetUpstreamGuard()
	got, err := alphaStakeForColdkey(fixtureColdkey, 25)
	if err != nil {
		t.Fatalf("no positions is not an error: %v", err)
	}
	if got != 0 {
		t.Fatalf("alpha = %v, want 0", got)
	}
}

func TestAlphaStakeZeroPoolShares(t *testing.T) {
	/* zero shares would divide by zero; the read must return 0 instead */
	stubChain(t, map[string]string{"TotalHotkeySharesV2": "0x" + strings.Repeat("00", 24)})
	resetUpstreamGuard()
	got, err := alphaStakeForColdkey(fixtureColdkey, 25)
	if err != nil {
		t.Fatalf("zero pool shares must not error: %v", err)
	}
	if got != 0 {
		t.Fatalf("alpha = %v, want 0", got)
	}
}

func TestAlphaStakeMalformedSafeFloat(t *testing.T) {
	stubChain(t, map[string]string{"AlphaV2": "0x" + strings.Repeat("11", 20)})
	resetUpstreamGuard()
	if _, err := alphaStakeForColdkey(fixtureColdkey, 25); err == nil {
		t.Fatal("a short SafeFloat must be rejected")
	}
}

func TestAlphaNoNetworkWhenBlocked(t *testing.T) {
	_, hits := stubChain(t, nil)
	resetUpstreamGuard()
	upstreamFailed("alpha")
	if _, ok := fetchAlphaStake(fixtureColdkey); ok {
		t.Fatal("a blocked upstream must not report a figure")
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Fatalf("chain was queried %d times inside the failure window", n)
	}
	resetUpstreamGuard()
}
