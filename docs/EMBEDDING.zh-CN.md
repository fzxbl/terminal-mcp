# 作为库嵌入 terminal-mcp

[English](EMBEDDING.md) | **简体中文**

本文介绍如何把 terminal-mcp **嵌入到你自己的 Go 服务里**，让它的 `terminal_*` 工具
与你其它工具**共用同一个 MCP server / 同一个 `/mcp` 端点**（单 server 模式），并给出
完整的公开 API 参考。

如果你只想把它当独立 MCP server 跑，不需要看本文——见 README 的「快速开始」。

- 模块：`github.com/fzxbl/terminal-mcp`
- 公开包：`github.com/fzxbl/terminal-mcp/mcpserver`
- 依赖官方 MCP Go SDK：`github.com/modelcontextprotocol/go-sdk`
  （terminal-mcp 基于它构建，因此可以直接注册到你自己的 `*mcp.Server` 上）。

```bash
go get github.com/fzxbl/terminal-mcp
```

## 公开 API（`mcpserver`）

| 函数 | 作用 |
| --- | --- |
| `Init(configPath string)` | 加载配置（`""` 用默认值）并初始化会话池。启动时最先调用一次。 |
| `RegisterTools(server *mcp.Server, auditWriter io.Writer)` | 把全部 `terminal_*` 工具注册到你的官方 SDK server。`auditWriter` 可传 `nil`（不记审计，例如宿主已记录工具调用）。 |
| `SetPublicBaseURL(base string)` | 设置 `terminal_url` 的对外入口，如 `https://mcp.example.com`。挂载前缀只能交给 `MountWebTerminal`。**可以是域名/VIP**：已挂载网页终端的属主在路径里，落到任意节点都会反代到属主。末尾 `/` 会去掉、缺 scheme 按 `http://` 补。传空恢复按 `listen_addr` 推导。并发安全。 |
| `SetSelfAddr(hostPort string)` | 设置本节点**直连** `host:port`：编码进 `session_id`，只用于节点间反代拨号，不进任何对外文本。未设置时按 `listen_addr` 推导（通配 host 换成本机 IP）。与 `SetPublicBaseURL` 正交，顺序无关。 |
| `WithSessionRouting(next http.Handler) http.Handler` | 包裹 **MCP handler**：`tools/call` 里的 `session_id` 属主非本机时整条反代过去。 |
| `MountWebTerminal(prefix string, mount func(pattern string, h http.Handler)) error` | 在 `<prefix>/view/terminal/` 挂载完整网页终端；这是唯一网页终端入口，路由、`terminal_url` 前缀和属主路由均由 `prefix` 一次推导。 |
| `SetToolDescriptions(over map[string]string)` | 按工具名覆盖对外暴露给模型的工具描述（key 如 `terminal_open`）。在 `RegisterTools`/`NewHTTPHandler` 之前调用。空串条目忽略，传 `nil` 清空。优先级：编程覆盖 > 配置文件 `tool_descriptions` > 内置默认。并发安全。 |
| `StartIdleGC(ctx context.Context)` | 启动空闲会话 GC + transcript 清理协程。取消 `ctx` 即停止并回收所有会话。 |
| `Shutdown()` | 关闭所有会话、回收子进程组（幂等）。 |
| `NewHTTPHandler(auditWriter io.Writer) http.Handler` | 一体化 handler，同时挂 `/mcp` 和 `/view/terminal/`。仅当你要让 terminal-mcp 独占自己的 `/mcp`（非共用 server 场景）时用。 |

## 模式 A —— 与你自己的工具共用一个 `/mcp`（推荐）

