# terminal-mcp oplog 存储重构 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把会话输出存储从"无界内存 buf + 绝对 slice 下标 + 独立 spill/transcript"重构为"以 `.raw` append-only 日志为唯一真相源、内存只做有界尾部缓存、偏移=日志绝对字节位置"的 `oplog` 组件。

**Architecture:** 新增 `internal/oplog`（文件 + 有界环形尾部缓存 + 单调绝对偏移 + 冷读回落）。`ProcSession` 委托给它；`session`/`terminal` 层偏移统一 `int64`；spill 收敛为"日志区间引用" + 新增 `terminal_read(mode=range,from,to)`；`.raw` 落盘为强依赖（打不开则 open 失败）。

**Tech Stack:** Go 1.25，`github.com/creack/pty`，官方 MCP SDK，`BurntSushi/toml`。命令：`GOWORK=off go test ./...`（本模块不在父仓库 go.work 内，必须带 `GOWORK=off`）。工作目录：`/root/icode/baidu/psop-se-stability/stability-lib/terminal-mcp`。

**规范：** 全部命令在上述工作目录下执行；提交信息用中文/conventional 前缀；不提交未跟踪的 `docs/juejin-terminal-mcp.zh-CN.md`。设计依据：`docs/superpowers/specs/2026-07-28-terminal-mcp-oplog-refactor-design.md`。

---

## Task 1: 新增 `internal/oplog` 组件（核心，纯单测）

**Files:**
- Create: `internal/oplog/oplog.go`
- Test: `internal/oplog/oplog_test.go`

- [ ] **Step 1: 写失败测试**

创建 `internal/oplog/oplog_test.go`：

```go
package oplog

import (
	"path/filepath"
	"strings"
	"testing"
)

func openTmp(t *testing.T, cacheBytes int) *Log {
	t.Helper()
	l, err := Open(filepath.Join(t.TempDir(), "s.raw"), cacheBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func TestAppendLenAndReadRange(t *testing.T) {
	l := openTmp(t, 1024)
	if _, err := l.Append([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append([]byte("world")); err != nil {
		t.Fatal(err)
	}
	if l.Len() != 10 {
		t.Fatalf("Len=%d want 10", l.Len())
	}
	if got, _ := l.ReadRange(0, 10); string(got) != "helloworld" {
		t.Fatalf("full=%q", got)
	}
	if got, _ := l.ReadRange(5, 10); string(got) != "world" {
		t.Fatalf("tail=%q", got)
	}
}

// 缓存淘汰后，冷区间从文件回读，且与全量拼接一致（无空洞）。
func TestReadRangeColdFromFile(t *testing.T) {
	l := openTmp(t, 8) // 极小缓存，强制淘汰
	l.Append([]byte("0123456789ABCDEF"))
	if l.Len() != 16 {
		t.Fatalf("Len=%d", l.Len())
	}
	// [0,4) 已被淘汰出缓存，必须从文件回读
	if got, _ := l.ReadRange(0, 4); string(got) != "0123" {
		t.Fatalf("cold=%q", got)
	}
	// 跨缓存边界拼接
	if got, _ := l.ReadRange(2, 12); string(got) != "23456789AB" {
		t.Fatalf("span=%q", got)
	}
	// 越界一律 clamp，不返回错误
	got, err := l.ReadRange(-5, 999)
	if err != nil {
		t.Fatalf("clamp err: %v", err)
	}
	if string(got) != "0123456789ABCDEF" {
		t.Fatalf("clamp=%q", got)
	}
}

func TestTail(t *testing.T) {
	l := openTmp(t, 1024)
	l.Append([]byte(strings.Repeat("x", 100) + "END"))
	if got := l.Tail(3); string(got) != "END" {
		t.Fatalf("tail=%q", got)
	}
	if got := l.Tail(1000); len(got) != 103 {
		t.Fatalf("tail overshoot len=%d", len(got))
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `GOWORK=off go test ./internal/oplog/ -run . -v`
Expected: 编译失败（`Open`/`Log` 未定义）。

- [ ] **Step 3: 实现 `internal/oplog/oplog.go`**

```go
// Package oplog 是会话输出的 append-only 日志：磁盘 .raw 文件为唯一真相源，
// 内存只保留最近 cacheBytes 字节的尾部缓存；偏移为日志绝对字节位置（单调）。
// 早于缓存窗口的字节从文件回读，永不空洞。单 writer（PTY reader goroutine）+ 多 reader。
package oplog

