# terminal-mcp

[English](README.md) | **简体中文**

[![Go Reference](https://pkg.go.dev/badge/github.com/fzxbl/terminal-mcp.svg)](https://pkg.go.dev/github.com/fzxbl/terminal-mcp)
[![Go 1.25+](https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![MCP](https://img.shields.io/badge/MCP-Model%20Context%20Protocol-6E56CF)](https://modelcontextprotocol.io)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Stars](https://img.shields.io/github/stars/fzxbl/terminal-mcp?style=social)](https://github.com/fzxbl/terminal-mcp/stargazers)

**给 AI Agent 的真正 PTY 终端 —— 全程可观测，人可无缝接管。**

`terminal-mcp` 是一个 [Model Context Protocol](https://modelcontextprotocol.io) 服务器：它给大模型 Agent 一个**真正的 PTY 终端**（而不是一次性的 `exec` 管道）。人可以**实时看到 Agent 的每一步操作**，在**它走错路的那一刻立刻接管同一个会话**动手纠正，而 Agent 随后会**带着你刚才所有操作的完整上下文无缝继续**。本地 shell、`ssh`、容器、`gdb`、`python`、`top`、`vim`——Agent 像人一样操作它们，而你永远不会被"锁在门外"。

> 一个终端，人和 Agent 共用。Agent 负责驾驶，你实时旁观、关键时刻接手方向盘、再交还回去——全程不掉链子。

Go 编写，单二进制，官方 MCP SDK，Streamable HTTP。可直接接入 Cursor、Claude、Comate 或任何 MCP 客户端。

---

## 为什么用 terminal-mcp

大多数"执行命令"工具给模型的是一个无状态管道：一条命令进、一坨输出出。遇到任何交互式场景（登录、REPL、需要保持的会话）就崩；更要命的是，当 Agent 走偏时，**人既看不到过程、也没法插手**。

terminal-mcp 把这几件事做到了极致：

- **1. 真 PTY，不是管道。** 真正的伪终端 + 持久会话。交互式与全屏程序（`gdb`、`python`、`mysql`、`top`、`vim`）、行编辑、颜色、提示符、控制键，全都和人用时一模一样。不是一次性 `exec`。
- **2. 全程可观测——和 Agent 看同一块屏。** 每个会话都有一个实时网页终端链接。打开它，就能实时看到 Agent 产生的每一个字节、每一次按键——不用再猜你的 Agent 到底干了什么。
- **3. 接管即改、交回即续——人机同驾一个会话。** 看到 Agent 走偏？点「接管」直接往运行中的 PTY 里敲字，它的写入立刻暂停；你亲手把事情摆正（输密码、跑对命令、把它从坑里捞出来）后点「释放」，你敲过的每条命令都会以 `[rc=n] $ command` + 输出回喂给它，让它**带着"你刚才做了什么"的完整认知**无缝接着干——不用重新解释、不丢状态。
- **4. 给模型加围栏——透明资源限制，切 shell 也逃不掉。** 可配置一条对模型**隐藏**的 `ulimit`：会话启动即注入，每次切进 `ssh` 远端 / `su` / 容器等新 shell 时**自动重注入**。硬限被所有子进程继承，非特权命令再怎么折腾也逃不出上限——给自主 Agent 一道「防跑飞」的资源围栏，模型全程无感知。
- **5. 挂上负载均衡就能横向扩展。** 会话级路由（属主节点编码进 `session_id`、跨节点自动反向代理）让它能挂在 LB 后做多节点部署，容量不够就加机器；配合可配置身份的会话归属隔离，多人、多 Agent 并发也互不串扰。（详见下文「分布式部署与横向扩展」。）

以及让上面这些真正可靠的底层机制：

- **精确的命令边界。** 哨兵提示符让服务端准确知道命令何时结束、退出码是多少——Agent 绝不会把"还在跑"误当成"已完成"。
- **切 shell 也不丢。** `ssh`、`su`、`docker exec`、进容器——哨兵自动重新布置，跨层跳转后跟踪依然有效。
- **对大模型友好的输出。** 剥除 ANSI 转义、暂缓半截转义序列，模型永远不会收到损坏字节。会话日志是磁盘上的 append-only 文件（唯一真相源）+ 有界内存尾部缓存，`cat` 大文件 / `yes` 之类的疯狂输出也顶不爆内存；超大结果以日志区间引用返回、按需分页取回。
- **本地与远程。** `mode=local` 在宿主机跑 shell/命令；`mode=ssh` 连出去。
- **审计与回放。** 每次工具调用记为 JSON（调用方 IP、用户、工具、参数、结果）；每个会话的原始字节流存盘可回放，保留时长可配。

---

## 快速开始

需要 Go 1.25+。

```bash
go install github.com/fzxbl/terminal-mcp/cmd/terminal-mcp@latest
terminal-mcp --listen 127.0.0.1:8900
```

MCP 端点在 `/mcp`；某会话的网页终端在 `/debug/terminal/<session_id>`。

## 在 AI 编码工具里使用

terminal-mcp 通过 **Streamable HTTP** 提供 MCP 服务。把客户端指向 `http://127.0.0.1:8900/mcp`。

通用 MCP 客户端配置（`mcp.json`）：

```jsonc
{
  "mcpServers": {
    "terminal": { "url": "http://127.0.0.1:8900/mcp" }
  }
}
```

然后直接对你的 Agent 说：

> "开个终端，ssh 到 staging，tail 一下服务日志，告诉我为什么在报 500。"

Agent 会开会话、跑命令、把结果流式带回。如果它需要输密码、或你想插手，打开返回的 `terminal_url` 接管即可——Agent 会等待、观察、然后接着干。

## 人工接管，20 秒上手

1. Agent 调用 `terminal_open` → 拿到 `terminal_url`。
2. 你在浏览器打开它，点**接管**。
3. 会话返回 `held=true`，Agent 的写入暂停。
4. 你敲命令（输密码、跑个有风险的命令、随便看看）。Agent 会看到你的每条命令被还原成 `[rc=n] $ cmd`。
5. 你点**释放**，Agent 从你停下的地方接着继续。

## 工具

| 工具 | 作用 |
| --- | --- |
| `terminal_open(mode, command?, host?)` | 起一个持久 PTY 会话。`mode=local` 或 `mode=ssh`。返回 `session_id` + `terminal_url`。 |
| `terminal_send(session_id, input, wait_ms?)` | 输入命令并等它稳定，返回输出、状态、退出码。 |
| `terminal_read(session_id, wait_ms?, mode?, from?, to?)` | 观察输出。`tail`（瞥一眼）、`since_last`（完整增量；也是人工接管命令的记录来源）或 `range`（分页读日志绝对区间 `[from,to)`；用截断结果里回传的 `range` 翻页）。 |
| `terminal_control(session_id, key)` | 发送控制键（`ctrl-c`、`ctrl-d`、`ctrl-z` …）或恢复动作（`flush`、`hard`、`rearm`）。 |
| `terminal_status(session_id)` | 轻量查询 状态 / 提示符 / 退出码 / 是否被接管。 |
| `terminal_close(session_id)` | 关闭会话，回收进程组。 |
| `terminal_list()` | 列出活跃会话。 |

## 配置

复制 `config.example.toml`。要点：`listen_addr`、`data_dir`、`default_shell`、`ssh_user`、`ssh_opts`、`shell_switch_commands`（触发自动重新布哨的命令——可自行追加，如进容器命令）、`auto_rearm`、`max_buffer_bytes`（内存尾部缓存上限；完整日志落磁盘）、`exec_output_max_bytes`（单次返回上限；超出以分页区间返回）、`transcript_retention_days`、`log_dir` / `log_rotate` / `log_max_age_days`。

**透明资源护栏（`resource_limit_cmd`）**：配一条对模型隐藏的 `ulimit`，随哨兵在会话启动时注入，并在每次切进新一层 shell（`ssh`/`su`/`docker exec`/`matrix_jail` …）与 `hard` reset 时自动重注入。`ulimit` 不带 `-S/-H` 时同时设软硬限，硬限被子进程继承，非特权命令无法调高，故切 shell、跑别的命令都逃不出上限：

```toml
resource_limit_cmd = "ulimit -v 4194304; ulimit -t 600; ulimit -u 4096"
```

> 边界：`ulimit` 约束不了「提权到 root 后调高自身硬限」或「由守护进程另起、不继承本会话的进程树」；要与权限无关地强约束本机进程树，请改用 cgroup v2。

**自定义工具描述**：用 `[tool_descriptions]` 按工具名覆盖对外暴露给模型的工具说明（集成到外部 MCP、想按自家话术改写时用；缺省或留空的工具沿用内置默认）：

```toml
[tool_descriptions]
terminal_open = "打开一个持久终端会话并返回 session_id 与只读网页终端地址。"
terminal_send = "向会话输入一条命令并等待其结束，返回新增输出与状态。"
```

## 作为库嵌入（共用一个 MCP server）

已经在跑一个 MCP server、想把终端工具挂到同一个 `/mcp`？`go get github.com/fzxbl/terminal-mcp`，注册到你自己的官方 SDK server 上：

```go
mcpserver.Init("config.toml")
mcpserver.SetAdvertiseAddr("10.0.0.5:8080") // 用于拼 terminal_url 的 host:port
mcpserver.SetToolDescriptions(map[string]string{ // 可选：按自家话术改写工具描述
    "terminal_open": "打开一个持久终端会话并返回 session_id 与只读网页终端地址。",
})
mcpserver.StartIdleGC(ctx)

mcpserver.RegisterTools(server, auditWriter)          // terminal_* 工具
mux.Handle("/debug/terminal/", mcpserver.TerminalHandler()) // 网页终端（人工接管）
```

`/mcp` 路径由你决定；网页终端前缀 `/debug/terminal/` 固定。工具描述也可用配置文件 `[tool_descriptions]` 覆盖（优先级：`SetToolDescriptions` > 配置文件 > 内置默认）。完整公开 API、共用 `/mcp` 的接入范式、路径约定、鉴权注意事项与配置说明见 **[docs/EMBEDDING.zh-CN.md](docs/EMBEDDING.zh-CN.md)**。

## 分布式部署与横向扩展

**它不是完全无状态的**——每个会话背后是一个绑定在某台节点上的**活的 PTY 进程 / ssh 连接**，这类内核资源无法跨进程/跨节点迁移。但通过**会话级路由**，可以把服务挂在负载均衡器后做**横向扩展**：

- **MCP 传输层无状态**：内部启用 SDK 的 stateless 模式，任意节点都能独立处理任意请求，客户端不会被"粘"在某个节点上。
- **会话级路由**：`session_id` 内编码了属主节点地址（`<host:port>~<uuid>`）。任意节点收到针对某会话的调用时，若属主不是自己，就把请求**反向代理**到属主节点。客户端全程只跟 LB / 任一节点对话，无需感知拓扑。
- **横向扩展**：需要更多并发会话容量时直接加节点。单节点宕机只影响其上的会话（不做 PTY 迁移），其余节点不受影响。

配套两项能力：

- **会话归属隔离**：可配置一组请求头（`[identity]`）计算客户端签名；客户端只能看到/操作自己开的会话，越权访问一律返回 `session not found`。`terminal_list` 会跨节点 fan-out 聚合出该客户端的全局会话视图。
- **兄弟节点发现**：反代无需清单（属主地址已在 `session_id` 里）；`terminal_list` 聚合需要节点清单，可用静态配置 `peers`，或注册动态服务发现函数 `SetPeerProvider(func() []string)` 对接 Consul/etcd/DNS/K8s Endpoints 等。该清单同时用作反代白名单（只允许反代到已知节点，避免被客户端可控的 `session_id` 诱导访问任意地址）。

最小配置（**所有节点共用同一份**）：

```toml
# 绑所有网卡即可；进程会自动探测本机可达 IP 作为属主地址，无需每实例配不同地址
listen_addr = "0.0.0.0:8900"
# 用于 terminal_list 跨节点聚合的兄弟节点（也可用 SetPeerProvider 动态提供，从而完全免配置）
peers = ["10.0.0.11:8900", "10.0.0.12:8900", "10.0.0.13:8900"]

[identity]
headers = ["X-MCP-USER"]   # 由可信网关注入的身份头
mode = "raw"                # raw | sha256
on_missing = "reject"       # reject | allow_empty
```

> 部署要点：身份头**只能由可信网关注入**，节点不应直接信任客户端自带的该头。属主地址默认由进程自动探测（把通配 `0.0.0.0` 解析为本机实际 IP），因此各实例可共用同一份配置；仅当跨 NAT/需对外映射地址时，才用嵌入接口 `SetAdvertiseAddr` 显式覆盖。`peers` 也可用 `SetPeerProvider` 动态发现，做到分布式下完全免配置。

## 安全

**该 HTTP 服务（MCP 端点 + 网页终端）默认无鉴权。** 调用这些工具等同于在宿主机（或经 ssh）上执行任意命令。在暴露到可信网络之外前，请绑定回环地址，或置于带鉴权的反向代理之后。

## 许可证

MIT。欢迎贡献与 star。
