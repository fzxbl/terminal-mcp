package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
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

// TestExploreToolSchemaSplit 校验 explore 已拆成独立工具：terminal_explore 的输入 schema
// 含 output_ref/op，而 terminal_read 的 schema 不再含 output_ref/op/pattern。
func TestExploreToolSchemaSplit(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "terminal-mcp-test", Version: "test"}, nil)
	registerTools(srv, audit.New(io.Discard))

	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	schemas := map[string]string{}
	for _, tl := range res.Tools {
		b, _ := json.Marshal(tl.InputSchema)
		schemas[tl.Name] = string(b)
	}

	exp, ok := schemas["terminal_explore"]
	if !ok {
		t.Fatalf("terminal_explore not registered; tools=%v", schemas)
	}
	// 检查 JSON schema 里的属性键（形如 "output_ref":），避免误匹配 "properties" 里的子串。
	for _, f := range []string{"output_ref", "op", "pattern", "line_offset", "byte_offset"} {
		if !strings.Contains(exp, `"`+f+`":`) {
			t.Fatalf("terminal_explore schema missing %q: %s", f, exp)
		}
	}

	rd, ok := schemas["terminal_read"]
	if !ok {
		t.Fatalf("terminal_read not registered")
	}
	for _, f := range []string{"output_ref", "op", "pattern"} {
		if strings.Contains(rd, `"`+f+`":`) {
			t.Fatalf("terminal_read schema should no longer contain %q: %s", f, rd)
		}
	}
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