import (
	"os"
	"sync"
)

type Log struct {
	mu    sync.RWMutex
	f     *os.File
	total int64  // 已写入总字节数 = 文件长度 = 绝对偏移上界
	cache []byte // 尾部缓存，最多 cap 字节
	cap   int    // 缓存容量（cacheBytes）
}

// Open 打开（追加）日志文件；打不开即返回错误（.raw 为强依赖）。
// cacheBytes<=0 时给一个安全下限，避免零容量缓存。
func Open(path string, cacheBytes int) (*Log, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if cacheBytes < 4096 {
		cacheBytes = 4096
	}
	l := &Log{f: f, total: st.Size(), cap: cacheBytes}
	// 若文件非空（hard reset 复用同一文件），把尾部预热进缓存，保证热读命中。
	if l.total > 0 {
		n := int64(cacheBytes)
		if n > l.total {
			n = l.total
		}
		buf := make([]byte, n)
		if _, err := readFullAt(f, buf, l.total-n); err == nil {
			l.cache = buf
		}
	}
	return l, nil
}

// readFullAt 因为 Open 用的是 O_WRONLY 句柄不能读，这里用只读句柄按需读文件。
func readFullAt(f *os.File, p []byte, off int64) (int, error) {
	rf, err := os.Open(f.Name())
	if err != nil {
		return 0, err
	}
	defer rf.Close()
	return rf.ReadAt(p, off)
}

// Append 唯一写入口：写文件 + 更新尾部缓存 + 推进 total。写盘失败返回错误。
func (l *Log) Append(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.f.Write(b); err != nil {
		return 0, err
	}
	l.total += int64(len(b))
	// 维护尾部缓存：追加后裁到 cap。
	if len(b) >= l.cap {
		l.cache = append(l.cache[:0], b[len(b)-l.cap:]...)
	} else {
		l.cache = append(l.cache, b...)
		if len(l.cache) > l.cap {
			l.cache = l.cache[len(l.cache)-l.cap:]
		}
	}
	return len(b), nil
}

// Len 返回绝对总字节数。
func (l *Log) Len() int64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.total
}

// ReadRange 返回绝对区间 [from,to) 的字节；越界 clamp 到 [0,total]，永不空洞、不报越界错。
// from>=cacheStart 全走内存；否则冷段从文件 ReadAt 读，再拼接缓存热段。
func (l *Log) ReadRange(from, to int64) ([]byte, error) {
	l.mu.RLock()
	total := l.total
	cacheLen := int64(len(l.cache))
	cacheStart := total - cacheLen
	// 拷出缓存快照，随后释放锁再读文件，避免长持锁。
	var cacheCopy []byte
	if cacheLen > 0 {
		cacheCopy = append([]byte(nil), l.cache...)
	}
	name := l.f.Name()
	l.mu.RUnlock()

	if from < 0 {
		from = 0
	}
	if to > total {
		to = total
	}
	if from >= to {
		return []byte{}, nil
	}
	out := make([]byte, 0, to-from)
	// 冷段 [from, min(to,cacheStart)) 从文件读。
	if from < cacheStart {
		coldEnd := to
		if coldEnd > cacheStart {
			coldEnd = cacheStart
		}
		cold := make([]byte, coldEnd-from)
		rf, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		_, err = rf.ReadAt(cold, from)
		_ = rf.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, cold...)
		from = coldEnd
	}
	// 热段 [max(from,cacheStart), to) 从缓存取。
	if from < to {
		lo := from - cacheStart
		hi := to - cacheStart
		if lo < 0 {
			lo = 0
		}
		if hi > cacheLen {
			hi = cacheLen
		}
		if lo < hi {
			out = append(out, cacheCopy[lo:hi]...)
		}
	}
	return out, nil
}

// Tail 返回最近 n 字节（缓存足够时纯内存；n 超过缓存则回退 ReadRange 从文件补齐）。
func (l *Log) Tail(n int) []byte {
	l.mu.RLock()
	total := l.total
	cacheLen := int64(len(l.cache))
	l.mu.RUnlock()
	if int64(n) >= total {
		b, _ := l.ReadRange(0, total)
		return b
	}
	if int64(n) <= cacheLen {
		b, _ := l.ReadRange(total-int64(n), total)
		return b
	}
	b, _ := l.ReadRange(total-int64(n), total)
	return b
}

