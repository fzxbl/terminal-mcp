package mcpserver

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fzxbl/terminal-mcp/internal/audit"
	"github.com/fzxbl/terminal-mcp/internal/config"
	"github.com/fzxbl/terminal-mcp/internal/identity"
	"github.com/fzxbl/terminal-mcp/internal/session"
)

// 输入结构体：json tag 决定 MCP 工具参数名（snake_case），jsonschema tag 为参数描述。
// 官方 SDK 的泛型 AddTool 会据此自动推断 input schema 并在进入 handler 前完成校验。

type openInput struct {
	Mode    string `json:"mode" jsonschema:"session mode: \"local\" (spawn a shell/command on this host) or \"ssh\" (ssh into host and run bash)"`
	Command string `json:"command,omitempty" jsonschema:"local mode only: optional command to run instead of the default shell; ignored for ssh mode"`
	Host    string `json:"host,omitempty" jsonschema:"ssh mode only: target host to ssh into; required when mode=ssh, ignored for local mode"`
}

type sendInput struct {
	SessionID string `json:"session_id" jsonschema:"the session id returned by terminal_open"`
	Input     string `json:"input" jsonschema:"the command line to type into the session (a trailing newline is added automatically)"`
	WaitMs    int    `json:"wait_ms,omitempty" jsonschema:"max milliseconds to block waiting for the command to settle (default 30000, capped by server max_block_seconds)"`
}

type readInput struct {
	SessionID  string `json:"session_id" jsonschema:"the session id"`
	WaitMs     int    `json:"wait_ms,omitempty" jsonschema:"max milliseconds to wait before returning (tail/since_last only; default 0)"`
	Mode       string `json:"mode,omitempty" jsonschema:"\"tail\" (default: peek at the tail to judge if the command finished; does NOT advance the cursor), \"since_last\" (deliver every new byte since the last since_last and advance the cursor), or \"explore\" (read-only exploration of an oversized result referenced by output_ref; does NOT advance the cursor)"`
	OutputRef  string `json:"output_ref,omitempty" jsonschema:"explore mode: the opaque reference returned in a truncated result"`
	Op         string `json:"op,omitempty" jsonschema:"explore mode: stat | read | grep"`
	LineOffset int    `json:"line_offset,omitempty" jsonschema:"explore read/grep start logical line (0-based; read accepts negative to count from the end)"`
	Limit      int    `json:"limit,omitempty" jsonschema:"explore: max lines (read) or max matches (grep)"`
	Pattern    string `json:"pattern,omitempty" jsonschema:"explore grep: Go regular expression"`
	Before     int    `json:"before,omitempty" jsonschema:"explore grep: context lines before each match"`
	After      int    `json:"after,omitempty" jsonschema:"explore grep: context lines after each match"`
	MaxBytes   int    `json:"max_bytes,omitempty" jsonschema:"explore: desired max body bytes (clamped to server hard cap)"`
}

type controlInput struct {
	SessionID string `json:"session_id" jsonschema:"the session id"`
	Key       string `json:"key" jsonschema:"control key or recovery action. Control keys (written to the PTY as the corresponding control byte): ctrl-c (SIGINT, interrupt the running command), ctrl-d (EOF, end input / exit a REPL or shell), ctrl-z (SIGTSTP, suspend to background), ctrl-\\ (SIGQUIT, quit with core), ctrl-l (clear screen), ctrl-u (erase to line start), ctrl-k (erase to line end), ctrl-a (move to line start), ctrl-e (move to line end), ctrl-w (erase previous word), ctrl-r (reverse history search), ctrl-g (bell / cancel current edit or search), tab (completion), esc (Escape), enter (Enter), backspace. Recovery actions: flush (drop queued input + clear the current line + Enter), hard (reopen the shell), rearm (re-inject the sentinel prompt after you switched into a new shell, e.g. after su/docker exec/matrix_jail, if the session appears stuck)."`
}

