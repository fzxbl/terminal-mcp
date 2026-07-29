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
	session.SetNodeToken("10.0.0.1:8900")
	defer session.SetNodeToken("")
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
	session.SetNodeToken("10.0.0.1:8900")
	defer session.SetNodeToken("")
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
	session.SetNodeToken("10.0.0.1:8900")
	defer session.SetNodeToken("")
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
	setPeers([]string{owner})                            // 白名单允许反代到该 backend
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
	session.SetNodeToken("10.0.0.1:8900")
	defer session.SetNodeToken("")
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
	session.SetNodeToken("10.0.0.1:8900")
	defer session.SetNodeToken("")
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