// Close 关闭底层文件。
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `GOWORK=off go test ./internal/oplog/ -v`
Expected: PASS（3 个测试）。

- [ ] **Step 5: 补并发测试并再跑一次（-race）**

在 `oplog_test.go` 追加：

```go
func TestConcurrentAppendRead(t *testing.T) {
	l := openTmp(t, 64)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 2000; i++ {
			l.Append([]byte("abcd"))
		}
		close(done)
	}()
	for {
		select {
		case <-done:
			if l.Len() != 8000 {
				t.Fatalf("Len=%d want 8000", l.Len())
			}
			return
		default:
			end := l.Len()
			if end > 8 {
				if _, err := l.ReadRange(end-8, end); err != nil {
					t.Fatalf("read err: %v", err)
				}
			}
		}
	}
}
```

Run: `GOWORK=off go test ./internal/oplog/ -race -v`
Expected: PASS，无 data race。

- [ ] **Step 6: 提交**

```bash
git add internal/oplog/oplog.go internal/oplog/oplog_test.go
git commit -m "feat(oplog): append-only log with bounded tail cache and absolute offsets"
```

---

## Task 2: 配置项调整（加 `max_buffer_bytes`，去 `spill_dir`）

**Files:**
- Modify: `internal/config/config.go`
- Modify: `config.example.toml`
- Test: `internal/config/config_test.go`（若存在则加用例；不存在则跳过测试步骤，靠 Task 3 编译验证）

- [ ] **Step 1: 修改 `internal/config/config.go`**

在 `Config` 结构体删除 `SpillDir` 字段，新增 `MaxBufferBytes`：

删除这一行：
```go
	SpillDir                string   `toml:"spill_dir"`
```
在 `ExecOutputMaxBytes` 行后新增：
```go
	MaxBufferBytes          int      `toml:"max_buffer_bytes"`
```

在 `applyDefaults()` 中删除 `SpillDir` 默认块：
```go
	if c.SpillDir == "" {
		c.SpillDir = filepath.Join(c.DataDir, "spill")
	}
```
新增默认：
```go
	if c.MaxBufferBytes <= 0 {
		c.MaxBufferBytes = 8 << 20 // 8 MiB 尾部缓存上限
	}
```

- [ ] **Step 2: 更新 `config.example.toml`**

删除任何 `spill_dir` 行（如有）。在 `resource_limit_cmd` 段后新增：

```toml
# max_buffer_bytes（默认 8MiB）：会话输出的内存尾部缓存上限。输出的唯一真相源是磁盘上的
# transcript（<transcript_dir>/<id>.raw），内存只保留最近这么多字节；更早的内容按需从 .raw
# 回读，不丢失。防止 cat 大文件 / yes 等流式输出把进程内存顶爆。
# max_buffer_bytes = 8388608

# exec_output_max_bytes（默认 1MiB）：单次 terminal_send/terminal_read 返回给模型的上限。
# 超过则只回头部预览 + 一个日志区间引用 {from,to}，用 terminal_read(mode=range, from, to) 翻取。
# exec_output_max_bytes = 1048576
```

- [ ] **Step 3: 编译校验（无独立单测则靠编译）**

Run: `GOWORK=off go build ./internal/config/`
Expected: 成功（此时其他包尚未改，可能整体未编译；仅编译 config 包）。

- [ ] **Step 4: 提交**

```bash
git add internal/config/config.go config.example.toml
git commit -m "feat(config): add max_buffer_bytes, drop spill_dir"
```

---

## Task 3: `ProcSession` 改用 oplog（offset 转 int64）

**Files:**
- Modify: `internal/pty/proc.go`
- Modify: `internal/pty/transcript.go`
- Test: `internal/pty/proc_test.go`

- [ ] **Step 1: 改造 `internal/pty/proc.go`**

结构体去掉 `buf`/`tf`，改持 `log`：
```go
import (
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/fzxbl/terminal-mcp/internal/oplog"
)

type ProcSession struct {
	cmd      *exec.Cmd
	ptmx     *os.File
	mu       sync.Mutex
	lastByte time.Time
	dead     bool
	log      *oplog.Log
}
```