type sessionIDInput struct {
	SessionID string `json:"session_id" jsonschema:"the session id"`
}

type emptyInput struct{}

// listOutput 包装会话列表：官方 SDK 要求工具输出为 JSON object，顶层数组不被接受。
type listOutput struct {
	Sessions []map[string]string `json:"sessions" jsonschema:"active sessions with status snapshot (session_id, host, status, idle_seconds, held)"`
}

// 工具描述（英文）。涵盖 PTY 会话、local/ssh 模式、
// 人工接管 held 语义、read 的 tail vs since_last 差异等关键指引。
const (
	descOpen = "Start a persistent real PTY session and return {session_id, state, terminal_url}. " +
		"mode=local spawns a shell (or the given command) on this host; mode=ssh opens 'ssh <host>' running bash (host is required). " +
		"The session starts in state=loading; poll terminal_status until it becomes idle before interacting. " +
		"terminal_url is a read-only web terminal a human can open to watch the session live, and optionally 'take over' to type commands manually. " +
		"While a human has taken over, the session returns held=true and the model's send/close/control are blocked; use terminal_read(mode=since_last) to observe what the human is doing."

	descSend = "Type a command into the session and block up to wait_ms (capped by max_block_seconds) for it to settle. " +
		"Returns this command's new output plus state (running/idle/dead), prompt and exit_code. " +
		"state=running means the command has not finished yet (e.g. a large core still loading) - keep polling with terminal_read. " +
		"If the output is too large the return is truncated: truncated=true and an output_ref is returned, and that output has ALREADY advanced the delivery cursor; use terminal_read(mode=explore) with op=stat/grep/read to inspect it selectively. You can continue running commands afterwards. " +
		"If held=true the session is under human takeover: this call was NOT executed, do not retry write operations; wait or do other work and watch with terminal_read(mode=since_last) until held clears."

	descRead = "Observe session output without relying on injected markers. Three modes, do not mix them to 'fetch everything':\n" +
		"mode=tail (default, 'a quick glance'): returns only the last ~tail_bytes of the current screen to judge whether the command finished (check state and prompt). It does NOT advance the since_last cursor and can be called repeatedly.\n" +
		"mode=since_last ('fetch the complete increment'): returns every new byte since the previous since_last call, losing nothing, and advances the delivery cursor. If a single increment is too large the return is truncated (truncated=true + output_ref) and the cursor has already advanced past it.\n" +
		"mode=explore ('inspect an oversized result'): read-only exploration of the fixed snapshot referenced by output_ref; does NOT advance the cursor. Do NOT read the whole result sequentially into context: first op=stat (size/line_count/max_line_bytes), then op=grep (pattern, before/after) to locate, then op=read a local slice (line_offset, limit). Errors are usually at the end - use op=read with a negative line_offset. The next since_last will NOT re-return the output_ref result.\n" +
		"Correct way to fetch a full result: poll with tail until state=idle, then read with since_last.\n" +
		"To see what a human did during takeover you MUST use mode=since_last: it reconstructs every command the human typed as \"[rc=n] $ command\" (with exit code) together with its output. held=true means a human is currently in control."

	descControl = "Send a control key to the session, or perform a recovery action. " +
		"Control keys are written to the PTY as raw control bytes and work even while a command is running: " +
		"ctrl-c (SIGINT, interrupt), ctrl-d (EOF), ctrl-z (suspend), ctrl-\\ (SIGQUIT), ctrl-l (clear screen), " +
		"ctrl-u / ctrl-k (erase to line start/end), ctrl-a / ctrl-e (move to line start/end), ctrl-w (erase word), " +
		"ctrl-r (reverse search), ctrl-g (cancel), tab, esc, enter, backspace. " +
		"Recovery actions: flush (drop queued input + clear the current line + Enter), hard (reopen the shell) and " +
		"rearm (re-inject the sentinel prompt after you switched into a new shell, e.g. after su/docker exec/matrix_jail, if the session appears stuck). " +
		"If held=true the session is under human takeover: this call was NOT executed; wait until held clears."

	descStatus = "Lightweight status query (empty output). Returns state, prompt, exit_code and held. " +
		"held=true means a human has taken over: the model should pause write operations and only read until held becomes false; " +
		"use terminal_read(mode=since_last) to see what the human executed."

	descClose = "Close the session, releasing its child process and concurrency slot. " +
		"If held=true the session is under human takeover: this call was NOT executed; wait until held clears."

	descList = "List all sessions on this instance with their status snapshot (session_id, host, status, idle_seconds, held). " +
		"held=true means that session is under human takeover; pause write operations until it clears."
)

