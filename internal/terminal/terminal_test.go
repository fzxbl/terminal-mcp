package terminal

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/fzxbl/terminal-mcp/internal/session"
)

// openLocalReady 起本地 bash 会话并等到 idle，返回会话 id。
func openLocalReady(t *testing.T) string {
	t.Helper()
	id, err := session.OpenLocalForTest()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		if env := session.Status(id); env.State == "idle" {
			return id
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("session not idle")
	return ""
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

	resp, err := http.Get(srv.URL + "/debug/terminal/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	page := string(body)
	if !strings.Contains(page, id) {
		t.Fatalf("page missing session id: %q", page[:min(200, len(page))])
	}
	if !strings.Contains(page, "人工接管") || !strings.Contains(page, "/ws") || !strings.Contains(page, "xterm") {
		t.Fatalf("page missing takeover UI / xterm")
	}
}

func TestTerminalPageUnknownSession404(t *testing.T) {
	srv := httptest.NewServer(TerminalHandler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/debug/terminal/does-not-exist")
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

	resp, err := http.Post(srv.URL+"/debug/terminal/"+id+"/takeover",
		"application/json", strings.NewReader(`{"on":true}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !session.Lookup(id).Held() {
		t.Fatal("session should be held after takeover on")
	}

	resp2, err := http.Post(srv.URL+"/debug/terminal/"+id+"/takeover",
		"application/json", strings.NewReader(`{"on":false}`))
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
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/debug/terminal/"+id+"/stream", nil)
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
	t.Fatalf("stream did not deliver scrollback containing marker; got %q", decoded)
}

func TestWSInputWritesToPTYWhenHeld(t *testing.T) {
	id := openLocalReady(t)
	defer closeReleasing(id)
	session.Lookup(id).SetHold(true)

	srv := httptest.NewServer(TerminalHandler())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/debug/terminal/" + id + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
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
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/debug/terminal/" + id + "/ws"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
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
	resp, err := http.Get(srv.URL + "/debug/terminal/" + id + "/takeover")
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
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/debug/terminal/" + id + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
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
	id, err := session.OpenLocalForTest()
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

	req := httptest.NewRequest(http.MethodGet, "/debug/terminal/"+id+"/stream", nil)
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
	id, _ := session.OpenLocalForTest()
	session.Close(id)
	req := httptest.NewRequest(http.MethodPost, "/debug/terminal/"+id+"/takeover",
		strings.NewReader(`{"on":true}`))
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
		resp, err := http.Post(srv.URL+"/debug/terminal/"+id+"/takeover",
			"application/json", strings.NewReader(body))
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
	base := "ws" + strings.TrimPrefix(srv.URL, "http") + "/debug/terminal/" + id + "/ws"

	_, resp, err := websocket.DefaultDialer.Dial(base+"?owner=B", nil)
	if err == nil {
		t.Fatal("dial should fail for non-owner")
	}
	if resp == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("non-owner want 409, got %v", resp)
	}
	conn, _, err := websocket.DefaultDialer.Dial(base+"?owner=A", nil)
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
		resp, err := http.Get(srv.URL + "/debug/terminal/" + id + "/takeover" + q)
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
