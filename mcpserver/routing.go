package mcpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/fzxbl/terminal-mcp/internal/session"
)

// forwardedHeader 标记"本请求已被某节点反代过"，防止转发环路。
const forwardedHeader = "X-Pty-Bridge-Forwarded"

// WithSessionRouting 用会话级路由中间件包裹上游 MCP handler：解析 tools/call 里的 session_id，
// 属主非本机则把整条请求反向代理到属主节点。供嵌入式宿主（自行挂载 /mcp）接入分布式路由用——
// 把它套在你的 MCP handler 外层即可：mux.Handle("/mcp", mcpserver.WithSessionRouting(myHandler))。
// 本机 token 由 session.NodeTokenForRouting() 实时读取（须先经 SetNodeToken/SetAdvertiseAddr 设定）。
func WithSessionRouting(next http.Handler) http.Handler { return sessionRoutingMiddleware(next) }

// sessionRoutingMiddleware 解析 tools/call 里的 session_id，属主非本机则反代到属主节点。
// 本机 token 每次请求实时读取（SetNodeToken/SetAdvertiseAddr 设定）。
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
		if token == "" || token == session.NodeTokenForRouting() {
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

// proxyTo 把请求反代到属主节点的 /mcp，打转发标记防环，支持 SSE 流式。
func proxyTo(w http.ResponseWriter, r *http.Request, token string, body []byte) {
	target := &url.URL{Scheme: "http", Host: token}
	rp := &httputil.ReverseProxy{
		FlushInterval: -1, // 立即冲刷，支持 text/event-stream
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			req.URL.Path = "/mcp"
			req.Header.Set(forwardedHeader, "1")
			req.Body = io.NopCloser(bytes.NewReader(body))
			req.ContentLength = int64(len(body))
		},
	}
	rp.ServeHTTP(w, r)
}
