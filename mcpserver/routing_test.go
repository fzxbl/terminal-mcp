package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fzxbl/terminal-mcp/internal/session"
)

func toolsCallBody(sessionID string) string {
	return `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"terminal_status","arguments":{"session_id":"` + sessionID + `"}}}`
}

func TestRoutingLocalPassthrough(t *testing.T) {
	session.SetSelfAddr("10.0.0.1:8900")
	defer session.SetSelfAddr("")
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(200) })
	mw := sessionRoutingMiddleware(next)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(toolsCallBody("10.0.0.1:8900~uuid-1")))
	mw.ServeHTTP(httptest.NewRecorder(), req)
	if !called {
		t.Fatalf("local session should pass through to next")
	}
}

func TestRoutingForwardLoopGuard(t *testing.T) {
	session.SetSelfAddr("10.0.0.1:8900")
	defer session.SetSelfAddr("")
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(200) })
	mw := sessionRoutingMiddleware(next)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(toolsCallBody("10.0.0.9:8900~uuid-2")))
	req.Header.Set(forwardedHeader, "1")
	mw.ServeHTTP(httptest.NewRecorder(), req)
	if !called {
		t.Fatalf("forwarded request must be handled locally, not re-proxied")
	}
}

func TestRoutingReverseProxy(t *testing.T) {
	session.SetSelfAddr("10.0.0.1:8900")
	defer session.SetSelfAddr("")
	backendHit := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendHit = true
		if r.Header.Get(forwardedHeader) != "1" {
			t.Errorf("forwarded header not set on proxied request")
		}
		w.WriteHeader(200)
	}))
	defer backend.Close()
	owner := strings.TrimPrefix(backend.URL, "http://") // host:port
	setPeers([]string{owner})                           // 白名单允许反代到该 backend
	defer setPeers(nil)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Errorf("should not reach next") })
	mw := sessionRoutingMiddleware(next)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(toolsCallBody(owner+"~uuid-3")))
	mw.ServeHTTP(httptest.NewRecorder(), req)
	if !backendHit {
		t.Fatalf("request to remote-owned session should be reverse-proxied")
	}
}

// SSRF 防护：session_id 里的 token 不在 peers 白名单时，绝不反代（不 dial 任意地址），交本地处理。
func TestRoutingUnknownTargetNotProxied(t *testing.T) {
	session.SetSelfAddr("10.0.0.1:8900")
	defer session.SetSelfAddr("")
	setPeers(nil) // 无白名单
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(200) })
	mw := sessionRoutingMiddleware(next)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(toolsCallBody("169.254.169.254:80~evil")))
	mw.ServeHTTP(httptest.NewRecorder(), req)
	if !called {
		t.Fatalf("unknown (non-peer) target must be handled locally, never proxied (SSRF guard)")
	}
}

// WithSessionRouting 是导出的包装器，供嵌入式宿主接入分布式路由；行为等同内部中间件。
func TestWithSessionRoutingProxies(t *testing.T) {
	session.SetSelfAddr("10.0.0.1:8900")
	defer session.SetSelfAddr("")
	backendHit := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendHit = true
		w.WriteHeader(200)
	}))
	defer backend.Close()
	owner := strings.TrimPrefix(backend.URL, "http://")
	setPeers([]string{owner})
	defer setPeers(nil)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Errorf("should not reach next") })
	mw := WithSessionRouting(next)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(toolsCallBody(owner+"~uuid-9")))
	mw.ServeHTTP(httptest.NewRecorder(), req)
	if !backendHit {
		t.Fatalf("WithSessionRouting should reverse-proxy remote-owned sessions")
	}
}

// terminalPath 拼出宿主挂载后的网页终端实际路径（本仓库标准挂法是 <前缀>/view/terminal/<id>）。
func terminalPath(prefix, sessionID string) string {
	return prefix + "/view/terminal/" + sessionID
}

// TestTerminalRoutingProxiesToOwnerKeepingPath：网页终端的属主信息在路径里，非本机时必须
// 反代到属主，且**保留原始路径**——各节点挂载点相同，改写路径会打到不存在的端点上。
//
// 这条是「terminal_url 不必写成本节点直连地址」的前提：人的浏览器只能访问统一入口，
// 落到哪个节点是随机的，没有这层路由，(N-1)/N 的点击会看到「会话不存在」。
func TestTerminalRoutingProxiesToOwnerKeepingPath(t *testing.T) {
	session.SetSelfAddr("10.0.0.1:8900")
	defer session.SetSelfAddr("")
	var gotPath, gotForwarded string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotForwarded = r.Header.Get(forwardedHeader)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()
	owner := strings.TrimPrefix(backend.URL, "http://")
	setPeers([]string{owner})
	defer setPeers(nil)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("归属兄弟节点的会话不该由本节点处理")
	})
	sid := owner + "~uuid-page"
	path := terminalPath("/mcp", sid)
	rec := httptest.NewRecorder()
	WithTerminalRouting(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("状态码 %d，want 204（属主的应答）", rec.Code)
	}
	if gotPath != path {
		t.Errorf("转发路径 = %q，want %q（保留原路径）", gotPath, path)
	}
	if gotForwarded == "" {
		t.Error("转发请求必须带防环标记")
	}
}

// TestTerminalRoutingLocalCases：这三种情况都必须留在本地处理。
func TestTerminalRoutingLocalCases(t *testing.T) {
	session.SetSelfAddr("10.0.0.1:8900")
	defer session.SetSelfAddr("")
	setPeers([]string{"10.0.0.9:8900"})
	defer setPeers(nil)

	cases := []struct {
		name string
		path string
		hdr  bool
	}{
		{"属主就是本节点", terminalPath("/mcp", "10.0.0.1:8900~uuid-local"), false},
		{"属主不在白名单（SSRF 防护）", terminalPath("/mcp", "169.254.169.254:80~evil"), false},
		{"路径里没有会话 id", "/mcp/view/terminal/", false},
		{"已被转发过（防环）", terminalPath("/mcp", "10.0.0.9:8900~uuid-loop"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
			req := httptest.NewRequest(http.MethodGet, c.path, nil)
			if c.hdr {
				req.Header.Set(forwardedHeader, "1")
			}
			WithTerminalRouting(next).ServeHTTP(httptest.NewRecorder(), req)
			if !called {
				t.Error("该请求应由本节点本地处理")
			}
		})
	}
}

// TestSessionIDFromPath：子资源（stream/ws/takeover）也要能取到同一个会话 id——
// 接管用的是 WebSocket、只读流是 SSE，它们和页面必须落到同一个节点。
func TestSessionIDFromPath(t *testing.T) {
	const sid = "10.0.0.1:8900~uuid-1"
	for _, p := range []string{
		"/terminal/" + sid,
		"/view/terminal/" + sid,
		"/mcp/view/terminal/" + sid,
		"/mcp/view/terminal/" + sid + "/stream",
		"/mcp/view/terminal/" + sid + "/ws",
		"/mcp/view/terminal/" + sid + "/takeover",
	} {
		if got := sessionIDFromPath(p); got != sid {
			t.Errorf("sessionIDFromPath(%q) = %q, want %q", p, got, sid)
		}
	}
	for _, p := range []string{"/mcp", "/view/terminal/", "/other/x"} {
		if got := sessionIDFromPath(p); got != "" {
			t.Errorf("sessionIDFromPath(%q) = %q, want 空串", p, got)
		}
	}
}