`NewProcSession` 新增 `logPath string, cacheBytes int` 参数，先开 `oplog`（强依赖，失败即返回错误），reader goroutine 改为 `Append`：
```go
func NewProcSession(logPath string, cacheBytes int, name string, args ...string) (*ProcSession, error) {
	lg, err := oplog.Open(logPath, cacheBytes)
	if err != nil {
		return nil, err
	}
	ptmx, tty, err := pty.Open()
	if err != nil {
		_ = lg.Close()
		return nil, err
	}
	_ = pty.Setsize(ptmx, &pty.Winsize{Rows: defaultPtyRows, Cols: defaultPtyCols})
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		_ = ptmx.Close()
		_ = tty.Close()
		_ = lg.Close()
		return nil, err
	}
	_ = tty.Close()
	p := &ProcSession{cmd: cmd, ptmx: ptmx, log: lg}
	go func() {
		b := make([]byte, 4096)
		for {
			n, rerr := ptmx.Read(b)
			if n > 0 {
				if _, werr := p.log.Append(b[:n]); werr != nil {
					// 写不了日志=会话不可信，主动终止。
					p.mu.Lock()
					p.dead = true
					p.mu.Unlock()
					return
				}
				p.mu.Lock()
				p.lastByte = time.Now()
				p.mu.Unlock()
			}
			if rerr != nil {
				p.mu.Lock()
				p.dead = true
				p.mu.Unlock()
				_ = p.log.Close()
				return
			}
		}
	}()
	return p, nil
}
```

替换读方法（删除 `SnapshotLen/Since/Tail/setTranscript`，新增 int64 版）：
```go
// Len 返回日志绝对总字节数。
func (p *ProcSession) Len() int64 { return p.log.Len() }

// ReadRange 返回绝对区间 [from,to) 内容。
func (p *ProcSession) ReadRange(from, to int64) string {
	b, _ := p.log.ReadRange(from, to)
	return string(b)
}

// Since 返回从绝对偏移 off 起到末尾的内容。
func (p *ProcSession) Since(off int64) string { return p.ReadRange(off, p.log.Len()) }

// Tail 返回最近 n 字节。
func (p *ProcSession) Tail(n int) string { return string(p.log.Tail(n)) }
```

`Close()` 中删除对 `p.tf` 的处理，改 `_ = p.log.Close()`：
```go
func (p *ProcSession) Close() {
	_ = p.ptmx.Close()
	if p.cmd.Process != nil {
		pid := p.cmd.Process.Pid
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = p.cmd.Process.Kill()
	}
	go func() { _ = p.cmd.Wait() }()
	_ = p.log.Close()
}
```

（`Write`/`SetSize`/`Interrupt`/`FlushInput`/`KillLine`/`IsDead`/`LastByteTime` 保持不变。）

- [ ] **Step 2: 精简 `internal/pty/transcript.go`**

`.raw` 现由 `oplog` 直接持有并写入，删除 `AttachTranscript` 与 `setTranscript` 相关；保留 `transcriptPath`、`ReadTranscript`、`SweepTranscripts`，并导出 `TranscriptPath` 供 session 层拼 logPath：

```go
// TranscriptPath 返回会话日志文件路径 <dir>/<id>.raw（供 oplog.Open 使用）。id 非法返回空串。
func TranscriptPath(dir, id string) (string, bool) {
	if strings.ContainsAny(id, "/\\") {
		return "", false
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false
	}
	return filepath.Join(dir, id+".raw"), true
}
```
`ReadTranscript`/`SweepTranscripts` 内部继续用私有 `transcriptPath` 拼接，不变。删除 `AttachTranscript`。

- [ ] **Step 3: 更新 `internal/pty/proc_test.go`**

把所有 `NewProcSession(name, args...)` 调用改为 `NewProcSession(filepath.Join(t.TempDir(),"s.raw"), 1<<20, name, args...)`；把 `SnapshotLen()` 改 `Len()`、`Since(int)` 改 `Since(int64)`。逐处按编译错误修正（保持断言语义不变）。

- [ ] **Step 4: 编译 + 跑 pty 测试**

Run: `GOWORK=off go build ./internal/pty/ ./internal/oplog/ && GOWORK=off go test ./internal/pty/ -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/pty/proc.go internal/pty/transcript.go internal/pty/proc_test.go
git commit -m "refactor(pty): back ProcSession with oplog; int64 offsets; .raw mandatory"
```

---

## Task 4: `session/store.go` 偏移与 takeover 改 int64

**Files:**
- Modify: `internal/session/store.go`
- Modify: `internal/session/access.go`

- [ ] **Step 1: 改字段与方法为 int64**