// defaultDescriptions 各工具的内置默认描述，供 resolveDesc 在无覆盖时回退。
var defaultDescriptions = map[string]string{
	"terminal_open":    descOpen,
	"terminal_send":    descSend,
	"terminal_read":    descRead,
	"terminal_control": descControl,
	"terminal_status":  descStatus,
	"terminal_close":   descClose,
	"terminal_list":    descList,
}

var (
	descMu           sync.RWMutex
	descProgOverride = map[string]string{}
)

// SetToolDescriptions 以编程方式覆盖工具描述（key 为工具名，如 terminal_open）。供把本模块嵌入外部
// MCP 的宿主在 RegisterTools/NewHTTPHandler 之前按自家话术改写工具说明。空串条目忽略；传 nil 清空。
// 优先级：编程覆盖 > 配置文件 tool_descriptions > 内置默认。并发安全。
func SetToolDescriptions(over map[string]string) {
	descMu.Lock()
	defer descMu.Unlock()
	descProgOverride = map[string]string{}
	for k, v := range over {
		if v != "" {
			descProgOverride[k] = v
		}
	}
}

// resolveDesc 计算工具对外描述：编程覆盖 > 配置文件 tool_descriptions > 内置默认。
func resolveDesc(name string) string {
	descMu.RLock()
	v, ok := descProgOverride[name]
	descMu.RUnlock()
	if ok {
		return v
	}
	if c := config.Get().ToolDescriptions; c != nil {
		if v, ok := c[name]; ok && v != "" {
			return v
		}
	}
	return defaultDescriptions[name]
}

