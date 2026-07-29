# 给 AI Agent 一个真正的终端：开源 MCP 工具 terminal-mcp，让人和 AI 共用一个 Shell

> terminal-mcp 是一个开源的 [Model Context Protocol](https://modelcontextprotocol.io)（MCP）服务器，给大模型 Agent 一个**真正的 PTY 终端**，而不是一次性的 `exec` 管道。它的核心是「人 + AI 共用同一个终端」：**人能实时看到 AI 的每一步操作，在 AI 走错路时立刻接管纠正，AI 又能在你交回控制权后带着完整上下文无缝继续**。支持本地 Shell、SSH、容器、gdb/python/top/vim，可一键接入 Cursor、Claude、Comate 等任意 MCP 客户端。
>
> GitHub：https://github.com/fzxbl/terminal-mcp

关键词：`MCP` `Model Context Protocol` `AI Agent` `terminal-mcp` `PTY 终端` `人工接管` `human-in-the-loop` `AI 运维` `Cursor` `Claude` `Go` `开源`

---

## 一、AI 帮你敲命令，为什么总让人不放心？

现在的 AI 编码 / 运维工具大多用一个「执行命令」工具：**一条命令进、一坨输出出**。看着方便，真用起来有两个硬伤：

1. **它不是终端，是管道。** 遇到任何交互式场景就崩——登录要输密码、进 `python`/`gdb` 这种 REPL、`top`/`vim` 这种全屏程序、一个需要保持状态的会话，管道式 `exec` 全搞不定。
2. **人被「锁在门外」。** AI 在后台闷头跑，你既**看不到它到底做了什么**；等发现它走偏了，也**没法当场插手**——只能等它把事情搞砸，再回滚。

在真实的线上排障里，这两点会被无限放大：一个几十 GB 的 core、一次需要人扫码鉴权的登录、一条危险的删除命令……你敢让 AI 全自动跑吗？

terminal-mcp 就是来解决这件事的：**让 AI 像人一样使用终端，同时让人始终在场。**

## 二、terminal-mcp 的四个核心优势

### 1. 真 PTY，不是管道

terminal-mcp 给 Agent 的是一个**真正的伪终端（PTY）+ 持久会话**。行编辑、颜色、提示符、控制键、全屏程序，全都和人用时一模一样。`gdb`、`python`、`mysql`、`top`、`vim` 直接能跑，会话状态一直在，而不是「一次性执行完就丢」。

### 2. 全程可观测：实时看 AI 在做什么

每个会话都自带一个**实时网页终端链接**。打开它，你能实时看到 Agent 产生的每一个字节、每一次按键——**和 AI 看到的是同一块屏幕**。AI 干了什么，一目了然，不用猜、不用事后翻日志。

### 3. 即时人工接管：走错立刻纠正

发现 AI 方向不对？点一下「接管」，直接往运行中的终端里敲字。**AI 的写入会被立即暂停**，你亲手把事情摆正——输个密码、跑对命令、把它从坑里捞出来。这就是真正的 human-in-the-loop，而不是「事后回滚」。

### 4. 无缝交回：AI 带着完整上下文继续

你操作完、释放控制权后，**你敲的每条命令都会以 `[rc=n] $ command` + 输出的形式回喂给 AI**，它就能**带着「你刚才做了什么」的完整认知**接着干——不用重新解释、不丢状态。人和 AI 之间是真正的接力，而不是各干各的。

> 一句话：**一个终端，人和 AI 共用。AI 负责驾驶，你实时旁观、关键时刻接手方向盘、再交还回去——全程不掉链子。**

## 三、还有哪些让它「可靠」的细节

- **精确的命令边界**：用哨兵提示符判定命令何时结束、退出码是多少，AI 不会把「还在跑」误当成「已完成」。
- **切 Shell 也不丢**：`ssh` / `su` / `docker exec` / 进容器后，会自动重新布置哨兵，跨层跳转依然能精确跟踪命令。
- **对大模型友好的输出**：剥除 ANSI 转义、拦截半截转义序列，模型不会收到损坏字节；超大输出自动转存、按需取回。
- **本地与远程**：`mode=local` 在本机跑；`mode=ssh` 连出去，天然就是远程终端。
- **审计与回放**：每次调用记 JSON（调用方、工具、参数、结果），每个会话原始字节流存盘可回放。

## 四、30 秒上手

```bash
go install github.com/fzxbl/terminal-mcp/cmd/terminal-mcp@latest
terminal-mcp --listen 127.0.0.1:8900
```

在你的 MCP 客户端（Cursor / Claude / Comate 等）里配置：

```json
{
  "mcpServers": {
    "terminal": { "url": "http://127.0.0.1:8900/mcp" }
  }
}
```

然后直接对 AI 说：

> 「开个终端，ssh 到 staging，tail 一下服务日志，告诉我为什么在报 500。」

AI 会开会话、跑命令、把结果带回来。需要输密码或你想插手时，打开返回的终端链接接管即可——AI 会等你、看着你操作、然后接着干。

## 五、它是怎么工作的（架构一览）

- 基于官方 MCP SDK，通过 **Streamable HTTP** 暴露 `/mcp` 工具端点；
- 每个会话是一个真 PTY 子进程，后台读线程把输出同时喂给：模型（`terminal_read`）、网页终端（SSE 实时渲染）、以及落盘的回放文件；
- 人工接管时，浏览器按键经 WebSocket 写回 PTY，模型写入被拦截；
- 一组工具覆盖全生命周期：`terminal_open / send / read / control / status / close / list`，外加大输出取回 `terminal_spill_explore`。

## 六、适合谁用

- **AI 运维 / SRE**：让 Agent 上机排障（看日志、抓栈、跑诊断命令），关键操作人来把关。
- **AI 编码助手**：给 Agent 一个持久 Shell 跑构建、测试、调试，而不是每条命令重开进程。
- **需要 human-in-the-loop 的自动化**：任何「AI 做大部分、人管关键一步」的流程。

## 七、开源地址与共建

terminal-mcp 用 Go 写成，单二进制，基于官方 MCP SDK，Streamable HTTP，可独立部署，也可作为库嵌入你已有的 MCP 服务、与其它工具共用一个 `/mcp`。

**GitHub：https://github.com/fzxbl/terminal-mcp**

如果这个思路对你有用，欢迎去仓库点个 Star、提 Issue 或 PR 一起共建。

---

<!-- ====== 以下为发布备注，掘金发布前请删除 ======
- 标题可 A/B：①给 AI Agent 一个真正的终端…… ②AI 排障总怕它乱来？terminal-mcp…… ③真 PTY + 人工接管：我开源了……
- 建议配图：网页终端「人工接管」截图、架构图；图片 alt 带关键词。
- 多平台同步：掘金 / 公众号 / 知乎 / CSDN / SegmentFault / Medium(英文) / dev.to，正文均带 GitHub 链接。
- 掘金标签：MCP、AI、Go、开源、运维、大模型。
==================================================== -->
