# terminal-mcp

**English** | [简体中文](README.zh-CN.md)

[![Go Reference](https://pkg.go.dev/badge/github.com/fzxbl/terminal-mcp.svg)](https://pkg.go.dev/github.com/fzxbl/terminal-mcp)
[![Go 1.25+](https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![MCP](https://img.shields.io/badge/MCP-Model%20Context%20Protocol-6E56CF)](https://modelcontextprotocol.io)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Stars](https://img.shields.io/github/stars/fzxbl/terminal-mcp?style=social)](https://github.com/fzxbl/terminal-mcp/stargazers)

**A real PTY terminal for AI agents — with full observability and seamless human takeover.**

`terminal-mcp` is a [Model Context Protocol](https://modelcontextprotocol.io) server that gives an LLM agent a **real PTY terminal** (not a one-shot `exec` pipe). A human can **watch every step the agent takes in real time**, **take over the exact same session the instant the agent goes down the wrong path**, and the agent then **resumes with the full context of everything the human just did**. Local shells, `ssh`, containers, `gdb`, `python`, `top`, `vim` — the agent drives them like a person, and you're never locked out.

> One terminal, shared by human and agent. The agent does the driving, you watch live, grab the wheel when it matters, and hand it back — without losing a beat.

Built in Go, single binary, official MCP SDK, Streamable HTTP. Drop it into Cursor, Claude, Comate, or any MCP client.

---

## Why terminal-mcp

Most "run a command" tools hand the model a stateless pipe: one command in, one blob out. It breaks on anything interactive (a login, a REPL, a long-running session), and — worse — it gives the human **no way to see what's happening or step in** when the agent goes wrong.

terminal-mcp nails five things:

- **1. A real PTY, not a pipe.** A genuine pseudo-terminal with a persistent session. Interactive and full-screen programs (`gdb`, `python`, `mysql`, `top`, `vim`), line editing, colors, prompts, control keys — all work exactly as they do for a human. Not `exec` one-shots.
- **2. Full observability — the same screen the agent sees.** Every session has a live web terminal URL. Open it and watch, in real time, every keystroke and byte the agent produces. No guessing what your agent did.
- **3. Take over to fix, release to resume — one session, driven by both.** See the agent heading the wrong way? Click *take over* and type straight into the running PTY; its writes pause instantly. Fix things by hand (enter a password, run the right command, back it out of a hole), then *release* — everything you typed is fed back as `[rc=n] $ command` with output, so the agent continues **with complete knowledge of what you just did**. No re-explaining, no lost state.
- **4. A fence around the agent — transparent resource limits, inescapable across shell switches.** Configure a model-invisible `ulimit` that's injected at session start and **re-injected every time the agent hops into a new shell** (`ssh` remote / `su` / container). The hard limit is inherited by every child process, so unprivileged commands can't raise it back — the agent can't escape the cap by switching shells or running other commands. A safety net for autonomous agents, with zero awareness on the model side.
- **5. Scales horizontally behind a load balancer.** Session-level routing (the owner node is encoded into the `session_id` and requests auto-reverse-proxy across nodes) lets you run it as a multi-node deployment behind an LB and add machines when you need more capacity; with configurable per-client session isolation, many users and agents run concurrently without stepping on each other. (See "Distributed deployment & horizontal scaling" below.)

Plus the machinery that makes the above reliable:

- **Precise command boundaries.** A sentinel prompt tells the server exactly when a command finished and its exit code — the agent never mistakes "still running" for "done".
- **Survives shell switches.** `ssh`, `su`, `docker exec`, entering a container — the sentinel is re-armed automatically so tracking keeps working across hops.
- **LLM-friendly output.** ANSI escapes stripped, partial escape sequences held back — the model never gets corrupted bytes. The session log is an append-only file on disk (the source of truth) with a bounded in-memory tail cache, so a runaway `cat`/`yes` can't blow up memory; oversized results are returned as a log-range reference and paged on demand.
- **Local & remote.** `mode=local` runs a shell/command on the host; `mode=ssh` connects out.
- **Audit & replay.** Every tool call logged as JSON (caller IP, user, tool, args, result); every session's raw byte stream saved for replay, with configurable retention.

---

## Quick start

Requires Go 1.25+.

```bash
go install github.com/fzxbl/terminal-mcp/cmd/terminal-mcp@latest
terminal-mcp --listen 127.0.0.1:8900
```

The MCP endpoint is at `/mcp`; a session's web terminal is at `/debug/terminal/<session_id>`.

## Use it from your AI coding tool

terminal-mcp speaks MCP over **Streamable HTTP**. Point your client at `http://127.0.0.1:8900/mcp`.

Generic MCP client config (`mcp.json`):

```jsonc
{
  "mcpServers": {
    "terminal": { "url": "http://127.0.0.1:8900/mcp" }
  }
}
```

Then just ask your agent:

> "Open a terminal, ssh into staging, tail the service log, and tell me why it's 500ing."

The agent opens a session, runs commands, and streams back results. If it needs a password or you want to intervene, open the returned `terminal_url` and take over — the agent waits, watches, and resumes.

## Human takeover, in 20 seconds

1. Agent calls `terminal_open` → gets a `terminal_url`.
2. You open it in a browser, click **take over**.
3. The session reports `held=true`; the agent's writes pause.
4. You type (enter a password, run a risky command, poke around). The agent sees each of your commands reconstructed as `[rc=n] $ cmd`.
5. You click **release**. The agent picks up right where you left off.

## Tools

| Tool | What it does |
| --- | --- |
| `terminal_open(mode, command?, host?)` | Start a persistent PTY session. `mode=local` or `mode=ssh`. Returns `session_id` + `terminal_url`. |
| `terminal_send(session_id, input, wait_ms?)` | Type a command, wait for it to settle. Returns output, state, exit_code. |
| `terminal_read(session_id, wait_ms?, mode?, from?, to?)` | Observe output. `tail` (peek), `since_last` (full increment; also the record of human-takeover commands), or `range` (page an absolute log byte window `[from,to)`; use the `range` returned in a truncated result). |
| `terminal_control(session_id, key)` | Send control keys (`ctrl-c`, `ctrl-d`, `ctrl-z`, …) or recovery actions (`flush`, `hard`, `rearm`). |
| `terminal_status(session_id)` | Lightweight state / prompt / exit_code / held query. |
| `terminal_close(session_id)` | Close the session, reclaim the process group. |
| `terminal_list()` | List active sessions. |

## Configure

Copy `config.example.toml`. Highlights: `listen_addr`, `data_dir`, `default_shell`, `ssh_user`, `ssh_opts`, `shell_switch_commands` (commands that trigger auto re-arm — add your own, e.g. container-enter commands), `auto_rearm`, `max_buffer_bytes` (in-memory tail-cache cap; the full log lives on disk), `exec_output_max_bytes` (per-call return cap; larger results come back as a paging range), `transcript_retention_days`, `log_dir` / `log_rotate` / `log_max_age_days`.

**Transparent resource guardrails (`resource_limit_cmd`)**: a model-invisible `ulimit` injected alongside the sentinel at session start, and re-injected on every shell switch (`ssh`/`su`/`docker exec`/`matrix_jail` …) and on `hard` reset. A `ulimit` without `-S/-H` sets both soft and hard limits; the hard limit is inherited by child processes and can't be raised by unprivileged commands, so switching shells or running other commands can't escape the cap:

```toml
resource_limit_cmd = "ulimit -v 4194304; ulimit -t 600; ulimit -u 4096"
```

> Boundary: `ulimit` can't constrain a process that escalates to root and raises its own hard limit, nor a process tree started by a daemon that doesn't inherit this session. For a privilege-independent cap on the local process tree, use cgroup v2 instead.

**Customize tool descriptions**: use `[tool_descriptions]` to override the model-facing description per tool (handy when embedding into an external MCP and you want your own wording; missing/empty entries keep the built-in default):

```toml
[tool_descriptions]
terminal_open = "Open a persistent terminal session; returns session_id and a read-only web terminal URL."
terminal_send = "Type a command into the session and wait for it to finish; returns new output and status."
```

## Embed it as a library (share one MCP server)

Already run an MCP server and want terminal tools on the same `/mcp`? `go get github.com/fzxbl/terminal-mcp` and register onto your own official-SDK server:

```go
mcpserver.Init("config.toml")
mcpserver.SetAdvertiseAddr("10.0.0.5:8080") // host:port used to build terminal_url
mcpserver.SetToolDescriptions(map[string]string{ // optional: reword tool descriptions
    "terminal_open": "Open a persistent terminal session; returns session_id and a web terminal URL.",
})
mcpserver.StartIdleGC(ctx)

mcpserver.RegisterTools(server, auditWriter)          // terminal_* tools
mux.Handle("/debug/terminal/", mcpserver.TerminalHandler()) // web terminal (human takeover)
```

`/mcp` path is yours to choose; the web terminal prefix `/debug/terminal/` is fixed. Tool descriptions can also be overridden via the `[tool_descriptions]` config table (precedence: `SetToolDescriptions` > config > built-in default). See **[docs/EMBEDDING.md](docs/EMBEDDING.md)** for the full public API reference, the shared-`/mcp` integration pattern, path conventions, authorization notes, and the config reference.

## Distributed deployment & horizontal scaling

**It is not fully stateless** — each session is backed by a **live PTY process / ssh connection pinned to the node** that created it, and such kernel resources cannot migrate across processes or nodes. But via **session-level routing**, you can put it behind a load balancer and **scale horizontally**:

- **Stateless MCP transport**: the SDK's stateless mode is enabled internally, so any node can independently handle any request — clients are never pinned to a specific node.
- **Session-level routing**: the owner node's address is encoded into the `session_id` (`<host:port>~<uuid>`). When a node receives a call for a session it doesn't own, it **reverse-proxies** the request to the owner node. Clients only ever talk to the LB / any node and need not know the topology.
- **Horizontal scaling**: add nodes for more concurrent session capacity. A single node crash only affects sessions on that node (no PTY migration); other nodes are unaffected.

Two supporting capabilities:

- **Per-client session isolation**: a configurable set of request headers (`[identity]`) yields a client signature; a client only sees/operates its own sessions, and cross-owner access always returns `session not found`. `terminal_list` fans out across nodes to aggregate that client's global view.
- **Peer discovery**: reverse-proxy needs no list (the owner address is in the `session_id`); `terminal_list` aggregation needs a peer list, provided either via static `peers` config or a registered discovery function `SetPeerProvider(func() []string)` (Consul/etcd/DNS/K8s, etc.). This list also acts as the reverse-proxy allowlist (only known nodes may be proxied to, preventing a client-controlled `session_id` from steering the server to dial arbitrary addresses).

Minimal config (**the same file on every node**):

```toml
# Bind all interfaces; the process auto-detects its own reachable IP as the owner
# address, so there's no need to configure a distinct address per instance.
listen_addr = "0.0.0.0:8900"
# Sibling nodes for terminal_list cross-node aggregation (or provide via SetPeerProvider
# for a fully config-free setup)
peers = ["10.0.0.11:8900", "10.0.0.12:8900", "10.0.0.13:8900"]

[identity]
headers = ["X-MCP-USER"]   # identity header injected by a trusted gateway
mode = "raw"                # raw | sha256
on_missing = "reject"       # reject | allow_empty
```

> Deployment notes: the identity header **must be injected by a trusted gateway** — nodes must not trust a client-supplied identity header. The owner address is auto-detected by the process (a wildcard `0.0.0.0` bind resolves to the machine's real IP), so every instance can share one config; only override via `SetAdvertiseAddr` (embedding API) when behind NAT or when an externally mapped address is required. `peers` can also be discovered dynamically via `SetPeerProvider`, making a distributed setup fully config-free.

## Security

**The HTTP service (MCP endpoint + web terminal) is unauthenticated by default.** Calling these tools is equivalent to shell access on the host (or over ssh). Bind it to loopback, or put it behind an authenticating reverse proxy, before exposing it beyond a trusted network.

## License

MIT. Contributions and stars welcome.