// registerTools 在给定 server 上注册全部 7 个 terminal_* 工具。
// 每个 handler 计算结果后调用 audit.Logger 记录一条审计。CallerIP 从 HTTP 请求头解析
// （官方 SDK 通过 CallToolRequest.Extra.Header 把 HTTP header 透传给每个工具 handler）。
func registerTools(server *mcp.Server, a *audit.Logger) {
	mcp.AddTool(server, &mcp.Tool{Name: "terminal_open", Description: resolveDesc("terminal_open")},
		func(_ context.Context, req *mcp.CallToolRequest, in openInput) (*mcp.CallToolResult, map[string]string, error) {
			owner, ok := ownerSig(req)
			if !ok {
				return nil, nil, fmt.Errorf("missing required identity header(s): %v", config.Get().Identity.Headers)
			}
			res, err := session.Open(in.Mode, in.Command, in.Host, owner)
			e := baseEntry(req, "terminal_open", map[string]any{
				"mode": in.Mode, "host": in.Host, "command": in.Command,
			})
			if err != nil {
				e.Error = err.Error()
			} else {
				e.State = res["state"]
			}
			a.Log(e)
			return nil, res, err
		})

	mcp.AddTool(server, &mcp.Tool{Name: "terminal_send", Description: resolveDesc("terminal_send")},
		func(_ context.Context, req *mcp.CallToolRequest, in sendInput) (*mcp.CallToolResult, session.Envelope, error) {
			owner, ok := ownerSig(req)
			if !ok || !authorizeOwner(owner, in.SessionID) {
				env := session.Envelope{State: "dead", Error: "session not found"}
				logEnv(a, req, "terminal_send", map[string]any{"session_id": in.SessionID}, env)
				return nil, env, nil
			}
			env := session.Send(in.SessionID, in.Input, in.WaitMs)
			logEnv(a, req, "terminal_send", map[string]any{
				"session_id": in.SessionID, "input": in.Input, "wait_ms": in.WaitMs,
			}, env)
			return nil, env, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "terminal_read", Description: resolveDesc("terminal_read")},
		func(_ context.Context, req *mcp.CallToolRequest, in readInput) (*mcp.CallToolResult, session.Envelope, error) {
			owner, ok := ownerSig(req)
			if !ok || !authorizeOwner(owner, in.SessionID) {
				env := session.Envelope{State: "dead", Error: "session not found"}
				logEnv(a, req, "terminal_read", map[string]any{"session_id": in.SessionID, "wait_ms": in.WaitMs, "mode": in.Mode}, env)
				return nil, env, nil
			}
			env := session.Read(in.SessionID, session.ReadArgs{
				Mode: in.Mode, WaitMs: in.WaitMs, OutputRef: in.OutputRef, Op: in.Op,
				LineOffset: in.LineOffset, Limit: in.Limit, Pattern: in.Pattern,
				Before: in.Before, After: in.After, MaxBytes: in.MaxBytes,
			})
			logEnv(a, req, "terminal_read", map[string]any{
				"session_id": in.SessionID, "wait_ms": in.WaitMs, "mode": in.Mode,
				"op": in.Op, "output_ref": in.OutputRef,
			}, env)
			return nil, env, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "terminal_control", Description: resolveDesc("terminal_control")},
		func(_ context.Context, req *mcp.CallToolRequest, in controlInput) (*mcp.CallToolResult, session.Envelope, error) {
			owner, ok := ownerSig(req)
			if !ok || !authorizeOwner(owner, in.SessionID) {
				env := session.Envelope{State: "dead", Error: "session not found"}
				logEnv(a, req, "terminal_control", map[string]any{"session_id": in.SessionID, "key": in.Key}, env)
				return nil, env, nil
			}
			env := session.Control(in.SessionID, in.Key)
			logEnv(a, req, "terminal_control", map[string]any{
				"session_id": in.SessionID, "key": in.Key,
			}, env)
			return nil, env, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "terminal_status", Description: resolveDesc("terminal_status")},
		func(_ context.Context, req *mcp.CallToolRequest, in sessionIDInput) (*mcp.CallToolResult, session.Envelope, error) {
			owner, ok := ownerSig(req)
			if !ok || !authorizeOwner(owner, in.SessionID) {
				env := session.Envelope{State: "dead", Error: "session not found"}
				logEnv(a, req, "terminal_status", map[string]any{"session_id": in.SessionID}, env)
				return nil, env, nil
			}
			env := session.Status(in.SessionID)
			logEnv(a, req, "terminal_status", map[string]any{"session_id": in.SessionID}, env)
			return nil, env, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "terminal_close", Description: resolveDesc("terminal_close")},
		func(_ context.Context, req *mcp.CallToolRequest, in sessionIDInput) (*mcp.CallToolResult, session.Envelope, error) {
			owner, ok := ownerSig(req)
			if !ok || !authorizeOwner(owner, in.SessionID) {
				env := session.Envelope{State: "dead", Error: "session not found"}
				logEnv(a, req, "terminal_close", map[string]any{"session_id": in.SessionID}, env)
				return nil, env, nil
			}
			env := session.Close(in.SessionID)
			logEnv(a, req, "terminal_close", map[string]any{"session_id": in.SessionID}, env)
			return nil, env, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "terminal_list", Description: resolveDesc("terminal_list")},
		func(_ context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, listOutput, error) {
			owner, ok := ownerSig(req)
			if !ok {
				return nil, listOutput{}, fmt.Errorf("missing required identity header(s): %v", config.Get().Identity.Headers)
			}
			local := session.List(owner)
			var all []map[string]string
			if req.Extra != nil && req.Extra.Header.Get(forwardedHeader) == "1" {
				all = local
			} else {
				all = fanoutList(local, peerList(), reqHeader(req), owner)
			}
			e := baseEntry(req, "terminal_list", nil)
			e.Bytes = len(all)
			a.Log(e)
			return nil, listOutput{Sessions: all}, nil
		})
}

