package session

import (
	"testing"

	"github.com/fzxbl/terminal-mcp/internal/config"
)

// TestNormExploreUsesDefaults 白盒验证 normExplore：调用方值 <=0 时取软默认，而非硬上限。
func TestNormExploreUsesDefaults(t *testing.T) {
	c := config.Load("")

	read := ReadArgs{Op: "read"}
	normExplore(&read)
	if read.MaxBytes != int(c.ExploreMaxBytesDefault) {
		t.Fatalf("read MaxBytes=%d, want default %d", read.MaxBytes, c.ExploreMaxBytesDefault)
	}
	if read.Limit != c.ExploreReadLimitDefault {
		t.Fatalf("read Limit=%d, want default %d", read.Limit, c.ExploreReadLimitDefault)
	}

	grep := ReadArgs{Op: "grep"}
	normExplore(&grep)
	if grep.Limit != c.ExploreGrepLimitDefault {
		t.Fatalf("grep Limit=%d, want default %d", grep.Limit, c.ExploreGrepLimitDefault)
	}
	// before/after 为 0 时保持 0，不套默认。
	if grep.Before != 0 || grep.After != 0 {
		t.Fatalf("grep before/after should stay 0, got %d/%d", grep.Before, grep.After)
	}

	// >0 的值 clamp 到硬上限。
	big := ReadArgs{Op: "read", MaxBytes: 1 << 30, Limit: 1 << 20}
	normExplore(&big)
	if int64(big.MaxBytes) != c.ExploreMaxBytesHard {
		t.Fatalf("big MaxBytes=%d, want hard %d", big.MaxBytes, c.ExploreMaxBytesHard)
	}
	if big.Limit != c.ExploreReadLimitHard {
		t.Fatalf("big Limit=%d, want hard %d", big.Limit, c.ExploreReadLimitHard)
	}
}