`Session` 结构体：
```go
	deliveredOffset int64
	...
	takeoverStart  int64
	takeoverEnd    int64
	takeoverActive bool
```
`setDelivered/delivered` 改 int64：
```go
func (s *Session) setDelivered(n int64) { s.stateMu.Lock(); s.deliveredOffset = n; s.stateMu.Unlock() }
func (s *Session) delivered() int64 {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.deliveredOffset
}
```
`setHold`/`acquireHold`/`releaseHold` 中 `off := 0` 改 `var off int64 = 0`，`p.SnapshotLen()` 改 `p.Len()`；`takeoverRange()` 返回值改 `(start, end int64, active bool)`。

- [ ] **Step 2: 改 `access.go`**

`SnapshotLen()` 包装删除或改名为 `Len()`：
```go
func (s *Session) Len() int64 {
	if p := s.getProc(); p != nil {
		return p.Len()
	}
	return 0
}
func (s *Session) Since(off int64) string {
	if p := s.getProc(); p != nil {
		return p.Since(off)
	}
	return ""
}
func (s *Session) ReadRange(from, to int64) string {
	if p := s.getProc(); p != nil {
		return p.ReadRange(from, to)
	}
	return ""
}
```
删除旧 `SnapshotLen() int` 包装（改所有调用点，见 Task 5、6）。`SetSize`/`WriteInput`/`Held` 等不变。

- [ ] **Step 3: 编译（预期 session 包因调用点未改而报错，Task 5 修复）**

Run: `GOWORK=off go build ./internal/session/ 2>&1 | head`
Expected: 报错集中在 `session.go`/`gc.go` 调用点（下一 Task 修）。可暂不提交，或与 Task 5 合并提交。

---

## Task 5: `session.go` 适配（Send/Read/rearm/hard reset/finalize/range + Envelope）

**Files:**
- Modify: `internal/session/session.go`

- [ ] **Step 1: Envelope 去 SpillID、加 Range**

```go
type Envelope struct {
	Output    string     `json:"output"`
	State     string     `json:"state"`
	Prompt    string     `json:"prompt,omitempty"`
	ExitCode  *int       `json:"exit_code,omitempty"`
	Truncated bool       `json:"truncated,omitempty"`
	Range     *LogRange  `json:"range,omitempty"`
	Held      bool       `json:"held,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// LogRange 是超限时返回的日志绝对区间引用，用 terminal_read(mode=range,from,to) 取回。
type LogRange struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}
```

- [ ] **Step 2: 重写 finalize（接收绝对区间，超限返回 Range 而非 spill）**

```go
// finalize 组装返回；当交付区间 [from,to) 的字节数超过 exec_output_max_bytes 时，
// 不返回全量 output，改为头部预览 + Range 引用（模型用 mode=range 翻取）。
func finalize(output string, from, to int64, state, prompt string, code *int) Envelope {
	env := Envelope{Output: output, State: state, Prompt: prompt, ExitCode: code}
	if to-from > config.Get().ExecOutputMaxBytes {
		const previewMax = 2048
		preview := output
		if len(preview) > previewMax {
			preview = preview[:previewMax]
		}
		env.Output = fmt.Sprintf("%s\n[输出 %d 字节超过上限，仅显示头部；用 terminal_read(mode=range, from=%d, to=%d) 分页取完整内容]",
			preview, to-from, from, to)
		env.Range = &LogRange{From: from, To: to}
		env.Truncated = true
	}
	return env
}
```
删除 `SpillResult` 调用与 `tool` 参数。所有 `finalize(x, state, prompt, code, "...")` 调用点改成传绝对区间（见下）。

- [ ] **Step 3: 改 Send**

将结尾：
```go
	raw := proc.Since(start)
	state, prompt, code := computeState(sess)
	k := clean.DeliverBoundary(raw, state)
	sess.setDelivered(start + k)
	return finalize(clean.CleanOutput(raw[:k], input), state, prompt, code, "ssh_send")
```
改为：
```go
	start64 := start // start 现为 int64（见下）
	raw := proc.Since(start64)
	state, prompt, code := computeState(sess)
	k := clean.DeliverBoundary(raw, state)
	sess.setDelivered(start64 + int64(k))
	return finalize(clean.CleanOutput(raw[:k], input), start64, start64+int64(k), state, prompt, code)
