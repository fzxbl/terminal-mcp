package mcpserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fzxbl/terminal-mcp/internal/session"
)

// 节点 B（真实 NewHTTPHandler）收到属主为"节点 A"的 session_id 的 tools/call，
// 应通过已接入的路由中间件反代到 A，并带上防环转发头。
func TestTwoNodeReverseProxyThroughHandler(t *testing.T) {
	Init("")
	defer session.SetNodeToken("")

	// 假节点 A：仅用于接住被反代过来的请求。
	var aHit atomic.Bool
	nodeA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		aHit.Store(true)
		if r.Header.Get(forwardedHeader) != "1" {
			t.Errorf("proxied request to A missing forwarded header")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"state":"ready"}}}`)
	}))
	defer nodeA.Close()
	aHost := strings.TrimPrefix(nodeA.URL, "http://")

	// 节点 B：真实 handler，本机 token = bHost。必须在构建 handler 前设置好本机 token。
	session.SetNodeToken("10.255.255.255:1") // 一个绝不等于 aHost 的本机 token
	setPeers([]string{aHost})                // 把 A 加入白名单，允许被反代（SSRF 防护要求）
	defer setPeers(nil)
	nodeB := httptest.NewServer(NewHTTPHandler(nil))
	defer nodeB.Close()

	// 向 B 发一个属主为 A 的 session_id 的 tools/call。
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"terminal_status","arguments":{"session_id":"` + aHost + `~uuid-x"}}}`
	req, _ := http.NewRequest(http.MethodPost, nodeB.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("X-MCP-USER", "alice")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request to B failed: %v", err)
	}
	resp.Body.Close()

	if !aHit.Load() {
		t.Fatalf("B did not reverse-proxy the remote-owned session to A")
	}
}
