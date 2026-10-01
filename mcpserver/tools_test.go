package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fzxbl/terminal-mcp/internal/audit"
	"github.com/fzxbl/terminal-mcp/internal/config"
	"github.com/fzxbl/terminal-mcp/internal/session"
)

type ownerHeaderTransport struct {
	owner string
}

func (t ownerHeaderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Header.Set("X-MCP-USER", t.owner)
	return http.DefaultTransport.RoundTrip(clone)
}

func connectOwnerClient(t *testing.T, endpoint, owner string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "terminal-mcp-test", Version: "test"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           &http.Client{Transport: ownerHeaderTransport{owner: owner}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect owner %q: %v", owner, err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callClose(t *testing.T, cs *mcp.ClientSession, id string) session.Envelope {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "terminal_close",
		Arguments: sessionIDInput{SessionID: id},
	})
	if err != nil {
		t.Fatalf("terminal_close(%q): %v", id, err)
	}
	if res.IsError {
		t.Fatalf("terminal_close(%q) returned MCP tool error: %+v", id, res.Content)
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal terminal_close(%q) structured result: %v", id, err)
	}
	var env session.Envelope
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("decode terminal_close(%q) structured result: %v", id, err)
	}
	return env
}

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

func TestFanoutListStripsCapabilities(t *testing.T) {
	got := fanoutList([]map[string]string{{"session_id": "s", "session_key": "secret", "host": "h"}}, nil, nil)
	if len(got) != 1 || got[0]["session_key"] != "" || got[0]["capability_hash"] != "" || got[0]["host"] != "h" {
		t.Fatalf("fanout list exposed capability or lost fields: %#v", got)
	}
}

func TestSessionCapabilityIsNeverAudited(t *testing.T) {
	const secret = "audit-must-not-contain-this-key"
	var buf bytes.Buffer
	logEnv(audit.New(&buf), nil, "terminal_status", map[string]any{"session_id": "s", "session_key": secret}, session.Envelope{State: "idle"})
	if strings.Contains(buf.String(), secret) {
		t.Fatalf("audit output contains session capability: %s", buf.String())
	}
}

// TestExploreToolSchemaSplit 校验 explore 已拆成独立工具：terminal_explore 的输入 schema
// 含 output_ref/op，而 terminal_output 的 schema 不再含 output_ref/op/pattern。
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

	rd, ok := schemas["terminal_output"]
	if !ok {
		t.Fatalf("terminal_output not registered")
	}
	for _, f := range []string{"output_ref", "op", "pattern"} {
		if strings.Contains(rd, `"`+f+`":`) {
			t.Fatalf("terminal_output schema should no longer contain %q: %s", f, rd)
		}
	}
	for _, name := range []string{"terminal_send", "terminal_output", "terminal_explore", "terminal_control", "terminal_status", "terminal_close"} {
		schema, ok := schemas[name]
		if !ok || !strings.Contains(schema, "session_key") {
			t.Fatalf("%s schema missing session_key: %s", name, schema)
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

func TestCloseIsIdempotentAndNonDisclosingAtMCPBoundary(t *testing.T) {
	Init("")
	session.InitStore(10)

	opened, err := session.Open("local", "", "", "")
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	id := opened["session_id"]
	t.Cleanup(func() { session.Close(id) })
	for i := 0; i < 200 && session.Status(id).State != "idle"; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if got := session.Status(id).State; got != "idle" {
		t.Fatalf("alice session state = %q, want idle", got)
	}

	httpServer := httptest.NewServer(NewHTTPHandler(nil))
	t.Cleanup(httpServer.Close)
	alice := connectOwnerClient(t, httpServer.URL+"/mcp", "alice")
	bob := connectOwnerClient(t, httpServer.URL+"/mcp", "bob")

	unauthorized := callClose(t, bob, id)
	if got := session.Status(id).State; got != "idle" {
		t.Fatalf("unauthorized close changed alice session state to %q", got)
	}
	unknown := callClose(t, bob, "does-not-exist")
	if unauthorized != unknown {
		t.Errorf("unauthorized close disclosed session existence: unauthorized=%+v unknown=%+v", unauthorized, unknown)
	}
	if unauthorized.State != "dead" || unauthorized.Error != "" {
		t.Errorf("unauthorized/unknown close = %+v, want successful no-op dead envelope", unauthorized)
	}

	if first := callClose(t, alice, id); first.State != "dead" || first.Error != "" {
		t.Fatalf("first close = %+v, want successful dead envelope", first)
	}
	if second := callClose(t, alice, id); second.State != "dead" || second.Error != "" {
		t.Fatalf("second close = %+v, want successful no-op dead envelope", second)
	}
}