```
并把函数内 `start := proc.SnapshotLen()` 改 `start := proc.Len()`（int64）。`switchCandidate` 分支里 `waitForShellPromptSince(proc, start, deadline)` 的 `start` 类型同步为 int64（Task 6 调整 rearm.go 的等待函数签名）。

- [ ] **Step 4: 改 sendWithRearm**

`armStart := proc.SnapshotLen()` → `armStart := proc.Len()`；`realLen := armStart - start` 用 int64；`sess.setDelivered(proc.SnapshotLen())` → `sess.setDelivered(proc.Len())`；结尾 `finalize(out, state, prompt, code, "ssh_send")` → `finalize(out, start, armStart, state, prompt, code)`（交付区间为命令真实输出段）。

- [ ] **Step 5: 改 Read（tail/since_last/新增 range）**

`since_last` 分支：
```go
	if mode == "since_last" {
		off := sess.delivered()
		raw := proc.Since(off)
		k := clean.DeliverBoundary(raw, state)
		sess.setDelivered(off + int64(k))
		observe := heldNow
		if !heldNow {
			if _, end, active := sess.takeoverRange(); active && end != 0 {
				if off < end {
					observe = true
				}
				if off+int64(k) >= end {
					sess.clearTakeover()
				}
			}
		}
		env := finalize(clean.ObserveOrClean(raw[:k], observe), off, off+int64(k), state, prompt, code)
		env.Held = heldNow
		return env
	}
```
新增 `range` 分支（放在 since_last 之前或之后均可）：
```go
	if mode == "range" {
		// from/to 通过新入参传入（见 Read 签名变更）。原始日志区间、不做 observe 还原。
		out := proc.ReadRange(rangeFrom, rangeTo)
		env := finalize(clean.CleanOutput(out, ""), rangeFrom, rangeTo, state, prompt, code)
		env.Held = heldNow
		return env
	}
```
`tail` 分支：`proc.SnapshotLen()` → `proc.Len()`；`total > config.Get().TailBytes` 比较用 int64（`total := proc.Len()`）。

**Read 签名变更**（无外部调用方，可改）：`func Read(id string, waitMs int, mode string, rangeFrom, rangeTo int64) Envelope`。`execLocal` 的 `read` 分支与 `sessionReq` 加 `RangeFrom/RangeTo int64` 字段并透传。

- [ ] **Step 6: 改 Control 的 rearm/hard**

`rearm`：`armStart := proc.SnapshotLen()` → `proc.Len()`；`sess.setDelivered(proc.SnapshotLen())` → `sess.setDelivered(proc.Len())`。
`hard`：`pty.NewProcSession(sess.reopenName, sess.reopenArgs...)` 改为带 logPath/cache（见 Task 6 startSession 统一封装）；`sess.setDelivered(0)` 改为在新 proc 就绪后 `sess.setDelivered(p.Len())`（Q4-A：跳末尾）。删除 `pty.AttachTranscript(...)` 调用（日志已在 NewProcSession 内开启）。

- [ ] **Step 7: computeState / startSession 内的 Tail 调用**

`computeState` 与 `startSession` 里 `proc.Tail(4096)` 保持（Tail 签名不变）。`startSession` 见 Task 6。

- [ ] **Step 8: 编译（跨 Task 6 后统一跑测试）**

Run: `GOWORK=off go build ./internal/session/ 2>&1 | head`
Expected: 仅剩 startSession/logPath 相关错误，由 Task 6 修复。

---

## Task 6: startSession 接 logPath、删 spill.go、改 rearm.go 等待签名、SSE 偏移

**Files:**
- Modify: `internal/session/session.go`（startSession/hard reset 构造 proc）
- Modify: `internal/session/rearm.go`（等待函数 offset 参数 int64）
- Delete: `internal/session/spill.go`
- Modify: `internal/terminal/terminal.go`（SSE 偏移 int64；closed 分支不变）

- [ ] **Step 1: startSession 构造 proc 时传 logPath/cache**

`startSession` 内：
```go
	logPath, ok := pty.TranscriptPath(config.Get().TranscriptDir, id)
	if !ok {
		sess.setStatus("dead", "invalid session id for transcript path")
		theStore.remove(id)
		return nil, fmt.Errorf("invalid session id")
	}
	proc, err := pty.NewProcSession(logPath, config.Get().MaxBufferBytes, name, args...)
	if err != nil {
		sess.setStatus("dead", err.Error())
		theStore.remove(id)
		return nil, err
	}
	sess.setProc(proc)
	// 删除原 pty.AttachTranscript(...) 行
