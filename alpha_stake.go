package main

// SN25 alpha positions.
//
// On the current finney runtime there is no StakeInfo/Stake/StakeInfoV2 storage
// and no per-coldkey "stake" field to read. Positions are share-denominated:
// SubtensorModule.AlphaV2 holds one share position per (hotkey, coldkey, netuid),
// and the pool it belongs to is described by TotalHotkeyAlpha (alpha, rao) and
// TotalHotkeySharesV2 (shares). Alpha is therefore shares x pool_alpha / pool_shares.
//
// Share values are share_pool::SafeFloat, a decimal fixed point:
//   { mantissa: u128 little-endian, exponent: i64 little-endian }  == mantissa * 10^exponent
// The exponent is genuinely variable (observed -9 and -13 on live values), so this
// is not a fixed-decimal encoding and must be read as a pair.

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"sync"
	"time"

	"golang.org/x/crypto/blake2b"
)

const snNetuid = 25

type safeFloat struct {
	mant *big.Int
	exp  int64
}

func decodeSafeFloat(raw []byte) (safeFloat, error) {
	/* exactly 24 bytes: a longer payload means the layout changed, and decoding
	   the first 24 anyway would print a garbage number as if it were real */
	if len(raw) != 24 {
		return safeFloat{}, fmt.Errorf("SafeFloat must be exactly 24 bytes, got %d", len(raw))
	}
	mant := new(big.Int).SetBytes(reverseCopy(raw[0:16]))
	exp := int64(binary.LittleEndian.Uint64(raw[16:24]))
	/* live exponents are single-digit negatives; anything outside this band is a
	   misread, and a huge one makes big.Exp allocate without bound */
	if exp > 40 || exp < -40 {
		return safeFloat{}, fmt.Errorf("SafeFloat exponent out of range: %d", exp)
	}
	return safeFloat{mant: mant, exp: exp}, nil
}

