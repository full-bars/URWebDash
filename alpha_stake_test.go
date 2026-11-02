package main

import (
	"encoding/hex"
	"math"
	"strings"
	"testing"
)

func trimZeros(s string) string {
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	return s
}

/*
Values captured live from finney. They pin the SafeFloat decode and the

	share-to-alpha identity without needing the network.
*/
func TestSafeFloatDecode(t *testing.T) {
	cases := []struct {
		hex  string
		want string
	}{
		/* AlphaV2 position: 32084183.2137573278304 shares */
		{"602290ad6f2a93641100000000000000f3ffffffffffffff", "32084183.2137573278304"},
		/* TotalHotkeySharesV2: 194813382858.128149760 shares */
		{"00cde63e8b2b948f0a00000000000000f7ffffffffffffff", "194813382858.12814976"},
	}
	for _, c := range cases {
		raw, err := hex.DecodeString(c.hex)
		if err != nil {
			t.Fatal(err)
		}
		sf, err := decodeSafeFloat(raw)
		if err != nil {
			t.Fatalf("%s: %v", c.hex, err)
		}
		got := trimZeros(sf.value().FloatString(13))
		want := trimZeros(c.want)
		if got != want {
			t.Fatalf("decode %s = %s, want %s", c.hex, got, want)
		}
	}
}

func TestAlphaShareIdentity(t *testing.T) {
	/* the three captured values must reproduce the live 62.9648 alpha */
	posRaw, _ := hex.DecodeString("602290ad6f2a93641100000000000000f3ffffffffffffff")
	sharesRaw, _ := hex.DecodeString("00cde63e8b2b948f0a00000000000000f7ffffffffffffff")

	pos, _ := decodeSafeFloat(posRaw)
	shares, _ := decodeSafeFloat(sharesRaw)
	poolAlphaRao := uint64(382318797048926)

	got := alphaFromShares(pos, shares, poolAlphaRao)
	if math.Abs(got-62.96480329338554) > 1e-9 {
		t.Fatalf("alpha = %.14f, want 62.96480329338554", got)
	}
}

func TestCompactLen(t *testing.T) {
	/* compact 1 = 0x04, compact 271 = 0xbc 0x04 */
	if v, n := compactLen([]byte{0x04}); v != 1 || n != 1 {
		t.Fatalf("compact 1 = %d/%d", v, n)
	}
	/* two-byte mode: value<<2 | 1 = 1085 = 0x043d, little-endian */
	if v, n := compactLen([]byte{0x3d, 0x04}); v != 271 || n != 2 {
		t.Fatalf("compact 271 = %d/%d", v, n)
	}
}

/*
the coldkey's hotkey vector is 33 bytes: compact 1 then one 32-byte id. The

	slice used to start at 1+used, which dropped the first byte of the id.
*/
func TestStakingHotkeysVectorDecode(t *testing.T) {
	raw, _ := hex.DecodeString("0456a9aee6291bd03ab6d36d4d13e2bebae7cd403518066c72fba1b417d6ddd748")
	n, used := compactLen(raw)
	if n != 1 || used != 1 {
		t.Fatalf("compact = %d/%d", n, used)
	}
	body := raw[used:]
	if len(body) != 32 {
		t.Fatalf("body = %d bytes, want 32", len(body))
	}
	if hex.EncodeToString(body) != "56a9aee6291bd03ab6d36d4d13e2bebae7cd403518066c72fba1b417d6ddd748" {
		t.Fatalf("hotkey = %x", body)
	}
}
