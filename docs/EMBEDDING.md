# Embedding terminal-mcp as a library

**English** | [简体中文](EMBEDDING.zh-CN.md)

This guide covers running terminal-mcp **inside your own Go service** so its
`terminal_*` tools live on the **same MCP server / same `/mcp` endpoint** as your
other tools (a single-server setup), plus the full public API reference.

If you just want to run it as a standalone MCP server, you don't need any of this —
see the README's Quick start.

- Module: `github.com/fzxbl/terminal-mcp`
- Public package: `github.com/fzxbl/terminal-mcp/mcpserver`
- Requires the official MCP Go SDK: `github.com/modelcontextprotocol/go-sdk`
  (terminal-mcp is built on it, so registering onto your own `*mcp.Server` works directly).

```bash
go get github.com/fzxbl/terminal-mcp
```

## Public API (`mcpserver`)

| Function | Purpose |
| --- | --- |
| `Init(configPath string)` | Load config (`""` = defaults) and initialize the session pool. Call once at startup, before anything else. |
| `RegisterTools(server *mcp.Server, auditWriter io.Writer)` | Register all `terminal_*` tools onto your official-SDK server. `auditWriter` may be `nil` (no audit; e.g. when the host already logs tool calls). |
| `TerminalHandler() http.Handler` | The web terminal (human-takeover) HTTP handler. It parses paths under `/terminal/`; mount it at `/terminal/` or under any prefix via `http.StripPrefix`. `RegisterTools` does NOT include it. |
| `SetAdvertiseAddr(hostPort string)` | Override the `host:port` used to build `terminal_url`. Needed when the host process owns the socket and its address differs from this module's `listen_addr`. Empty resets to the default. Concurrency-safe. |
| `SetToolDescriptions(over map[string]string)` | Override the model-facing tool descriptions by tool name (keys like `terminal_open`). Call before `RegisterTools`/`NewHTTPHandler`. Empty-string entries are ignored; `nil` clears. Precedence: programmatic > config `tool_descriptions` > built-in default. Concurrency-safe. |
| `StartIdleGC(ctx context.Context)` | Start the idle-session GC + transcript-sweep goroutine. Cancel `ctx` to stop and reclaim all sessions. |
| `Shutdown()` | Close all sessions and reclaim child process groups (idempotent). |
| `NewHTTPHandler(auditWriter io.Writer) http.Handler` | All-in-one handler that mounts BOTH `/mcp` and `/view/terminal/`. Use it only if you want terminal-mcp to own its own `/mcp` (not the shared-server case). |

## Pattern A — share one `/mcp` with your own tools (recommended)

Register terminal tools onto your existing server, and mount the web terminal
handler separately:

```go
package main

import (
    "context"
    "log"
    "net/http"

    "github.com/modelcontextprotocol/go-sdk/mcp"
    "github.com/fzxbl/terminal-mcp/mcpserver"
)

func main() {
    // 1) Load config + session pool ("" = defaults)
    mcpserver.Init("config.toml")

    // 2) terminal_url is built from config.listen_addr by default. If the socket
    //    is owned by your host process (different address/port), point it at the
    //    host's real reachable address, otherwise the web terminal link won't open.
    mcpserver.SetAdvertiseAddr("10.0.0.5:8080")

    // 3) Session idle-GC / transcript cleanup lifecycle
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    mcpserver.StartIdleGC(ctx)
    defer mcpserver.Shutdown()

    // 4) Your own MCP server, shared by your tools and terminal-mcp's
    server := mcp.NewServer(&mcp.Implementation{Name: "my-app", Version: "1.0.0"}, nil)
    // ... register your own tools onto `server` here ...
    mcpserver.RegisterTools(server, nil) // terminal_* tools

    // 5) Routing: /mcp -> your server; /view/terminal/ -> web terminal (human takeover)
    mux := http.NewServeMux()
    mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(
        func(*http.Request) *mcp.Server { return server }, nil))
    mux.Handle("/view/terminal/", http.StripPrefix("/view", mcpserver.TerminalHandler()))
    log.Fatal(http.ListenAndServe(":8080", mux))
}
```

## Pattern B — standalone all-in-one handler

If terminal-mcp can own its own `/mcp`:

```go
mcpserver.Init("config.toml")
mcpserver.StartIdleGC(context.Background())
h := mcpserver.NewHTTPHandler(nil) // mounts /mcp and /view/terminal/
log.Fatal(http.ListenAndServe(":8900", h))
```

