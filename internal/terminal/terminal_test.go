package terminal

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/fzxbl/terminal-mcp/internal/session"
)

var testKeys sync.Map

// openLocalReady 起本地 bash 会话并等到 idle，返回会话 id。
func openLocalReady(t *testing.T) string {
	t.Helper()
	id, key, err := session.OpenLocalForTestWithCapability()
	if err != nil {
		t.Fatal(err)
	}
	testKeys.Store(id, key)
	for i := 0; i < 200; i++ {
		if env := session.Status(id); env.State == "idle" {
			return id
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("session not idle")
	return ""
}

func testKey(id string) string {
	v, _ := testKeys.Load(id)
	return v.(string)
}

func authorizedRequest(method, url, body, key string) *http.Request {
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("X-Terminal-Capability", key)
	return req
}

func authorizedWS(id, url string) (*websocket.Conn, *http.Response, error) {
	d := websocket.Dialer{Subprotocols: []string{"terminal-capability." + testKey(id)}}
	return d.Dial(url, nil)
}

func TestWebCapabilityRequired(t *testing.T) {
	id := openLocalReady(t)
	defer closeReleasing(id)
	srv := httptest.NewServer(TerminalHandler())
	defer srv.Close()
	for _, path := range []string{"/stream", "/takeover"} {
		resp, err := http.Get(srv.URL + "/terminal/" + id + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("ID-only GET %s returned %d, want 404", path, resp.StatusCode)
		}
		wrong := authorizedRequest(http.MethodGet, srv.URL+"/terminal/"+id+path, "", "wrong-session-key")
		wrongResp, err := http.DefaultClient.Do(wrong)
		if err != nil {
			t.Fatal(err)
		}
		wrongResp.Body.Close()
		if wrongResp.StatusCode != http.StatusNotFound {
			t.Fatalf("wrong-key GET %s returned %d, want 404", path, wrongResp.StatusCode)
		}
	}
	post, err := http.Post(srv.URL+"/terminal/"+id+"/takeover", "application/json", strings.NewReader(`{"on":true}`))
	if err != nil {
		t.Fatal(err)
	}
	post.Body.Close()
	if post.StatusCode != http.StatusNotFound || session.Lookup(id).Held() {
		t.Fatalf("ID-only takeover POST status=%d held=%v", post.StatusCode, session.Lookup(id).Held())
	}
	wrongPost := authorizedRequest(http.MethodPost, srv.URL+"/terminal/"+id+"/takeover", `{"on":true}`, "wrong-session-key")
	wrongResp, err := http.DefaultClient.Do(wrongPost)
	if err != nil {
		t.Fatal(err)
	}
	wrongResp.Body.Close()
	if wrongResp.StatusCode != http.StatusNotFound || session.Lookup(id).Held() {
		t.Fatalf("wrong-key takeover POST status=%d held=%v", wrongResp.StatusCode, session.Lookup(id).Held())
	}
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/terminal/" + id + "/ws"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("ID-only WebSocket should be rejected with 404, response=%v err=%v", resp, err)
	}
	wrongDialer := websocket.Dialer{Subprotocols: []string{"terminal-capability.wrong-session-key"}}
	_, resp, err = wrongDialer.Dial(wsURL, nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("wrong-key WebSocket should be rejected with 404, response=%v err=%v", resp, err)
	}
}

// closeReleasing 先清接管标志（否则 Close 被 held 拦截），再关闭会话。
func closeReleasing(id string) {
	if s := session.Lookup(id); s != nil {
		s.SetHold(false)
	}
	session.Close(id)
}

func TestTerminalPageServed(t *testing.T) {
	id := openLocalReady(t)
	defer session.Close(id)
	srv := httptest.NewServer(TerminalHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/terminal/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	page := string(body)
	if regexp.MustCompile(`(?i)(?:src|href)\s*=\s*["']https?://|url\(\s*["']?https?://|@import\s+(?:url\()?\s*["']?https?://`).MatchString(page) {
		t.Fatal("rendered terminal page contains an external HTTP(S) asset URL")
	}
	if !strings.Contains(page, id) {
		t.Fatalf("page missing session id: %q", page[:min(200, len(page))])
	}
	if !strings.Contains(page, "人工接管") || !strings.Contains(page, "/ws") || !strings.Contains(page, "xterm") {
		t.Fatalf("page missing takeover UI / xterm")
	}
	if !strings.Contains(page, "location.hash.slice(1)") || !strings.Contains(page, "X-Terminal-Capability") || !strings.Contains(page, "terminal-capability.") {
		t.Fatal("page does not read fragment capability and send it to protected requests")
	}
	for _, localAsset := range []string{"assets/xterm-6.0.0.css", "assets/xterm-6.0.0.js", "assets/addon-fit-0.11.0.js", "assets/addon-webgl-0.19.0.js"} {
		if !strings.Contains(page, localAsset) {
			t.Errorf("page does not reference local asset %q", localAsset)
		}
	}
	csp := resp.Header.Get("Content-Security-Policy")
	directives := strings.Split(csp, ";")
	if csp == "" || len(directives) <= 5 || !strings.Contains(directives[5], "script-src 'nonce-") || strings.Contains(directives[5], "unsafe-inline") {
		t.Fatalf("page CSP does not restrict executable script to the per-response nonce: %q", csp)
	}
	if strings.Count(page, `nonce="`) < 4 || !strings.Contains(page, `<base href="./">`) {
		t.Fatal("page inline script nonce or relative asset base missing")
	}
}

func TestTerminalAssetsServedFromEmbeddedFiles(t *testing.T) {
	srv := httptest.NewServer(TerminalHandler())
	defer srv.Close()
	for _, asset := range []string{"xterm-6.0.0.css", "xterm-6.0.0.js", "addon-fit-0.11.0.js", "addon-webgl-0.19.0.js"} {
		resp, err := http.Get(srv.URL + "/terminal/assets/" + asset)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || len(data) == 0 || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("asset %s: status=%d bytes=%d nosniff=%q", asset, resp.StatusCode, len(data), resp.Header.Get("X-Content-Type-Options"))
		}
	}
}

func TestTerminalPageUnknownSession404(t *testing.T) {
	srv := httptest.NewServer(TerminalHandler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/terminal/does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

func TestTakeoverEndpointTogglesHold(t *testing.T) {
	id := openLocalReady(t)
	defer closeReleasing(id)
	srv := httptest.NewServer(TerminalHandler())
	defer srv.Close()

	resp, err := http.DefaultClient.Do(authorizedRequest(http.MethodPost, srv.URL+"/terminal/"+id+"/takeover", `{"on":true}`, testKey(id)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !session.Lookup(id).Held() {
		t.Fatal("session should be held after takeover on")
	}

	resp2, err := http.DefaultClient.Do(authorizedRequest(http.MethodPost, srv.URL+"/terminal/"+id+"/takeover", `{"on":false}`, testKey(id)))
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if session.Lookup(id).Held() {
		t.Fatal("session should not be held after takeover off")
	}
}

func TestTerminalStreamPushesScrollback(t *testing.T) {
	id := openLocalReady(t)
	defer session.Close(id)
	session.Send(id, "echo streammark123", 5000)

	srv := httptest.NewServer(TerminalHandler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/terminal/"+id+"/stream", nil)
	req.Header.Set("X-Terminal-Capability", testKey(id))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	sc := bufio.NewScanner(resp.Body)
	var lastEvent, decoded string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			lastEvent = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: ") && lastEvent == "data":
			if b, e := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "data: ")); e == nil {
				decoded += string(b)
			}
		}
		if strings.Contains(decoded, "streammark123") {
			return // found the buffered output pushed as scrollback
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("stream scan failed: %v; got %q", err, decoded)
	}
	t.Fatalf("stream did not deliver scrollback containing marker; got %q", decoded)
}

func TestWSInputWritesToPTYWhenHeld(t *testing.T) {
	id := openLocalReady(t)
	defer closeReleasing(id)
	session.Lookup(id).SetHold(true)

	srv := httptest.NewServer(TerminalHandler())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/terminal/" + id + "/ws"
	conn, _, err := authorizedWS(id, wsURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"t":"in","d":"echo wsmark123\r"}`)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s := session.Lookup(id); s != nil && strings.Contains(s.Since(0), "wsmark123") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("ws input not reflected in PTY buffer")
}

func TestWSInputRejectedWhenNotHeld(t *testing.T) {
	id := openLocalReady(t)
	defer session.Close(id)
	srv := httptest.NewServer(TerminalHandler())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/terminal/" + id + "/ws"
	_, resp, err := authorizedWS(id, wsURL)
	if err == nil {
		t.Fatal("dial should fail when session not held")
	}
	if resp == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("want 409 Conflict, got %v", resp)
	}
}

func TestTakeoverGETReturnsHeld(t *testing.T) {
	id := openLocalReady(t)
	defer closeReleasing(id)
	srv := httptest.NewServer(TerminalHandler())
	defer srv.Close()
	session.Lookup(id).SetHold(true)
	req := authorizedRequest(http.MethodGet, srv.URL+"/terminal/"+id+"/takeover", "", testKey(id))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "\"held\":true") {
		t.Fatalf("GET takeover should report held, got %s", body)
	}
}

func TestWSResizeThenInput(t *testing.T) {
	id := openLocalReady(t)
	defer closeReleasing(id)
	session.Lookup(id).SetHold(true)

	srv := httptest.NewServer(TerminalHandler())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/terminal/" + id + "/ws"
	conn, _, err := authorizedWS(id, wsURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// resize 帧不应中断连接；随后的 in 帧仍应写入 PTY。
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"t":"resize","cols":120,"rows":40}`)); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"t":"in","d":"echo rzmark456\r"}`)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s := session.Lookup(id); s != nil && strings.Contains(s.Since(0), "rzmark456") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("input after resize not reflected in PTY buffer")
}

func TestStreamHistoricalWhenClosed(t *testing.T) {
	id, key, err := session.OpenLocalForTestWithCapability()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if env := session.Status(id); env.State == "idle" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	session.Send(id, "echo HISTMARK", 3000)
	session.Close(id)

	req := httptest.NewRequest(http.MethodGet, "/terminal/"+id+"/stream", nil)
	req.Header.Set("X-Terminal-Capability", key)
	rr := httptest.NewRecorder()
	TerminalHandler().ServeHTTP(rr, req)
	body := rr.Body.String()
	if !strings.Contains(body, "disconnected") {
		t.Fatalf("stream missing disconnected state: %q", body)
	}
	if !strings.Contains(body, "event: data") {
		t.Fatalf("stream missing historical data event: %q", body)
	}
}

func TestTakeoverRejectedWhenClosed(t *testing.T) {
	id, key, _ := session.OpenLocalForTestWithCapability()
	session.Close(id)
	req := httptest.NewRequest(http.MethodPost, "/terminal/"+id+"/takeover",
		strings.NewReader(`{"on":true}`))
	req.Header.Set("X-Terminal-Capability", key)
	rr := httptest.NewRecorder()
	TerminalHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict && rr.Code != http.StatusNotFound {
		t.Fatalf("takeover on closed session code = %d, want 409/404", rr.Code)
	}
}

// TestTakeoverSingleOwnerEnforced 校验单人持有：他人接管被拒、非持有者不能释放、持有者释放后他人可接管。
func TestTakeoverSingleOwnerEnforced(t *testing.T) {
	id := openLocalReady(t)
	defer closeReleasing(id)
	srv := httptest.NewServer(TerminalHandler())
	defer srv.Close()

	post := func(body string) int {
		resp, err := http.DefaultClient.Do(authorizedRequest(http.MethodPost, srv.URL+"/terminal/"+id+"/takeover", body, testKey(id)))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if code := post(`{"on":true,"owner":"A"}`); code != http.StatusOK {
		t.Fatalf("A takeover want 200, got %d", code)
	}
	if session.Lookup(id).HoldOwner() != "A" {
		t.Fatal("owner should be A")
	}
	// B 试图接管 → 409，持有者仍为 A
	if code := post(`{"on":true,"owner":"B"}`); code != http.StatusConflict {
		t.Fatalf("B takeover want 409, got %d", code)
	}
	if session.Lookup(id).HoldOwner() != "A" {
		t.Fatal("owner should remain A after B rejected")
	}
	// B 试图释放 A 的接管 → 409，仍处于接管态
	if code := post(`{"on":false,"owner":"B"}`); code != http.StatusConflict {
		t.Fatalf("B release want 409, got %d", code)
	}
	if !session.Lookup(id).Held() {
		t.Fatal("should still be held after B's failed release")
	}
	// A 释放后 B 可接管
	if code := post(`{"on":false,"owner":"A"}`); code != http.StatusOK {
		t.Fatalf("A release want 200, got %d", code)
	}
	if code := post(`{"on":true,"owner":"B"}`); code != http.StatusOK {
		t.Fatalf("B takeover after release want 200, got %d", code)
	}
	if session.Lookup(id).HoldOwner() != "B" {
		t.Fatal("owner should be B after A released")
	}
}

// TestWSInputRejectedForNonOwner 校验接管有归属时仅持有者可连上写通道，他人被拒。
func TestWSInputRejectedForNonOwner(t *testing.T) {
	id := openLocalReady(t)
	defer closeReleasing(id)
	if ok, _ := session.Lookup(id).AcquireHold("A"); !ok {
		t.Fatal("A acquire failed")
	}
	srv := httptest.NewServer(TerminalHandler())
	defer srv.Close()
	base := "ws" + strings.TrimPrefix(srv.URL, "http") + "/terminal/" + id + "/ws"

	_, resp, err := authorizedWS(id, base+"?owner=B")
	if err == nil {
		t.Fatal("dial should fail for non-owner")
	}
	if resp == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("non-owner want 409, got %v", resp)
	}
	conn, _, err := authorizedWS(id, base+"?owner=A")
	if err != nil {
		t.Fatalf("owner dial should succeed: %v", err)
	}
	conn.Close()
}

// TestTakeoverGETReportsMine 校验 GET 依 owner 回传 mine（本浏览器是否为当前持有者）。
func TestTakeoverGETReportsMine(t *testing.T) {
	id := openLocalReady(t)
	defer closeReleasing(id)
	session.Lookup(id).AcquireHold("A")
	srv := httptest.NewServer(TerminalHandler())
	defer srv.Close()
	get := func(q string) string {
		req := authorizedRequest(http.MethodGet, srv.URL+"/terminal/"+id+"/takeover"+q, "", testKey(id))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	if s := get("?owner=A"); !strings.Contains(s, "\"mine\":true") {
		t.Fatalf("owner A should be mine, got %s", s)
	}
	if s := get("?owner=B"); !strings.Contains(s, "\"mine\":false") {
		t.Fatalf("owner B should not be mine, got %s", s)
	}
}
