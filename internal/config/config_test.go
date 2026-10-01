package config

import (
	"strings"
	"testing"
)

func TestDefaultSSHOptsPreserveHostKeyVerification(t *testing.T) {
	c := Load("")
	if !strings.Contains(c.SSHOpts, "StrictHostKeyChecking=accept-new") {
		t.Fatalf("SSHOpts default must use accept-new host-key policy, got %q", c.SSHOpts)
	}
	if strings.Contains(c.SSHOpts, "StrictHostKeyChecking=no") {
		t.Fatalf("SSHOpts default must not disable host-key checking: %q", c.SSHOpts)
	}
	if strings.Contains(c.SSHOpts, "UserKnownHostsFile=/dev/null") {
		t.Fatalf("SSHOpts default must persist known host keys: %q", c.SSHOpts)
	}
}

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

func TestPeersDefault(t *testing.T) {
	var c Config
	c.applyDefaults()
	if c.Peers == nil {
		t.Fatalf("Peers should default to empty non-nil slice")
	}
}

func TestExploreDefaultsAndHardCaps(t *testing.T) {
	var c Config
	c.applyDefaults()
	// 软默认值
	if c.ExploreMaxBytesDefault != 32<<10 {
		t.Fatalf("ExploreMaxBytesDefault = %d, want %d", c.ExploreMaxBytesDefault, 32<<10)
	}
	if c.ExploreReadLimitDefault != 100 {
		t.Fatalf("ExploreReadLimitDefault = %d, want 100", c.ExploreReadLimitDefault)
	}
	if c.ExploreGrepLimitDefault != 50 {
		t.Fatalf("ExploreGrepLimitDefault = %d, want 50", c.ExploreGrepLimitDefault)
	}
	// 硬上限
	if c.ExploreMaxBytesHard != 128<<10 {
		t.Fatalf("ExploreMaxBytesHard = %d, want %d", c.ExploreMaxBytesHard, 128<<10)
	}
	if c.ExploreReadLimitHard != 1000 {
		t.Fatalf("ExploreReadLimitHard = %d, want 1000", c.ExploreReadLimitHard)
	}
	if c.ExploreGrepLimitHard != 500 {
		t.Fatalf("ExploreGrepLimitHard = %d, want 500", c.ExploreGrepLimitHard)
	}
	if c.ExploreCtxHard != 20 {
		t.Fatalf("ExploreCtxHard = %d, want 20", c.ExploreCtxHard)
	}
	// 默认值不得超过对应硬上限（clamp）
	if c.ExploreMaxBytesDefault > c.ExploreMaxBytesHard ||
		c.ExploreReadLimitDefault > c.ExploreReadLimitHard ||
		c.ExploreGrepLimitDefault > c.ExploreGrepLimitHard {
		t.Fatalf("defaults must not exceed hard caps: %+v", c)
	}
}
