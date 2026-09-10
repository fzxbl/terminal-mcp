package session

import (
	"net"
	"strings"
	"testing"
)

// TestPublicBaseURLAndTerminalURL：terminal_url 用「对外入口 + 路径里的会话 id」拼成，
// 对外入口与「节点间拨号地址」（自身直连地址）互不影响。
//
// 后一条是重点：对外入口与自身直连地址正交、互不影响，设置顺序无关——绝不能让「设置
// 对外入口」顺带改写拨号地址，否则会拿着带路径的字符串去拨号。
func TestPublicBaseURLAndTerminalURL(t *testing.T) {
	t.Cleanup(func() { SetPublicBaseURL(""); SetSelfAddr(""); SetPathPrefix("") })

	SetSelfAddr("10.0.0.1:8900") // 内部拨号地址
	SetPathPrefix("/mcp")        // 宿主挂载前缀（唯一来源：MountWebTerminal）
	SetPublicBaseURL("https://mcp.example.com/")
	if got, want := terminalURL("sid-1"),
		"https://mcp.example.com/mcp/view/terminal/sid-1"; got != want {
		t.Errorf("terminalURL = %q, want %q（入口去掉末尾斜杠 + 挂载前缀 + 内部路径）", got, want)
	}
	if got := SelfAddrForRouting(); got != "10.0.0.1:8900" {
		t.Errorf("设置对外入口把 自身直连地址 改成了 %q：两者必须正交", got)
	}

	// 只给 host:port 时按 http:// 补全（前缀仍由 SetPathPrefix 提供，不在 base 里拼）。
	SetPublicBaseURL("10.1.2.3:8080")
	if got, want := terminalURL("sid-2"), "http://10.1.2.3:8080/mcp/view/terminal/sid-2"; got != want {
		t.Errorf("terminalURL = %q, want %q（缺 scheme 应补 http://）", got, want)
	}

	// 反过来：先设入口再设 自身直连地址，也不该互相污染。
	SetPublicBaseURL("")
	SetSelfAddr("")
	SetPathPrefix("")
	SetPublicBaseURL("http://vip.example.com")
	SetSelfAddr("10.0.0.2:8900")
	if got := SelfAddrForRouting(); got != "10.0.0.2:8900" {
		t.Errorf("自身直连地址 = %q，want 10.0.0.2:8900", got)
	}
	if got, want := terminalURL("sid-3"), "http://vip.example.com/view/terminal/sid-3"; got != want {
		t.Errorf("terminalURL = %q, want %q", got, want)
	}
}

func TestURLHostPort(t *testing.T) {
	// 非通配地址原样保留 host:port。
	if got := urlHostPort("127.0.0.1:8900"); got != "127.0.0.1:8900" {
		t.Fatalf("urlHostPort(127.0.0.1:8900)=%q, want unchanged", got)
	}
	// 通配地址：host 应被替换掉 0.0.0.0（若本机有可用 IP），端口必须保留。
	got := urlHostPort("0.0.0.0:8013")
	if !strings.HasSuffix(got, ":8013") {
		t.Fatalf("urlHostPort wildcard lost port: %q", got)
	}
	if strings.HasPrefix(got, "0.0.0.0:") && localIP() != "" {
		t.Fatalf("wildcard host not replaced though localIP=%q: %q", localIP(), got)
	}
	// 非 host:port 形态原样返回。
	if got := urlHostPort("not-a-hostport"); got != "not-a-hostport" {
		t.Fatalf("urlHostPort passthrough failed: %q", got)
	}
}

func TestListFilfersByOwner(t *testing.T) {
	InitStore(10)
	SetSelfAddr("")
	a := &Session{ID: "a", Owner: "alice", Status: "ready"}
	b := &Session{ID: "b", Owner: "bob", Status: "ready"}
	theStore.add(a)
	theStore.add(b)

	got := List("alice")
	if len(got) != 1 || got[0]["session_id"] != "a" {
		t.Fatalf("List(alice) = %v", got)
	}
}

func TestOwnerLookup(t *testing.T) {
	InitStore(10)
	theStore.add(&Session{ID: "x", Owner: "alice"})
	owner, ok := Owner("x")
	if !ok || owner != "alice" {
		t.Fatalf("Owner(x) = (%q,%v)", owner, ok)
	}
	if _, ok := Owner("missing"); ok {
		t.Fatalf("Owner(missing) should be false")
	}
}

func TestReachableHostPort(t *testing.T) {
	// 显式 host 原样保留（含端口）。
	if got := ReachableHostPort("10.0.0.7:8900"); got != "10.0.0.7:8900" {
		t.Fatalf("explicit host = %q", got)
	}
	// 回环保留（单机场景）。
	if got := ReachableHostPort("127.0.0.1:8900"); got != "127.0.0.1:8900" {
		t.Fatalf("loopback = %q", got)
	}
	// 通配 host 应被替换为非通配（自动探测本机 IP），端口保持不变。
	got := ReachableHostPort("0.0.0.0:8900")
	if _, port, err := net.SplitHostPort(got); err != nil || port != "8900" {
		t.Fatalf("wildcard resolved = %q (port must stay 8900)", got)
	}
	// 仅当本机探测到具体 IP 时才断言通配 host 已被替换（隔离环境可能无非回环网卡）。
	if localIP() != "" && strings.HasPrefix(got, "0.0.0.0:") {
		t.Fatalf("wildcard host should be resolved to a concrete IP, got %q", got)
	}
}