var (
	signerMu  sync.Mutex
	theSigner *identity.Signer
)

// signer 惰性构造身份签名器（读一次配置）。加锁避免 stateless 下并发请求同时初始化的数据竞争。
func signer() *identity.Signer {
	signerMu.Lock()
	defer signerMu.Unlock()
	if theSigner == nil {
		c := config.Get()
		theSigner = identity.New(c.Identity.Headers, c.Identity.Mode, c.Identity.OnMissing)
	}
	return theSigner
}

// ownerSig 从请求头算出调用方归属签名；ok=false 表示按 reject 策略缺头、应拒绝。
func ownerSig(req *mcp.CallToolRequest) (string, bool) {
	return signer().Signature(reqHeader(req))
}

// authorizeOwner 校验签名 owner 是否为会话 id 的属主。false → 越权或本机无此会话，按 not found 处理。
func authorizeOwner(owner, id string) bool {
	got, found := session.Owner(id)
	if !found {
		return false
	}
	return got == owner
}

// baseEntry 构造带 CallerIP 与调用方标识（X-MCP-USER）的审计条目骨架。
func baseEntry(req *mcp.CallToolRequest, tool string, params map[string]any) audit.Entry {
	h := reqHeader(req)
	return audit.Entry{
		CallerIP: callerIP(h),
		User:     mcpUser(h),
		Tool:     tool,
		Params:   params,
	}
}

// mcpUser 从 X-MCP-USER 头解析每次调用的调用方标识（无则空）。
func mcpUser(h http.Header) string {
	if h == nil {
		return ""
	}
	return h.Get("X-MCP-USER")
}

// logEnv 记录返回 Envelope 的工具审计（State/ExitCode/Held/Bytes/Error）。
func logEnv(a *audit.Logger, req *mcp.CallToolRequest, tool string, params map[string]any, env session.Envelope) {
	e := baseEntry(req, tool, params)
	e.State = env.State
	e.Held = env.Held
	e.Bytes = len(env.Output)
	e.Error = env.Error
	e.ExitCode = env.ExitCode
	a.Log(e)
}

// reqHeader 取本次调用透传的 HTTP header（无则 nil）。
func reqHeader(req *mcp.CallToolRequest) http.Header {
	if req == nil || req.Extra == nil {
		return nil
	}
	return req.Extra.Header
}

// callerIP 从 HTTP header 解析调用方 IP：优先 X-Forwarded-For（取首个），
// 再 X-Real-Ip，最后由 audit 中间件注入的 X-Pty-Bridge-Mcp-Remoteaddr（原始连接地址）。
func callerIP(h http.Header) string {
	if h == nil {
		return ""
	}
	if xff := h.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	if xr := h.Get("X-Real-Ip"); xr != "" {
		return strings.TrimSpace(xr)
	}
	return h.Get("X-Pty-Bridge-Mcp-Remoteaddr")
}

// remoteAddrMiddleware 在无转发头时，把 TCP 连接的对端 IP 注入 X-Pty-Bridge-Mcp-Remoteaddr，
// 使每个工具 handler 都能经 CallToolRequest.Extra.Header 拿到调用方 IP 做审计。
func remoteAddrMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Forwarded-For") == "" && r.Header.Get("X-Real-Ip") == "" {
			ip, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				ip = r.RemoteAddr
			}
			r.Header.Set("X-Pty-Bridge-Mcp-Remoteaddr", ip)
		}
		next.ServeHTTP(w, r)
	})
}
