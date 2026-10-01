package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/fzxbl/terminal-mcp/internal/session"
)

// TestWSUpgradeThroughFullMount 复现浏览器人工接管输入的真实链路：
// 完整的 NewHTTPHandler mux + /view 前缀剥离 + withTerminalRouting + 带节点前缀(含冒号)的
// session_id + 浏览器会发的 Origin 头 + subprotocol 能力票。
// 既有单测只直接打 TerminalHandler("/terminal/<裸uuid>/ws")，这些层一个都没覆盖。
func TestWSUpgradeThroughFullMount(t *testing.T) {
	// 模拟独立进程 --listen 后 SetSelfAddr 的效果：session_id 形如 host:port~uuid（带冒号）。
	session.SetSelfAddr("10.229.133.11:8900")
	defer session.SetSelfAddr("")

	id, key, err := session.OpenLocalForTestWithCapability()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s := session.Lookup(id); s != nil {
			s.SetHold(false)
		}
		session.Close(id)
	}()
	if !strings.Contains(id, "10.229.133.11:8900~") {
		t.Fatalf("expected host-prefixed session id, got %q", id)
	}
	for i := 0; i < 200; i++ {
		if session.Status(id).State == "idle" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	srv := httptest.NewServer(NewHTTPHandler(nil))
	defer srv.Close()

	owner := "browser-A"
	// 先接管（走完整 /view 路径）。
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/view/terminal/"+id+"/takeover?owner="+owner,
		strings.NewReader(`{"on":true,"owner":"`+owner+`"}`))
	req.Header.Set("X-Terminal-Capability", key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("takeover status = %d, want 200", resp.StatusCode)
	}

	// 像浏览器一样连 ws：带 Origin 头 + 能力 subprotocol，走 /view/terminal/<id>/ws。
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/view/terminal/" + id + "/ws?owner=" + owner
	d := websocket.Dialer{Subprotocols: []string{"terminal-capability." + key}}
	hdr := http.Header{"Origin": {srv.URL}}
	conn, wsResp, err := d.Dial(wsURL, hdr)
	if err != nil {
		code := 0
		if wsResp != nil {
			code = wsResp.StatusCode
		}
		t.Fatalf("ws dial through full mount failed: err=%v status=%d", err, code)
	}
	defer conn.Close()
	// 浏览器会在「提供了 subprotocol 但服务端一个都没回选」时直接 Fail 握手
	// （Chrome: "Sent non-empty 'Sec-WebSocket-Protocol' header but no response was received"）。
	// Go 客户端对缺省回显是容忍的，所以这问题只在浏览器复现。断言服务端回显了能力 subprotocol。
	if got := conn.Subprotocol(); got != "terminal-capability."+key {
		t.Fatalf("server did not echo capability subprotocol: got %q, want %q", got, "terminal-capability."+key)
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"t":"in","d":"x"}`)); err != nil {
		t.Fatalf("ws write input failed: %v", err)
	}
}
