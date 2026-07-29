package mcpserver

import (
	"io"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fzxbl/terminal-mcp/internal/audit"
	"github.com/fzxbl/terminal-mcp/internal/config"
	"github.com/fzxbl/terminal-mcp/internal/session"
)

// TestRegisterToolsSchemas 确保所有工具都能成功注册。官方 SDK 在注册时校验
// input/output schema（输出必须是 object），非法 schema 会 panic——本测试作为回归护栏，
// 防止再次出现 terminal_list 返回顶层数组导致启动时 panic 的问题。
func TestRegisterToolsSchemas(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("registerTools panicked: %v", r)
		}
	}()
	srv := mcp.NewServer(&mcp.Implementation{Name: "terminal-mcp-test", Version: "test"}, nil)
	registerTools(srv, audit.New(io.Discard))
}

// TestResolveDescOverrides 验证工具描述覆盖的优先级：编程覆盖 > 配置文件 > 内置默认，
// 且空串条目回退到内置默认。
func TestResolveDescOverrides(t *testing.T) {
	config.Load("")
	orig := config.Get().ToolDescriptions
	defer func() { config.Get().ToolDescriptions = orig }()

	// 无覆盖 → 内置默认。
	if got := resolveDesc("terminal_open"); got != descOpen {
		t.Fatalf("expected default desc, got %q", got)
	}

	// 配置文件覆盖生效；空串条目回退默认。
	config.Get().ToolDescriptions = map[string]string{"terminal_open": "cfg-open", "terminal_send": ""}
	if got := resolveDesc("terminal_open"); got != "cfg-open" {
		t.Fatalf("config override not applied: %q", got)
	}
	if got := resolveDesc("terminal_send"); got != descSend {
		t.Fatalf("empty config entry should fall back to default, got %q", got)
	}

	// 编程覆盖优先级最高。
	SetToolDescriptions(map[string]string{"terminal_open": "prog-open"})
	defer SetToolDescriptions(nil)
	if got := resolveDesc("terminal_open"); got != "prog-open" {
		t.Fatalf("programmatic override should win, got %q", got)
	}
	// 编程覆盖未列出的工具仍走配置文件覆盖。
	if got := resolveDesc("terminal_send"); got != descSend {
		t.Fatalf("terminal_send should still fall back to default, got %q", got)
	}
}

// 这些错误路径在构造子进程前就返回，因此无需真实 shell/ssh。
func TestOpenValidation(t *testing.T) {
	t.Run("ssh without host errors", func(t *testing.T) {
		if _, err := session.Open("ssh", "", "", ""); err == nil {
			t.Fatal("expected error for mode=ssh with empty host, got nil")
		}
	})

	t.Run("invalid mode errors", func(t *testing.T) {
		if _, err := session.Open("telnet", "", "h1", ""); err == nil {
			t.Fatal("expected error for invalid mode, got nil")
		}
		if _, err := session.Open("", "", "", ""); err == nil {
			t.Fatal("expected error for empty mode, got nil")
		}
	})
}

func TestOwnerSigFromHeader(t *testing.T) {
	// 默认配置 headers=[X-MCP-USER], mode=raw, on_missing=reject
	Init("")
	theSigner = nil // 重置惰性 signer，确保读取默认配置
	h := http.Header{}
	h.Set("X-MCP-USER", "alice")
	sig, ok := signer().Signature(h)
	if !ok || sig != "alice" {
		t.Fatalf("ownerSig = (%q,%v)", sig, ok)
	}
}

func TestAuthorizeOwnerUnknownSession(t *testing.T) {
	session.InitStore(10)
	if authorizeOwner("alice", "no-such-id") {
		t.Fatalf("unknown session must not authorize")
	}
}
