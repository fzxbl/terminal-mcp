package config

import "testing"

func TestDefaults(t *testing.T) {
	c := Load("")
	if c.SSHUser != "" {
		t.Fatalf("SSHUser must have no default, got %q", c.SSHUser)
	}
	if c.MaxSessions != 2 {
		t.Fatalf("MaxSessions default = %d, want 2", c.MaxSessions)
	}
	if c.DefaultShell != "bash" {
		t.Fatalf("DefaultShell default = %q, want bash", c.DefaultShell)
	}
}

func TestIdentityAndPeersDefaults(t *testing.T) {
	var c Config
	c.applyDefaults()
	if c.Identity.Mode != "raw" {
		t.Fatalf("Identity.Mode default = %q, want raw", c.Identity.Mode)
	}
	if c.Identity.OnMissing != "reject" {
		t.Fatalf("Identity.OnMissing default = %q, want reject", c.Identity.OnMissing)
	}
	if len(c.Identity.Headers) != 1 || c.Identity.Headers[0] != "X-MCP-USER" {
		t.Fatalf("Identity.Headers default = %v, want [X-MCP-USER]", c.Identity.Headers)
	}
	if c.Peers == nil {
		t.Fatalf("Peers should default to empty non-nil slice")
	}
}