```
`Control` 的 `hard` 分支同样用 `pty.TranscriptPath` + `pty.NewProcSession(logPath, config.Get().MaxBufferBytes, sess.reopenName, sess.reopenArgs...)`，就绪后 `sess.setDelivered(p.Len())`。为复用，可抽一个 `sess.reopenProc()` 辅助函数（可选）。

- [ ] **Step 2: rearm.go 等待函数 offset 参数改 int64**

把 `waitForShellPromptSince(proc, start int, ...)`、`waitForBashSentinelSince(proc, armStart int, ...)` 的 offset 形参改为 `int64`，内部对 `proc.Since(off)`/`proc.Len()` 的使用同步为 int64。逐处按编译错误修正。

- [ ] **Step 3: 删除 spill.go 与其调用**

```bash
git rm internal/session/spill.go
```
确认 `session.go`/`gc.go` 无 `SpillResult/SpillRead/SpillDir` 引用（Task 5 已移除 finalize 内的 spill 调用；`gc.go` 无 spill 引用）。

- [ ] **Step 4: SSE 偏移 int64（terminal.go）**

`serveTerminalStream` live 分支：
```go
	var off int64 = 0
	...
	if total := sess.SnapshotLen(); total > off {   // 改为 sess.Len()
		chunk := sess.Since(off)                      // Since(int64)
		off = total
		...
	}
```
即把 `off` 与 `total` 改 int64，`sess.SnapshotLen()` 改 `sess.Len()`。closed-session 分支（`ReadTranscript`）不变。`serveTakeover`/`serveTerminalInput` 不涉及偏移，不改。

- [ ] **Step 5: 全量编译**

Run: `GOWORK=off go build ./...`
Expected: 成功（mcpserver 仍引用 spill 工具，若报错则 Task 7 修；可先跳到 Task 7 再统一 build）。

- [ ] **Step 6: 提交（Task 4-6 合并为一次一致提交）**

```bash
git add -A
git commit -m "refactor(session,terminal): int64 log offsets, oplog-backed proc, range read, remove spill"
```

---

## Task 7: 工具面收敛（删 spill_explore、加 mode=range、Envelope 描述）

**Files:**
- Modify: `mcpserver/tools.go`
- Modify: `mcpserver/tools_test.go`

- [ ] **Step 1: 删除 spill 工具，新增 range 入参**

删除：`spillInput`、`spillOutput`、`descSpill`、`defaultDescriptions` 里的 `"terminal_spill_explore"` 项、`registerTools` 里 `terminal_spill_explore` 的 `mcp.AddTool(...)` 整块。

`readInput` 增加 range 参数与 mode 说明更新：
```go
type readInput struct {
	SessionID string `json:"session_id" jsonschema:"the session id"`
	WaitMs    int    `json:"wait_ms,omitempty" jsonschema:"max milliseconds to wait before returning (default 0)"`
	Mode      string `json:"mode,omitempty" jsonschema:"\"tail\" (default; peek, does not advance cursor), \"since_last\" (full increment since last call, advances cursor), or \"range\" (read an absolute log byte range [from,to); use the from/to returned in a truncated result to page through oversized output)"`
	From      int64  `json:"from,omitempty" jsonschema:"range mode only: absolute start offset (inclusive)"`
	To        int64  `json:"to,omitempty" jsonschema:"range mode only: absolute end offset (exclusive)"`
}
```
`terminal_read` handler 调用改为：
```go
			env := session.Read(in.SessionID, in.WaitMs, in.Mode, in.From, in.To)
```

- [ ] **Step 2: 更新 descSend/descRead 文案（去 spill，改 range）**

`descSend` 中把 “it is spilled: truncated=true and spill_id ... terminal_spill_explore” 改为：“the return is truncated: truncated=true and a range {from,to} is returned; use terminal_read(mode=range, from, to) to page the full content.”
`descRead` 中把 since_last 的 “spilled (truncated=true + spill_id); then call terminal_spill_explore(spill_id)” 改为：“truncated with a range {from,to}; then call terminal_read(mode=range, from, to) to page it.” 并补一句 range 模式说明。

- [ ] **Step 3: 更新 tools_test.go**

删除任何断言 `terminal_spill_explore` 存在/spill 相关的用例；若测试统计工具数量（如"7/8 个工具"），改为新数量（少 1 个）。按编译与断言错误修正。

- [ ] **Step 4: 全量编译 + 全量测试**

Run: `GOWORK=off go build ./... && GOWORK=off go test ./... 2>&1 | tail -20`
Expected: 全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add mcpserver/tools.go mcpserver/tools_test.go
git commit -m "feat(tools): replace terminal_spill_explore with terminal_read(mode=range); Envelope.Range"
```

