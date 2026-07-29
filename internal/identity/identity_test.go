package identity

import (
	"net/http"
	"testing"
)

func TestSignatureRaw(t *testing.T) {
	s := New([]string{"X-MCP-USER", "X-MCP-Client-Id"}, "raw", "reject")
	h := http.Header{}
	h.Set("X-MCP-USER", "alice")
	h.Set("X-MCP-Client-Id", "cli-1")
	sig, ok := s.Signature(h)
	if !ok || sig != "alice\x1fcli-1" {
		t.Fatalf("got (%q,%v)", sig, ok)
	}
}

func TestSignatureRejectOnMissing(t *testing.T) {
	s := New([]string{"X-MCP-USER"}, "raw", "reject")
	if _, ok := s.Signature(http.Header{}); ok {
		t.Fatalf("expected reject when header missing")
	}
}

func TestSignatureAllowEmpty(t *testing.T) {
	s := New([]string{"X-MCP-USER"}, "raw", "allow_empty")
	sig, ok := s.Signature(http.Header{})
	if !ok || sig != "" {
		t.Fatalf("got (%q,%v), want empty ok", sig, ok)
	}
}

func TestSignatureSHA256Stable(t *testing.T) {
	s := New([]string{"X-MCP-USER"}, "sha256", "reject")
	h := http.Header{}
	h.Set("X-MCP-USER", "alice")
	a, _ := s.Signature(h)
	b, _ := s.Signature(h)
	if a == "" || a != b || a == "alice" {
		t.Fatalf("sha256 sig unstable or not hashed: %q vs %q", a, b)
	}
}