## Path conventions (what you can change, what's fixed)

- **`/mcp`** — you choose. `RegisterTools` only registers tools onto the
  `*mcp.Server`; it has nothing to do with the HTTP path. Mount your server's
  streamable handler at any path.
- **web terminal** — the handler parses request paths under `/terminal/`. Mount it
  at `/terminal/`, or under any prefix by stripping the prefix first, e.g.
  `http.StripPrefix("/view", TerminalHandler())` at `/view/terminal/`. The web
  frontend derives its SSE / WebSocket / takeover URLs from the page location
  (relative), so any mount prefix works without code changes. Keep `terminal_url`
  (built by `SetAdvertiseAddr` + the mount path) consistent with where you mount.
- **host:port** — set via `SetAdvertiseAddr` (used to build `terminal_url`); it does
  **not** affect the path prefix.

## Authorization (important)

`terminal_send`, `terminal_control`, `terminal_open` execute arbitrary commands.
When embedding behind an authenticating host, make sure your authorization layer
treats these as **high-risk / write** operations. terminal-mcp itself does not
enforce per-user authorization — that's the host's responsibility. Registering the
tools onto a server that has no auth means anyone reaching `/mcp` gets shell access.

If your host derives risk from a static registry (rather than reading it off the
tool), you must register the terminal tools' risk explicitly, e.g.:

- `terminal_open` / `terminal_send` / `terminal_control` → write, high
- `terminal_close` → write, medium
- `terminal_output` / `terminal_status` / `terminal_list` → read, low

## Configuration reference

Passed to `Init(configPath)`; TOML. All fields optional (sensible defaults).

| Key | Default | Meaning |
| --- | --- | --- |
| `listen_addr` | `127.0.0.1:8900` | Bind address (standalone / `NewHTTPHandler`). For embedded use, prefer `SetAdvertiseAddr`. |
| `data_dir` | `./data` | Base dir for session transcripts (the `.raw` logs). |
| `default_shell` | `bash` | Command for `mode=local` when none is given. |
| `ssh_user` | (empty) | Required for `mode=ssh`. |
| `ssh_opts` | `-tt -o LogLevel=error -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=4` | Preset SSH options; keep `-tt`. |
| `shell_switch_commands` | `ssh, su, sudo -i, sudo su, docker exec, kubectl exec, nsenter, chroot` | Commands that trigger sentinel auto re-arm after switching shell. Append your own (e.g. a container-enter command). |
| `auto_rearm` | `true` | Auto re-arm the sentinel after a shell switch. `false` = manual `terminal_control(rearm)` only. |
| `tool_descriptions` | (empty) | TOML table (`[tool_descriptions]`, keys like `terminal_open`) overriding the model-facing tool descriptions. Missing/empty entries keep the built-in default. Can also be overridden programmatically via `SetToolDescriptions` (higher precedence). |
| `disable_local_mode` | `false` | Optional hardening, off by default. When `true`, reject `mode=local` (do not expose the MCP host's local shell); only ssh sessions are allowed. |
| `ssh_login_prologue` | (empty) | Optional. A command run in the local shell before ssh, typically for auth login (when the credential must share the ssh session). Chain multiple with `&&` — the whole thing is wrapped in `sh -c '{...} && exec ssh'`, so on success `exec` replaces the process (no local shell left) and on failure it short-circuits and exits; neither escapes to a local shell. Empty = ssh directly. |
| `max_sessions` | `2` | Concurrent session cap. |
| `idle_ttl_minutes` | `30` | Idle session GC timeout. |
| `transcript_retention_days` | `7` | How long `.raw` transcripts are kept. |
| `max_buffer_bytes` | `8388608` (8 MiB) | In-memory tail-cache cap per session. The full session output is an append-only `.raw` log on disk (source of truth); memory keeps only the last this-many bytes and older bytes are read back from disk on demand. Bounds memory against runaway streaming output. |
| `exec_output_max_bytes` | `1048576` (1 MiB) | Per-call return cap for `terminal_send`/`terminal_output`. Larger results come back truncated with a `range` `{from,to}`; page them with `terminal_output(mode=range, from, to)`. |
| `log_dir` | `log` | Audit + runtime log directory. |
| `log_rotate` | `daily` | `daily` or `hourly` (time-based, not size-based). |
| `log_max_age_days` | `30` | Rotated-log retention. |
| `audit_log` | (empty) | Audit log filename prefix (empty = `audit`). |

See `config.example.toml` for a copy-paste starting point.
