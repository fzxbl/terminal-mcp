package mcpserver

import (
	"context"
	"io"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fzxbl/terminal-mcp/internal/audit"
	"github.com/fzxbl/terminal-mcp/internal/config"
	"github.com/fzxbl/terminal-mcp/internal/session"
	"github.com/fzxbl/terminal-mcp/internal/terminal"
)

// serverName/serverVersion 为 MCP Implementation 元信息。
const (
	serverName    = "terminal-mcp"
	serverVersion = "v0.1.0"
)

// Init loads config from the given TOML path (empty string = defaults only)
// and initializes the session store. Must be called before RegisterTools or
// NewHTTPHandler.
func Init(configPath string) {
	c := config.Load(configPath)
	session.InitStore(c.MaxSessions)
	// 应用配置里的静态 peers（供 terminal_list 跨节点聚合与反代白名单）。嵌入宿主如需动态服务发现，
	// 可在 Init 后调用 SetPeerProvider 覆盖。identity 归属隔离由工具 handler 惰性读配置生效，无需在此处理。
	setPeers(c.Peers)
}

// RegisterTools registers all terminal_* and terminal_spill_explore tools on the given
// mcp.Server, letting you embed PTY terminal capabilities into your own MCP
// server. Call Init first.
//
// auditWriter receives JSON audit log entries (nil = discard).
func RegisterTools(server *mcp.Server, auditWriter io.Writer) {
	a := audit.New(discardIfNil(auditWriter))
	registerTools(server, a)
}

// NewHTTPHandler returns an http.Handler that serves the MCP Streamable HTTP
// endpoint at /mcp and the web terminal UI at /view/terminal/. Mount it in
// your own server or use standalone. Call Init first.
//
// auditWriter receives JSON audit log entries (nil = discard).
func NewHTTPHandler(auditWriter io.Writer) http.Handler {
	a := audit.New(discardIfNil(auditWriter))
	mux := http.NewServeMux()
	mux.Handle("/mcp", sessionRoutingMiddleware(newMCPStreamableHandler(a)))
	// 外围加 /view 前缀：StripPrefix 剥掉后交给按 /terminal/ 解析的 handler。
	mux.Handle("/view/terminal/", http.StripPrefix("/view", terminal.TerminalHandler()))
	return mux
}

// newMCPStreamableHandler 用官方 SDK 构建 Server、注册全部 terminal_* 工具，
// 返回 Streamable HTTP handler，并包一层中间件注入调用方 IP 供 per-tool 审计使用。
func newMCPStreamableHandler(a *audit.Logger) http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: serverVersion}, nil)
	registerTools(server, a)
	h := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	return remoteAddrMiddleware(h)
}

func discardIfNil(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// TerminalHandler returns just the web terminal (human-takeover) HTTP handler,
// which serves GET/POST/WebSocket under /terminal/. Use it when you embed
// the tools via RegisterTools onto your own MCP server (which does NOT include
// the web UI) and want to mount the terminal UI in your own router. Mount it at
// /terminal/, or under any prefix by stripping it first, e.g.
// http.StripPrefix("/view", TerminalHandler()) at /view/terminal/. Call Init first.
func TerminalHandler() http.Handler { return terminal.TerminalHandler() }

// SetAdvertiseAddr overrides the host:port used to build the terminal_url returned
// by terminal_open. Use it when embedding terminal-mcp behind another HTTP server
// whose bind address differs from this module's listen_addr (the host process owns
// the socket): set it to the host's real reachable host:port so the terminal_url is
// clickable. Empty string restores the listen_addr-based default. Concurrency-safe.
func SetAdvertiseAddr(hostPort string) { session.SetAdvertiseAddr(hostPort) }

// SetNodeToken 设置本节点对外可达地址（host:port），编码进 session_id 供跨节点路由。
// 未设置时回退到 advertise addr / listen_addr。
func SetNodeToken(hostPort string) { session.SetNodeToken(hostPort) }

// SetPeers 设置 terminal_list 跨节点聚合的兄弟节点列表（host:port）。静态列表，通常来自配置。
func SetPeers(p []string) { setPeers(p) }

// SetPeerProvider 注册动态兄弟节点发现函数：每次 terminal_list fan-out 与反代白名单校验时实时调用它，
// 返回当前可达的兄弟节点 host:port 列表。用它对接任意服务发现（Consul/etcd/DNS/K8s Endpoints 等），
// 无需依赖静态配置。传 nil 清除。与 SetPeers 互为覆盖，后调用者生效。
func SetPeerProvider(fn func() []string) { setPeerProvider(fn) }

// StartIdleGC starts the idle-session GC + transcript sweep goroutine; cancel ctx
// to stop it and reclaim all sessions. Embedded hosts should call it once after Init.
func StartIdleGC(ctx context.Context) { session.StartIdleGC(ctx) }

// Shutdown closes all sessions and reclaims child process groups (idempotent).
func Shutdown() { session.Shutdown() }
