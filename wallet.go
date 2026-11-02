package main

// Bittensor wallet identity and balance.
//
// The coldkey comes from the account's own consent record (GET /sn/wallet), not
// from the payout history. The legacy USDC payout wallet is a Solana address,
// and reading it into Coldkey made the dashboard show a Solana key labelled as
// a Bittensor one.
//
// Balances are chain reads. Subtensor storage keys are twox128(module) +
// twox128(item) + blake2_128_concat(arg), so the two hashes are implemented
// here rather than pulled in as dependencies.

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/blake2b"
)

const (
	bittensorRPC     = "https://entrypoint-finney.opentensor.ai:443"
	snWalletCacheTTL = 10 * time.Minute
	balanceCacheTTL  = 5 * time.Minute
)

type snWalletEntry struct {
	ColdkeySS58  string `json:"coldkey_ss58"`
	ConsentScope string `json:"consent_scope"`
	FromEpoch    int64  `json:"from_epoch"`
	ThroughEpoch int64  `json:"through_epoch"`
	SetAtMillis  int64  `json:"set_at_millis"`
}

type snWalletResponse struct {
	Wallet  *snWalletEntry  `json:"wallet"`
	Wallets []snWalletEntry `json:"wallets"`
}

var snWalletCache struct {
	mu  sync.Mutex
	rec *snWalletEntry
	at  time.Time
}

// fetchSNWallet asks the account's own API which coldkey is mapped to it. This
// works for any user with a session token, so nothing has to exist on disk.
func fetchSNWallet(token string) (*snWalletEntry, error) {
	if token == "" {
		return nil, fmt.Errorf("no token")
	}
	/* this runs on the request path: after a failure, do not pay the timeout
	   again inside the window - serve the last good entry if there is one */
	if upstreamBlocked("snwallet") {
		snWalletCache.mu.Lock()
		defer snWalletCache.mu.Unlock()
		if snWalletCache.rec != nil {
			rec := *snWalletCache.rec
			return &rec, nil
		}
		return nil, fmt.Errorf("sn wallet unavailable (recent failure)")
	}
	snWalletCache.mu.Lock()
	if snWalletCache.rec != nil && time.Since(snWalletCache.at) < snWalletCacheTTL {
		rec := *snWalletCache.rec
		snWalletCache.mu.Unlock()
		return &rec, nil
	}
	snWalletCache.mu.Unlock()

	req, err := http.NewRequest("GET", "https://api.bringyour.com/sn/wallet", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		upstreamFailed("snwallet")
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		upstreamFailed("snwallet")
		return nil, fmt.Errorf("sn/wallet: HTTP %d", resp.StatusCode)
	}
	var out snWalletResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	/* the current mapping is the last consent actually in force; fall back to the
	   newest history row so a deployment without a live consent still names its key */
	/* pick explicitly: the current mapping is the entry that carries a consent
	   scope, otherwise the newest by set_at_millis. Relying on array order made
	   this depend on how the server happens to sort its history. */
	rec := out.Wallet
	if rec == nil || strings.TrimSpace(rec.ColdkeySS58) == "" {
		var best *snWalletEntry
		for i := range out.Wallets {
			w := &out.Wallets[i]
			if strings.TrimSpace(w.ColdkeySS58) == "" {
				continue
			}
			if w.ConsentScope != "" {
				if best == nil || w.ConsentScope != "" && best.ConsentScope == "" || w.SetAtMillis > best.SetAtMillis {
					best = w
				}
			} else if best == nil || (best.ConsentScope == "" && w.SetAtMillis > best.SetAtMillis) {
				/* with no consent scope anywhere, the newest entry wins: keeping
				   the first one reported an older mapping as current */
				best = w
			}
		}
		rec = best
	}
	if rec == nil || rec.ColdkeySS58 == "" {
		return nil, fmt.Errorf("no coldkey on the account")
	}
	cp := *rec
	snWalletCache.mu.Lock()
	snWalletCache.rec = &cp
	snWalletCache.at = time.Now()
	snWalletCache.mu.Unlock()
	return &cp, nil
}

/* ---------- ss58 + hashing ------------------------------------------------- */

