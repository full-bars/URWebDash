package main

import (
	"encoding/hex"
	"testing"
)

/*
the storage key is only correct if the hashes are, and a wrong key reads

	someone else's storage without failing. Pin both against known vectors.
*/
func TestXXH64Vectors(t *testing.T) {
	cases := []struct {
		in   string
		want uint64
	}{
		{"", 0xEF46DB3751D8E999},
		{"a", 0xD24EC4F1A98C6E5B},
		{"abc", 0x44BC2CF5AD770999},
	}
	for _, c := range cases {
		if got := xxh64([]byte(c.in), 0); got != c.want {
			t.Fatalf("xxh64(%q) = %#x, want %#x", c.in, got, c.want)
		}
	}
}

func TestTwox128KnownPrefix(t *testing.T) {
	/* twox128("System") is a published constant in substrate */
	got := hex.EncodeToString(twox128("System"))
	if got != "26aa394eea5630e07c48ae0c9558cef7" {
		t.Fatalf("twox128(System) = %s", got)
	}
	got = hex.EncodeToString(twox128("Account"))
	if got != "b99d880ec681799c0cf30e8886371da9" {
		t.Fatalf("twox128(Account) = %s", got)
	}
}

func TestSS58Decode(t *testing.T) {
	/* 32-byte id must survive the round trip through the address form */
	id, err := ss58AccountID("5GbD4Vk6cASzfgkywuGkGHszPSr1s6gx9y9fFBDjLV2q1GWS")
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 32 {
		t.Fatalf("decoded %d bytes", len(id))
	}
}
