package outputref

import "testing"

func TestSignParseRoundTrip(t *testing.T) {
	s := Scope{From: 100, To: 5000, Input: "cat big.log", Observe: false}
	tok := Sign(s)
	if tok == "" {
		t.Fatal("empty token")
	}
	got, err := Parse(tok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got != s {
		t.Fatalf("round trip mismatch: %+v != %+v", got, s)
	}
}

func TestParseRejectsTampered(t *testing.T) {
	tok := Sign(Scope{From: 0, To: 10})
	if _, err := Parse(tok + "x"); err == nil {
		t.Fatal("tampered token accepted")
	}
	if _, err := Parse("not-a-token"); err == nil {
		t.Fatal("garbage token accepted")
	}
	if _, err := Parse(""); err == nil {
		t.Fatal("empty token accepted")
	}
}