const ss58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func base58Decode(s string) ([]byte, error) {
	num := big.NewInt(0)
	base := big.NewInt(58)
	for _, r := range s {
		idx := strings.IndexRune(ss58Alphabet, r)
		if idx < 0 {
			return nil, fmt.Errorf("bad base58 char %q", r)
		}
		num.Mul(num, base)
		num.Add(num, big.NewInt(int64(idx)))
	}
	raw := num.Bytes()
	/* leading '1's are leading zero bytes */
	zeros := 0
	for zeros < len(s) && s[zeros] == '1' {
		zeros++
	}
	return append(make([]byte, zeros), raw...), nil
}

// ss58AccountID decodes an address to its 32-byte account id and verifies the
// checksum. Without the check a mistyped address decodes to some other account,
// reads a zero balance, and the page reports it as known.
func ss58AccountID(addr string) ([]byte, error) {
	raw, err := base58Decode(addr)
	if err != nil {
		return nil, err
	}
	if len(raw) < 35 {
		return nil, fmt.Errorf("ss58 too short: %d bytes", len(raw))
	}
	prefixLen := 1
	if raw[0]&0x40 != 0 { /* two-byte network prefix */
		prefixLen = 2
	}
	if len(raw) != prefixLen+32+2 {
		return nil, fmt.Errorf("ss58 length %d for prefix %d", len(raw), prefixLen)
	}
	body := raw[:prefixLen+32]
	id := raw[prefixLen : prefixLen+32]
	sum, err := blake2b.New512(nil)
	if err != nil {
		return nil, err
	}
	sum.Write([]byte("SS58PRE"))
	sum.Write(body)
	out := sum.Sum(nil)
	if out[0] != raw[prefixLen+32] || out[1] != raw[prefixLen+33] {
		return nil, fmt.Errorf("ss58 checksum mismatch")
	}
	return id, nil
}

/*
xxhash64, seed 0 — the chain's twox hash is xxh64 and substrate's twox128 is

	two of them concatenated with seeds 0 and 1
*/
const (
	prime1 uint64 = 11400714785074694791
	prime2 uint64 = 14029467366897019727
	prime3 uint64 = 1609587929392839161
	prime4 uint64 = 9650029242287828579
	prime5 uint64 = 2870177450012600261
)

func rotl(x uint64, r uint) uint64 { return (x << r) | (x >> (64 - r)) }

func xxh64(data []byte, seed uint64) uint64 {
	n := len(data)
	var h uint64
	i := 0
	if n >= 32 {
		v1 := seed + prime1 + prime2
		v2 := seed + prime2
		v3 := seed
		v4 := seed - prime1
		for ; i <= n-32; i += 32 {
			v1 = rotl(v1+binary.LittleEndian.Uint64(data[i:])*prime2, 31) * prime1
			v2 = rotl(v2+binary.LittleEndian.Uint64(data[i+8:])*prime2, 31) * prime1
			v3 = rotl(v3+binary.LittleEndian.Uint64(data[i+16:])*prime2, 31) * prime1
			v4 = rotl(v4+binary.LittleEndian.Uint64(data[i+24:])*prime2, 31) * prime1
		}
		h = rotl(v1, 1) + rotl(v2, 7) + rotl(v3, 12) + rotl(v4, 18)
		for _, v := range []uint64{v1, v2, v3, v4} {
			h = (h^rotl(v*prime2, 31)*prime1)*prime1 + prime4
		}
	} else {
		h = seed + prime5
	}
	h += uint64(n)
	for ; i+8 <= n; i += 8 {
		k := rotl(binary.LittleEndian.Uint64(data[i:])*prime2, 31) * prime1
		h = (rotl(h^k, 27)*prime1 + prime4)
	}
	if i+4 <= n {
		h = (rotl(h^(uint64(binary.LittleEndian.Uint32(data[i:]))*prime1), 23)*prime2 + prime3)
		i += 4
	}
	for ; i < n; i++ {
		h = rotl(h^(uint64(data[i])*prime5), 11) * prime1
	}
	h ^= h >> 33
	h *= prime2
	h ^= h >> 29
	h *= prime3
	h ^= h >> 32
	return h
}

