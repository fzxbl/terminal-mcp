# terminal-mcp 存储层重构：以 append-only 日志为真相源（oplog）

日期：2026-07-28
状态：设计已确认，待转 implementation plan
适用仓库：`github.com/fzxbl/terminal-mcp`（无外部调用方，可自由改接口与签名）

## 背景与动机

当前会话输出存储在 `ProcSession.buf []byte` 中：append-only、**无内存上限**；所有读取方（模型 `since_last` 游标、Web SSE、`tail`、人工接管区间）都直接耦合到"对这块内存 slice 的绝对整数偏移"。同时另有两套持久化：`transcript <id>.raw`（逐块 write-through 全量落盘）与 `spill <uuid>.txt`（单次超大返回结果的副本）。

问题：

1. **内存无界**：`cat 大文件` / `yes` / `tail -f` 等流式输出会让 `buf` 实时暴涨，可能在任何 spill 触发前就 OOM。spill 只在命令结束、`finalize()` 时对"已完整驻留内存"的结果做转存，**不构成内存上界**。
2. **难演进**：逻辑偏移与物理存储焊死，加内存上限需回填 base/clamp/空洞，牵动所有偏移消费方。
3. **三套冗余**：buffer、transcript、spill 各管一摊，概念重复。

`.raw` 本就是每字节的持久全量日志。最优方向是：**让日志成为唯一真相源，内存只做有界尾部缓存，偏移=日志绝对字节位置**；如此 buffer/spill/transcript 收敛为"同一条日志的不同视图"。

## 已确认的核心决策

- **Q1=A 全量日志为真相源，无空洞。** 内存有界；早于缓存窗口的字节从 `.raw` 回读，历史始终可取。
- **Q2=A spill 并入日志。** 删除独立 spill 文件与 `terminal_spill_explore`；单次返回超限时回"日志区间引用" `{from,to}`，模型用新增 `terminal_read(mode=range, from, to)` 分页取。
- **Q3=A `.raw` 为强依赖。** 打不开则 `terminal_open` 直接失败，"日志=真相源"为硬不变量，读路径无"无文件"分支。
- **Q4=A 单调连续绝对偏移。** 偏移即 `.raw` 文件位置，跨 hard reset 不回退；hard reset 把 `delivered` 推到当前文件末尾（从新 shell 看起），历史仍可回读。
- **实现取向=2 独立 `internal/oplog` 组件。** 偏移语义/clamp/冷读回落集中于一个可独立单测的单元；`ProcSession` 委托给它。

## 架构总览

```
PTY master ──reader goroutine──▶ oplog.Append(bytes)
                                    │  唯一写入口：落 .raw 文件 + 更新尾部缓存 + 推进 total
       ┌────────────────────────────┼───────────────────────────┐
       ▼                            ▼                             ▼
 session 层                    terminal SSE                  历史/冷读
 Send/Read(since_last/         SnapshotLen + ReadRange       ReadRange(旧区间)
 range/tail)、rearm、          增量推流                       命中缓存走内存 / 否则读 .raw
 takeover 区间
```

- 唯一真相源：`oplog` 背后的 `.raw`（强依赖）。
- 内存有界：`oplog` 仅保留最近 N 字节尾部缓存；更早内容从 `.raw` 回读，无空洞。
- 偏移=绝对文件位置：全局单调、跨 hard reset 连续。
- buffer/spill/transcript 三合一。

## 组件设计

### internal/oplog（新增，核心）

对外接口（签名可调）：

```go
type Log struct { /* 文件句柄 + 尾部环形缓存 + total + mu */ }

func Open(path string, cacheBytes int) (*Log, error) // .raw 打不开则报错（Q3-A）
func (l *Log) Append(b []byte) error                 // 唯一写入：写文件 + 更新缓存 + total += len
func (l *Log) Len() int64                            // 绝对总字节数（= 文件长度）
func (l *Log) ReadRange(from, to int64) ([]byte, error) // 任意绝对区间；缓存命中走内存，否则读文件
func (l *Log) Tail(n int) []byte                     // 最近 n 字节（缓存足够时纯内存）
func (l *Log) Close() error
```

内部存储与语义：

- 尾部缓存为固定容量环形缓冲（`cacheBytes`），记录 `cacheStart`（缓存首字节的绝对偏移）。
- `ReadRange(from, to)`：
  - `from >= cacheStart` → 全走内存缓存；
  - `from < cacheStart` → `[from, min(to, cacheStart))` 从 `.raw` 用 `ReadAt` 读，其余部分拼接缓存；
  - 越界（`from < 0`、`to > Len`、`from > to`）一律 clamp 到真实可得区间，**永不空洞**，不向上层返回错误。
- 并发：单 writer（reader goroutine）+ 多 reader。`Append` 持写锁更新缓存/total；读先持读锁取 `total`/`cacheStart` 快照，再对 append-only 的 `.raw` 做 `ReadAt`（只读已写入区间，安全）。

### internal/pty/proc.go 改造

