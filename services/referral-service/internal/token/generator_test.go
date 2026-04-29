package token

import (
	"testing"
)

func TestGenerate_LengthAndAlphabet(t *testing.T) {
	for i := 0; i < 100; i++ {
		tok, err := Generate(0)
		if err != nil {
			t.Fatalf("Generate failed: %v", err)
		}
		if len(tok) != defaultLength {
			t.Errorf("expected len=%d, got %d (%q)", defaultLength, len(tok), tok)
		}
		if !IsValid(tok) {
			t.Errorf("token %q failed IsValid", tok)
		}
	}
}

func TestGenerate_Uniqueness(t *testing.T) {
	const n = 1000
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		tok, err := Generate(0)
		if err != nil {
			t.Fatalf("Generate failed: %v", err)
		}
		if seen[tok] {
			t.Fatalf("collision on iteration %d: %q", i, tok)
		}
		seen[tok] = true
	}
}

func TestGenerate_CustomLength(t *testing.T) {
	tok, err := Generate(16)
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 16 {
		t.Errorf("expected len=16, got %d", len(tok))
	}
}

func TestIsValid(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"abcd", true},
		{"a1B2c3D4", true},
		{"abc", false},                     // too short
		{"abcdefghijklmnopqrstuvwxyz12345678901234", false}, // too long
		{"abcd-1234", false},               // dash not allowed
		{"abcd 1234", false},               // space not allowed
		{"abcd_1234", false},               // underscore not allowed
		{"", false},
	}
	for _, c := range cases {
		if got := IsValid(c.in); got != c.want {
			t.Errorf("IsValid(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
