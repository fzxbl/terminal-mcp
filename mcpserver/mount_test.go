package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fzxbl/terminal-mcp/internal/session"
)

// TestMountWebTerminalPrefixIsSingleSourceOfTruth：宿主只说一次「挂在哪」，
// 挂载 pattern、terminal_url 里的路径、跨节点转发看到的路径必须全部同源。
//
// 三处（mount 路径、StripPrefix、terminal_url 路径）任一与其它不一致，现象就是
// terminal_url 点开 404 或「会话不存在」，且只在多节点下暴露——所以这里一并锁死。
func TestMountWebTerminalPrefixIsSingleSourceOfTruth(t *testing.T) {
	t.Cleanup(func() {
		session.SetPathPrefix("")
		session.SetPublicBaseURL("")
		session.SetSelfAddr("")
	})
	session.SetPublicBaseURL("https://mcp.example.com")

	var mounted []string
	mux := http.NewServeMux()
	if err := MountWebTerminal("/mcp", func(pattern string, h http.Handler) {
		mounted = append(mounted, pattern)
		mux.Handle(pattern, h)
	}); err != nil {
		t.Fatalf("MountWebTerminal: %v", err)
	}
	if want := []string{"/mcp/view/terminal/"}; strings.Join(mounted, ",") != strings.Join(want, ",") {
		t.Fatalf("挂载的 pattern = %v，want %v", mounted, want)
	}
	if got, want := session.PathPrefix(), "/mcp"; got != want {
		t.Errorf("PathPrefix = %q, want %q", got, want)
	}

	// 属主是兄弟节点时，经宿主实际路径进来的请求必须被反代过去——这同时证明
	// 「pattern 挂对了」与「会话路由包在外层」。
	var ownerHit int
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ownerHit++
		if r.URL.Path != "/mcp/view/terminal/"+strings.TrimPrefix(r.URL.Path, "/mcp/view/terminal/") {
			t.Errorf("转发路径被改写了: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer owner.Close()
	ownerAddr := strings.TrimPrefix(owner.URL, "http://")
	session.SetSelfAddr("10.0.0.1:8900")
	setPeers([]string{ownerAddr})
	t.Cleanup(func() { setPeers(nil) })

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/mcp/view/terminal/"+ownerAddr+"~uuid-mounted", nil))
	if rec.Code != http.StatusNoContent || ownerHit != 1 {
		t.Fatalf("经挂载路径的跨节点请求没被反代：code=%d ownerHit=%d", rec.Code, ownerHit)
	}
}

// TestMountWebTerminalRejectsBadPrefix：前缀写错必须报错且一条路由都不挂，
// 而不是起来之后 terminal_url 静默 404。
func TestMountWebTerminalRejectsBadPrefix(t *testing.T) {
	t.Cleanup(func() { session.SetPathPrefix("") })
	for _, prefix := range []string{"mcp", "/mcp/", "/mcp/*", "/mcp view"} {
		mounted := 0
		err := MountWebTerminal(prefix, func(string, http.Handler) { mounted++ })
		if err == nil {
			t.Errorf("非法前缀 %q 却挂载成功", prefix)
			continue
		}
		if !strings.Contains(err.Error(), "挂载前缀") {
			t.Errorf("非法前缀 %q 的报错没点名挂载前缀: %v", prefix, err)
		}
		if mounted != 0 {
			t.Errorf("非法前缀 %q 仍挂上了 %d 条路由", prefix, mounted)
		}
	}
	if err := MountWebTerminal("/mcp", nil); err == nil {
		t.Error("mount 函数为 nil 却成功了")
	}
}