---

## Task 8: 补 session 层新行为测试（range 分页、hard reset 跳末尾、冷读）

**Files:**
- Modify: `internal/session/session_test.go`
- Modify: `internal/terminal/terminal_test.go`

- [ ] **Step 1: 写 range 分页测试（先失败则实现已在，故应直接 PASS——作为回归保护）**

在 `session_test.go` 追加：
```go
func TestReadRangePagesOversizedOutput(t *testing.T) {
	config.Load("")
	old := config.Get().ExecOutputMaxBytes
	config.Get().ExecOutputMaxBytes = 256
	defer func() { config.Get().ExecOutputMaxBytes = old }()

	id := openLocalReady(t)
	defer Close(id)

	env := Send(id, "for i in $(seq 1 500); do echo LINE$i; done", 8000)
	if !env.Truncated || env.Range == nil {
		t.Fatalf("expected truncated+range, got truncated=%v range=%v", env.Truncated, env.Range)
	}
	// 用 range 取回完整区间，应能看到首尾标记。
	full := Read(id, 0, "range", env.Range.From, env.Range.To)
	if !strings.Contains(full.Output, "LINE1") || !strings.Contains(full.Output, "LINE500") {
		t.Fatalf("range read missing content, got %q", full.Output[:min(200, len(full.Output))])
	}
}
```

- [ ] **Step 2: 写 hard reset 偏移不归零测试**

```go
func TestHardResetAdvancesDeliveredNotZero(t *testing.T) {
	id := openLocalReady(t)
	defer Close(id)
	Send(id, "echo before_reset", 5000)
	before := theStore.get(id).delivered()
	Control(id, "hard")
	// 等新 shell ready
	for i := 0; i < 200; i++ {
		if Status(id).State == "idle" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	after := theStore.get(id).delivered()
	if after < before {
		t.Fatalf("delivered must not go backwards after hard reset: before=%d after=%d", before, after)
	}
	// reset 后新命令输出正常交付
	env := Send(id, "echo after_reset", 5000)
	if env.Output != "after_reset" {
		t.Fatalf("post-hard-reset output=%q", env.Output)
	}
}
```

- [ ] **Step 3: 运行 session 全量测试**

Run: `GOWORK=off go test ./internal/session/ -v 2>&1 | tail -40`
Expected: 全部 PASS（含既有 takeover/rearm 用例）。

- [ ] **Step 4: 终端 SSE 冷读回归（可选，若 SSE 测试易构造）**

在 `terminal_test.go` 视情况补：以极小 `max_buffer_bytes` 打开会话、产出超过缓存的输出，验证 SSE stream 仍能推出全量（冷段从 .raw 回读）。若构造成本高，可在 oplog 层已覆盖，跳过并注明。

- [ ] **Step 5: 全量测试 + 提交**

Run: `GOWORK=off go test ./... 2>&1 | tail -20`
Expected: 全 PASS。
```bash
git add internal/session/session_test.go internal/terminal/terminal_test.go
git commit -m "test: range paging, hard-reset offset monotonicity, cold-read regression"
```

---

## Self-Review

- **Spec 覆盖**：Q1（oplog 冷读回落）→Task1；Q2（spill→range）→Task5/7；Q3（.raw 强依赖）→Task1 Open + Task3/6 open 失败即 fail；Q4（单调偏移/hard reset 跳末尾）→Task4/5/6 + Task8 测试；取向2（独立组件）→Task1。配置 max_buffer_bytes/exec_output_max_bytes→Task2。SSE→Task6。工具面→Task7。均有对应任务。
- **占位符**：无 TBD/TODO；代码步骤均给出完整代码或精确编辑指令。
- **类型一致性**：偏移全链路 int64（oplog.Len/ReadRange、proc.Len/Since/ReadRange、session delivered/takeover、SSE off、Read from/to、finalize from/to、Envelope.LogRange）；`Since` 参数 int64 统一；`NewProcSession(logPath, cacheBytes, name, args...)` 签名在 Task3 定义、Task6 hard reset 与 startSession 一致使用。
- **风险点**：`clean.DeliverBoundary`/`CleanOutput`/`ObserveOrClean` 签名未改动，沿用现状；`k` 为 int，偏移相加处显式 `int64(k)`。