- 移除 `buf []byte` 与 `tf *os.File`，改持有 `log *oplog.Log`。
- reader goroutine：`n,_ := ptmx.Read(b)` → `p.log.Append(b[:n])`（不再有 `buf=append` 与单独 `tf.Write`）。
- 方法调整：
  - `SnapshotLen() int` → `Len() int64`
  - `Since(off)` → 由 `ReadRange(off, Len())` 实现（保留 `Since` 名转发亦可）
  - `Tail(n)` → `log.Tail(n)`
  - 新增 `ReadRange(from, to)`
- `AttachTranscript` 并入 `oplog.Open`（路径仍 `<dir>/<id>.raw`，hard reset 复用同一文件→连续追加，支撑 Q4-A）。`ReadTranscript`（关闭后回看）改为直接读该 `.raw`，行为不变。
- proc.go 回归"只管 PTY 读写 + 信号 + 生命周期"。

### internal/session 层适配

偏移类型统一 `int → int64`（`deliveredOffset`、`takeoverStart/End`、`Send.start`、`armStart`）。

- **Send**：`start := proc.Len()` → 写命令 → `raw := proc.ReadRange(start, proc.Len())` → `setDelivered(start + int64(k))`。语义不变。
- **Read(since_last)**：`off := delivered` → `ReadRange(off, Len())` → `setDelivered(off + k)`。冷区间可回读，区间读天然一致，消除"推过头丢字节"。
- **Read(tail)**：`proc.Tail(TailBytes)`，不动游标。
- **hard reset**（Q4-A）：不再 `setDelivered(0)`；改 `setDelivered(proc.Len())`（跳到末尾，从新 shell 看起），`.raw` 继续追加。
- **rearm / sendWithRearm**：`armStart := proc.Len()`；布哨噪声丢弃 = `setDelivered(proc.Len())`，逻辑不变。
- **takeover 区间**：`takeoverStart/End` 用 `proc.Len()` 取绝对偏移；observe 还原用 `ReadRange` 取该区间，不变。
- **超限返回（替代 spill，Q2-A）**：由调用方（`Send`/`Read`）把本次交付的绝对区间 `{from,to}` 传给 `finalize`（`finalize` 签名需扩展以接收该区间）；当 `to-from > exec_output_max_bytes` 时不写 spill 文件，改为返回"头部预览 + `Envelope.Range{From,To}`"；`delivered` 推到 `to`（模型已获得可回读引用）。`Send` 与 `Read(since_last)` 均走此路径。

### internal/terminal 层与工具面

- **SSE**：`off:=0` → `total:=SnapshotLen()` → `Since(off,total)` 循环，改走新 `Len/ReadRange`；首连若缓存已淘汰早期内容，冷区间从 `.raw` 回读（无空洞）。
- **工具面变化**：
  - `terminal_read` 新增 `mode=range`，入参 `from,to`（读日志任意区间，替代 spill_explore）。
  - 删除 `terminal_spill_explore` 工具、`SpillResult/ReadSpill/SpillRead`、`Envelope.SpillID`、`spill_dir` 相关代码。
  - `Envelope` 新增 `Range *struct{ From, To int64 }`（超限时给引用）。

## 配置项

- 新增 `max_buffer_bytes`：oplog 尾部缓存上限，默认 8 MiB。
- 保留并在 `config.example.toml` 补文档 `exec_output_max_bytes`：单次返回上限（超出触发 range 引用），默认 1 MiB。
- 移除 `spill_dir`（并入日志）。
- `transcript_dir` / `transcript_retention_days` 保留：`.raw` 即真相源，保留策略照旧对**已关闭**会话按 mtime 清理。

## 错误处理

- `oplog.Open` 失败 → `terminal_open` 失败（Q3-A 硬不变量）。
- `Append` 写盘失败 → 标记 proc dead + 记日志（写不了日志=会话不可信，主动终止优于静默丢数据）。
- `ReadRange` 越界一律 clamp 到 `[0, Len]`，不向上层返回错误。

## 测试策略

- **oplog 单测**：小 `cacheBytes` 触发淘汰，验证热读/冷读/跨缓存边界拼接与全量一致；`ReadRange` clamp；并发 append+read。
- **session 层**：现有 `Send/Read/takeover/rearm/hard reset` 测试沿用（行为不变；仅 hard reset 后 `delivered` 语义微调，更新对应断言）；新增 `mode=range` 分页取回超大输出测试。
- **terminal SSE**：现有测试沿用；新增"缓存淘汰后冷区间仍能推回"测试。
- 删除 spill 相关测试，替换为 range 读测试。

## 影响与迁移

- 无外部调用方，可直接改接口/签名，无兼容包袱。
- 观察行为对齐：模型 `since_last`、SSE 推流、takeover 还原、`.raw` 全量回放均保持等价；唯一有意的行为变更是 hard reset 后偏移不再归零（改为跳末尾，历史可回读）与 spill→range 的工具面替换。
- 内存从"无界"变为"有界（`max_buffer_bytes`）+ 冷读回盘"。

## 非目标（YAGNI）

- 不做单命令字节预算 / 自动 `ctrl-c` 打断（另议）。
- 不做 mmap / 日志分段轮转（`.raw` 单文件 + 现有 mtime 清理已够）。
- 不引入 cgroup 等产出端资源限制（与本存储层重构正交）。