把终端工具注册到你已有的 server，网页终端处理器单独挂：

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
    // 1) 加载配置 + 会话池（"" 用默认值）
    mcpserver.Init("config.toml")

    // 2) terminal_url 的对外入口：给人点的链接，填统一入口（域名/VIP 均可），
    //    不要携带挂载前缀。节点间拨号地址（SetSelfAddr）与它无关，
    //    默认由进程按 listen_addr 自动探测。
    mcpserver.SetPublicBaseURL("https://mcp.example.com")

    // 3) 会话空闲 GC / transcript 清理生命周期
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    mcpserver.StartIdleGC(ctx)
    defer mcpserver.Shutdown()

    // 4) 你自己的 MCP server，供你的工具与 terminal-mcp 共用
    server := mcp.NewServer(&mcp.Implementation{Name: "my-app", Version: "1.0.0"}, nil)
    // ... 在这里注册你自己的工具到 server ...
    mcpserver.RegisterTools(server, nil) // terminal_* 工具

    // 5) 路由：MCP 端点与网页终端共用一个挂载前缀。
    mux := http.NewServeMux()
    mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
    mux.Handle("/mcp", mcpserver.WithSessionRouting(mcpHandler))
    if err := mcpserver.MountWebTerminal("/mcp", func(pattern string, h http.Handler) {
        mux.Handle(pattern, h)
    }); err != nil {
        log.Fatal(err)
    }
    log.Fatal(http.ListenAndServe(":8080", mux))
}
```

## 模式 B —— 独立一体化 handler

若 terminal-mcp 可以独占自己的 `/mcp`：

```go
mcpserver.Init("config.toml")
mcpserver.StartIdleGC(context.Background())
h := mcpserver.NewHTTPHandler(nil) // 同时挂 /mcp 和 /view/terminal/
log.Fatal(http.ListenAndServe(":8900", h))
```

## 路径约定（哪些能改、哪些写死）

- **`/mcp`** —— 你决定。`RegisterTools` 只把工具注册到 `*mcp.Server`，与 HTTP 路径无关，
  你可以把 server 的 streamable handler 挂到任意路径。
- **网页终端** —— 只能通过 `MountWebTerminal(prefix, mount)` 挂载。它注册
  `<prefix>/view/terminal/`，并从同一个 `prefix` 推导网页前端路径、`terminal_url` 与多节点属主路由。
- **对外入口** —— 用 `SetPublicBaseURL` 指定（拼 `terminal_url` 用），可为域名/VIP。
- **节点直连地址** —— 用 `SetSelfAddr` 指定（只用于节点间反代），默认自动探测。两者互不影响。

## 鉴权（重要）

`terminal_send`、`terminal_control`、`terminal_open` 会执行任意命令。嵌入到带鉴权的宿主
时，务必让你的授权层把它们当作**高风险 / 写**操作。terminal-mcp 自身不做按人鉴权——那是
宿主的责任。把工具注册到一个无鉴权的 server，等于谁能访问 `/mcp` 谁就拿到了 shell 权限。

如果你的宿主是靠"静态注册表"判定风险（而非从工具本身读取），必须显式登记终端工具的风险，例如：

- `terminal_open` / `terminal_send` / `terminal_control` → 写，high
- `terminal_close` → 写，medium
- `terminal_output` / `terminal_status` / `terminal_list` → 读，low

## 配置参考

传给 `Init(configPath)`；TOML。所有字段可选（有合理默认）。

| 键 | 默认 | 含义 |
| --- | --- | --- |
| `listen_addr` | `127.0.0.1:8900` | 绑定地址（独立 / `NewHTTPHandler`）。嵌入时对外入口用 `SetPublicBaseURL`、直连地址用 `SetSelfAddr`。 |
| `data_dir` | `./data` | 会话 transcript（`.raw` 日志）根目录。 |
| `default_shell` | `bash` | `mode=local` 未给命令时用的 shell。 |
| `ssh_user` | （空） | `mode=ssh` 必填。 |
| `ssh_opts` | `-tt -o LogLevel=error -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=4` | 预设 SSH 参数；保留 `-tt`。 |
| `shell_switch_commands` | `ssh, su, sudo -i, sudo su, docker exec, kubectl exec, nsenter, chroot` | 触发切 shell 后自动重新布哨的命令；可追加（如进容器命令）。 |
| `auto_rearm` | `true` | 切 shell 后自动重新布哨。`false` = 仅手动 `terminal_control(rearm)`。 |
| `tool_descriptions` | （空） | 按工具名覆盖对外工具描述的 TOML 表（`[tool_descriptions]`，key 如 `terminal_open`）。缺省/空串沿用内置默认。也可用 `SetToolDescriptions` 以编程方式覆盖（优先级更高）。 |
| `disable_local_mode` | `false` | 可选加固，默认关。`true` 时禁止 `mode=local`（不暴露 MCP 宿主机本地 shell），仅允许 ssh 会话。 |
| `ssh_login_prologue` | （空） | 可选。ssh 前在本地 shell 执行的一条命令，常用于鉴权登录（凭证需与 ssh 同会话时）。多条用 `&&` 连接即可——整体包在 `sh -c '{...} && exec ssh'` 里，成功后 exec 顶替进程、不残留本地 shell，失败则短路退出，均不逃逸。留空则 ssh 直连。 |
| `max_sessions` | `2` | 并发会话上限。 |
| `idle_ttl_minutes` | `30` | 空闲会话 GC 超时。 |
| `transcript_retention_days` | `7` | `.raw` transcript 保留天数。 |
| `max_buffer_bytes` | `8388608`（8 MiB） | 每会话内存尾部缓存上限。会话完整输出是磁盘上的 append-only `.raw` 日志（唯一真相源），内存只保留最近这么多字节，更早内容按需从磁盘回读，防止流式输出把内存顶爆。 |
| `exec_output_max_bytes` | `1048576`（1 MiB） | 单次 `terminal_send`/`terminal_output` 返回上限。超出会带 `output_ref`，用 `terminal_explore(op=stat|grep|read)` 按需探索固定快照。 |
| `log_dir` | `log` | 审计 + 运行日志目录。 |
| `log_rotate` | `daily` | `daily` 或 `hourly`（按时间切割，非按大小）。 |
| `log_max_age_days` | `30` | 切割日志保留天数。 |
| `audit_log` | （空） | 审计日志文件名前缀（空 = `audit`）。 |

可复制 `config.example.toml` 作为起点。