func twox128(b string) []byte {
	out := make([]byte, 16)
	binary.LittleEndian.PutUint64(out[0:], xxh64([]byte(b), 0))
	binary.LittleEndian.PutUint64(out[8:], xxh64([]byte(b), 1))
	return out
}

// storageKey builds twox128(module) ++ twox128(item) ++ blake2_128(arg) ++ arg
func storageKey(module, item string, arg []byte) []byte {
	var buf bytes.Buffer
	buf.Write(twox128(module))
	buf.Write(twox128(item))
	h, err := blake2b.New(16, nil)
	if err != nil { /* blake2b.New only fails on a bad key length */
	}
	h.Write(arg)
	buf.Write(h.Sum(nil))
	buf.Write(arg)
	return buf.Bytes()
}

/* ---------- rpc ------------------------------------------------------------ */

type rpcReq struct {
	ID     int    `json:"id"`
	JSON   string `json:"jsonrpc"`
	Method string `json:"method"`
	Params []any  `json:"params"`
}

func rpcCall(method string, params []any, out any) error {
	url := strings.TrimSpace(os.Getenv("BT_RPC_URL"))
	if url == "" {
		url = bittensorRPC
	}
	body, _ := json.Marshal(rpcReq{ID: 1, JSON: "2.0", Method: method, Params: params})
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return err
	}
	if env.Error != nil {
		return fmt.Errorf("%s: %s", method, env.Error.Message)
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

/* ---------- balances ------------------------------------------------------- */

type dashBalances struct {
	TaoFree float64 `json:"taoFree"`
	Known   bool    `json:"known"`
}

var balanceCache struct {
	mu  sync.Mutex
	val dashBalances
	at  time.Time
	key string
}

// accountFreeTao reads System.Account for the coldkey. AccountInfo is
// nonce, consumers, providers, sufficients (each u32) followed by AccountData
// whose first field is the free balance as a little-endian u128.
func accountFreeTao(accountID []byte) (float64, error) {
	key := storageKey("System", "Account", accountID)
	var hexVal string
	if err := rpcCall("state_getStorage", []any{"0x" + fmt.Sprintf("%x", key)}, &hexVal); err != nil {
		return 0, err
	}
	if hexVal == "" || hexVal == "0x" {
		return 0, nil
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(hexVal, "0x"))
	if err != nil {
		return 0, err
	}
	const offset = 16 /* four u32 fields */
	if len(raw) < offset+16 {
		return 0, fmt.Errorf("AccountInfo too short: %d", len(raw))
	}
	free := new(big.Int).SetBytes(reverseCopy(raw[offset : offset+16]))
	f := new(big.Float).SetInt(free)
	f.Quo(f, big.NewFloat(1e9))
	out, _ := f.Float64()
	return out, nil
}

func reverseCopy(b []byte) []byte {
	out := make([]byte, len(b))
	for i := range b {
		out[i] = b[len(b)-1-i]
	}
	return out
}

func fetchBalances(coldkey string) dashBalances {
	if coldkey == "" {
		return dashBalances{}
	}
	if upstreamBlocked("balances") {
		balanceCache.mu.Lock()
		defer balanceCache.mu.Unlock()
		if balanceCache.key == coldkey {
			return balanceCache.val /* stale but real beats a hung request */
		}
		return dashBalances{}
	}
	balanceCache.mu.Lock()
	if balanceCache.key == coldkey && time.Since(balanceCache.at) < balanceCacheTTL {
		v := balanceCache.val
		balanceCache.mu.Unlock()
		return v
	}
	balanceCache.mu.Unlock()

	id, err := ss58AccountID(coldkey)
	if err != nil {
		fmt.Printf("[balance] bad coldkey %q: %v\n", coldkey, err)
		return dashBalances{}
	}
	tao, err := accountFreeTao(id)
	if err != nil {
		upstreamFailed("balances")
		fmt.Printf("[balance] tao read failed: %v\n", err)
		return dashBalances{}
	}
	out := dashBalances{TaoFree: tao, Known: true}
	balanceCache.mu.Lock()
	balanceCache.val = out
	balanceCache.at = time.Now()
	balanceCache.key = coldkey
	balanceCache.mu.Unlock()
	return out
}
