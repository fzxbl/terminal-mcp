package mcpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/fzxbl/terminal-mcp/internal/session"
)

// forwardedHeader 标记"本请求已被某节点反代过"，防止转发环路。
const forwardedHeader = "X-Pty-Bridge-Forwarded"

// WithSessionRouting 用会话级路由中间件包裹上游 MCP handler：解析 tools/call 里的 session_id，
// 属主非本机则把整条请求反向代理到属主节点。供嵌入式宿主（自行挂载 /mcp）接入分布式路由用——
// 把它套在你的 MCP handler 外层即可：mux.Handle("/mcp", mcpserver.WithSessionRouting(myHandler))。
// 本机 token 由 session.SelfAddrForRouting() 实时读取（须先经 SetSelfAddr 设定）。
//
// 网页终端（页面/SSE/WebSocket）不是 tools/call，由 MountWebTerminal 挂载。
func WithSessionRouting(next http.Handler) http.Handler { return sessionRoutingMiddleware(next) }

// withTerminalRouting 用会话级路由包裹**网页终端** handler：属主信息就在路径里
// （…/terminal/<session_id>[/stream|/ws|/takeover]），属主非本机时把整条请求反代过去。
//
// 存在的理由：会话（PTY + 子进程）只活在开它的那个节点上，而 terminal_url 是给人点的——
// 人的浏览器多半只能访问统一入口（域名/VIP），落到哪个节点是随机的。有了这条，terminal_url
// 就不必写成「本节点直连地址」，直连地址退回只做节点间拨号。
//
// 它只由 MountWebTerminal 装配，宿主不应自行组合网页终端路由。它不读 body，故 GET（页面/SSE）、
// POST（接管态）、WebSocket 升级都适用。
func withTerminalRouting(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(forwardedHeader) != "" {
			next.ServeHTTP(w, r)
			return
		}
		token, _ := session.DecodeSessionID(sessionIDFromPath(r.URL.Path))
		if token == "" || token == session.SelfAddrForRouting() || !isAllowedProxyTarget(token) {
			// 未知/非兄弟节点一律本地处理（会返回 session not found），绝不 dial 任意地址：
			// 路径里的 token 是调用方可控的，不加白名单就是一个 SSRF 入口。
			next.ServeHTTP(w, r)
			return
		}
		proxyTo(w, r, token, nil)
	})
}

// sessionIDFromPath 从 …/terminal/<session_id>[/子资源] 里取出 session_id。
// 按 "/terminal/" 这一段定位而不是按固定前缀裁剪：外围前缀由宿主决定（/view/terminal/、
// /mcp/view/terminal/ 都合法），写死前缀会在换挂载点之后整体失效。
func sessionIDFromPath(p string) string {
	const seg = "/terminal/"
	i := strings.LastIndex(p, seg)
	if i < 0 {
		return ""
	}
	rest := p[i+len(seg):]
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// sessionRoutingMiddleware 解析 tools/call 里的 session_id，属主非本机则反代到属主节点。
// 本机 token 每次请求实时读取（SetSelfAddr 设定；未设置时按 listen_addr 推导）。
// 无 session_id / 非 tools/call / 已带转发标记 → 交给 next 本地处理。
func sessionRoutingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get(forwardedHeader) != "" {
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body)) // 复位，供后续处理再次读取

		sid := extractSessionID(body)
		if sid == "" {
			next.ServeHTTP(w, r)
			return
		}
		token, _ := session.DecodeSessionID(sid)
		if token == "" || token == session.SelfAddrForRouting() {
			next.ServeHTTP(w, r)
			return
		}
		// 安全：只允许反代到已知兄弟节点（peers）。token 来自客户端可控的 session_id，
		// 若不加白名单，攻击者可构造任意 host:port 让本节点主动外连（SSRF）。未知 token
		// 交本地处理（会返回 session not found），绝不 dial 任意地址。
		if !isAllowedProxyTarget(token) {
			next.ServeHTTP(w, r)
			return
		}
		proxyTo(w, r, token, body)
	})
}

// isAllowedProxyTarget 判定 token 是否为已配置的兄弟节点（peers）之一，仅这些地址允许被反代。
func isAllowedProxyTarget(token string) bool {
	for _, p := range peerList() {
		if p == token {
			return true
		}
	}
	return false
}

// extractSessionID 从 JSON-RPC 请求体解析 method==tools/call 时的 params.arguments.session_id。
func extractSessionID(body []byte) string {
	var msg struct {
		Method string `json:"method"`
		Params struct {
			Arguments struct {
				SessionID string `json:"session_id"`
			} `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		return ""
	}
	if msg.Method != "tools/call" {
		return ""
	}
	return msg.Params.Arguments.SessionID
}

// proxyTo 把请求单跳反代到属主节点，打转发标记防环，支持 SSE 流式与 WebSocket 升级。
//
// body 非 nil 表示「已被读过、需要重放」（tools/call 场景：解析 session_id 时读完了）；
// nil 表示不重放，ReverseProxy 直接转发 r.Body（网页终端场景，body 从未被读）。
//
// 路径保持 r.URL.Path 原样：各节点是同一份二进制、挂载点相同，因此宿主把 MCP 端点挂在
// 任何路径下都不会把请求反代到不存在的路径。
func proxyTo(w http.ResponseWriter, r *http.Request, token string, body []byte) {
	target := &url.URL{Scheme: "http", Host: token}
	rp := &httputil.ReverseProxy{
		FlushInterval: -1, // 立即冲刷，支持 text/event-stream
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			req.Header.Set(forwardedHeader, "1")
			if body != nil {
				req.Body = io.NopCloser(bytes.NewReader(body))
				req.ContentLength = int64(len(body))
			}
		},
	}
	rp.ServeHTTP(w, r)
}