// value returns mantissa * 10^exponent as an exact rational.
func (s safeFloat) value() *big.Rat {
	r := new(big.Rat).SetInt(s.mant)
	if s.exp == 0 {
		return r
	}
	pow := new(big.Int).Exp(big.NewInt(10), big.NewInt(abs64(s.exp)), nil)
	if s.exp > 0 {
		return r.Mul(r, new(big.Rat).SetInt(pow))
	}
	return r.Quo(r, new(big.Rat).SetInt(pow))
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

/* ---------- storage reads ------------------------------------------------- */

func storageGet(key []byte) ([]byte, error) {
	var hexVal string
	if err := rpcCall("state_getStorage", []any{"0x" + hex.EncodeToString(key)}, &hexVal); err != nil {
		return nil, err
	}
	if hexVal == "" || hexVal == "0x" {
		return nil, nil
	}
	return hex.DecodeString(trim0x(hexVal))
}

func trim0x(v string) string {
	if len(v) >= 2 && v[0] == '0' && (v[1] == 'x' || v[1] == 'X') {
		return v[2:]
	}
	return v
}

// concatKey builds twox128(module) ++ twox128(item) then appends blake2_128_concat
// hashed arguments and any identity (raw) arguments.
func subtensorKey(item string, hashed [][]byte, raw []byte) []byte {
	key := append(twox128("SubtensorModule"), twox128(item)...)
	for _, h := range hashed {
		key = append(key, blake2_128_concat(h)...)
	}
	key = append(key, raw...)
	return key
}

// stakingHotkeys lists the hotkeys this coldkey holds positions with. AlphaV2 is
// keyed by hotkey first, so a coldkey cannot be enumerated from it directly.
func stakingHotkeys(coldkeyID []byte) ([][]byte, error) {
	raw, err := storageGet(subtensorKey("StakingHotkeys", [][]byte{coldkeyID}, nil))
	if err != nil || len(raw) == 0 {
		return nil, err
	}
	n, used := compactLen(raw)
	if n < 0 {
		return nil, fmt.Errorf("StakingHotkeys: unsupported compact length encoding")
	}
	if used+n*32 > len(raw) {
		return nil, fmt.Errorf("StakingHotkeys truncated: %d bytes, %d keys", len(raw), n)
	}
	body := raw[used:] /* compactLen already reports the bytes it consumed */
	out := make([][]byte, 0, n)
	for i := 0; i < n && (i+1)*32 <= len(body); i++ {
		hk := make([]byte, 32)
		copy(hk, body[i*32:(i+1)*32])
		out = append(out, hk)
	}
	return out, nil
}

// compactLen decodes a SCALE compact integer, returning value and bytes used.
func compactLen(b []byte) (int, int) {
	if len(b) == 0 {
		return 0, 0
	}
	switch b[0] & 0x03 {
	case 0:
		return int(b[0] >> 2), 1
	case 1:
		if len(b) < 2 {
			return 0, 1
		}
		return int(binary.LittleEndian.Uint16(b) >> 2), 2
	case 2:
		if len(b) < 4 {
			return 0, 1
		}
		return int(binary.LittleEndian.Uint32(b) >> 2), 4
	default:
		/* mode 3 carries its length in the first byte's top six bits; it never
		   appears in the storages read here, so refuse it rather than guess */
		return -1, 1
	}
}

func blake2_128_concat(arg []byte) []byte {
	return append(blake128(arg), arg...)
}

func blake128(b []byte) []byte {
	h, err := blake2b.New(16, nil)
	if err != nil {
		return nil /* only fails on a bad key length */
	}
	h.Write(b)
	return h.Sum(nil)
}

/* ---------- alpha stake ---------------------------------------------------- */

var alphaCache struct {
	mu  sync.Mutex
	val float64
	ok  bool
	at  time.Time
	key string
}

const alphaCacheTTL = 5 * time.Minute

// alphaStakeForColdkey returns the alpha this coldkey holds on netuid, summed
// across every hotkey it has a position with.
func alphaStakeForColdkey(coldkey string, netuid uint16) (float64, error) {
	ck, err := ss58AccountID(coldkey)
	if err != nil {
		return 0, err
	}
	hotkeys, err := stakingHotkeys(ck)
	if err != nil {
		return 0, err
	}
	if len(hotkeys) == 0 {
		return 0, nil
	}
	nb := []byte{byte(netuid), byte(netuid >> 8)} /* identity hasher: raw u16 LE */
	total := new(big.Rat)

	for _, hk := range hotkeys {
		posRaw, err := storageGet(subtensorKey("AlphaV2", [][]byte{hk, ck}, nb))
		if err != nil {
			return 0, err
		}
		if len(posRaw) == 0 {
			continue /* no position on this netuid for this hotkey */
		}
		pos, err := decodeSafeFloat(posRaw)
		if err != nil {
			return 0, err
		}

		sharesRaw, err := storageGet(subtensorKey("TotalHotkeySharesV2", [][]byte{hk}, nb))
		if err != nil {
			return 0, err
		}
		pAlphaRaw, err := storageGet(subtensorKey("TotalHotkeyAlpha", [][]byte{hk}, nb))
		if err != nil {
			return 0, err
		}
		/* a position exists, so its pool must exist too. Continuing here would
		   understate the total and report it as known. */
		if len(sharesRaw) == 0 || len(pAlphaRaw) < 8 {
			return 0, fmt.Errorf("position on netuid %d has no pool data", netuid)
		}
		shares, err := decodeSafeFloat(sharesRaw)
		if err != nil {
			return 0, err
		}
		poolShares := shares.value()
		if poolShares.Sign() == 0 {
			continue
		}
		/* pool alpha is u64 rao; the identity is alpha_rao = position * pool_alpha / pool_shares
		   because a position is a share of that pool, not an amount of alpha */
		poolAlphaRao := new(big.Int).SetUint64(binary.LittleEndian.Uint64(pAlphaRaw))
		shareRat := pos.value()
		alphaRao := new(big.Rat).Mul(shareRat, new(big.Rat).SetInt(poolAlphaRao))
		alphaRao.Quo(alphaRao, poolShares)
		total.Add(total, alphaRao)
	}

	raoPerAlpha := new(big.Rat).SetInt64(1e9)
	out := new(big.Rat).Quo(total, raoPerAlpha)
	f, _ := out.Float64()
	return f, nil
}

func fetchAlphaStake(coldkey string) (float64, bool) {
	if coldkey == "" {
		return 0, false
	}
	alphaCache.mu.Lock()
	if alphaCache.key == coldkey && time.Since(alphaCache.at) < alphaCacheTTL {
		v, ok := alphaCache.val, alphaCache.ok
		alphaCache.mu.Unlock()
		return v, ok
	}
	alphaCache.mu.Unlock()

	v, err := alphaStakeForColdkey(coldkey, snNetuid)
	if err != nil {
		fmt.Printf("[alpha] read failed: %v\n", err)
		return 0, false
	}
	alphaCache.mu.Lock()
	alphaCache.val, alphaCache.ok, alphaCache.at, alphaCache.key = v, true, time.Now(), coldkey
	alphaCache.mu.Unlock()
	return v, true
}

// alphaFromShares is the identity on its own, so the test can pin it without a
// network read: position shares of a pool convert at pool_alpha / pool_shares.
func alphaFromShares(pos, pool safeFloat, poolAlphaRao uint64) float64 {
	shares := pool.value()
	if shares.Sign() == 0 {
		return 0
	}
	alphaRao := new(big.Rat).Mul(pos.value(), new(big.Rat).SetInt(new(big.Int).SetUint64(poolAlphaRao)))
	alphaRao.Quo(alphaRao, shares)
	out := new(big.Rat).Quo(alphaRao, new(big.Rat).SetInt64(1e9))
	f, _ := out.Float64()
	return f
}
