package main

import (
	"math"
	"testing"
)

func TestParseByteSizeMB(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"850MiB", 850},
		{"1.5GiB", 1536},
		{"104857600", 100},
		{"512KiB", 0.5},
	}
	for _, tc := range cases {
		got, err := parseByteSizeMB(tc.in)
		if err != nil {
			t.Fatalf("parseByteSizeMB(%q) unexpected error: %v", tc.in, err)
		}
		if math.Abs(got-tc.want) > 0.01 {
			t.Fatalf("parseByteSizeMB(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
	if _, err := parseByteSizeMB("invalid"); err == nil {
		t.Fatal("expected error on invalid byte size")
	}
}

func TestSafeSeedValidation(t *testing.T) {
	if !safeSeedRe.MatchString("anon_01-test") {
		t.Fatal("expected valid seed")
	}
	for _, bad := range []string{"", "../escape", "a/b", "seed with space"} {
		if safeSeedRe.MatchString(bad) {
			t.Fatalf("expected %q to be rejected by safeSeedRe", bad)
		}
	}
}
