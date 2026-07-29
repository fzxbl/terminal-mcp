package session

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/fzxbl/terminal-mcp/internal/clean"
	"github.com/fzxbl/terminal-mcp/internal/config"
	"github.com/fzxbl/terminal-mcp/internal/interp"
	"github.com/fzxbl/terminal-mcp/internal/outputref"
	"github.com/fzxbl/terminal-mcp/internal/pty"
	"github.com/fzxbl/terminal-mcp/internal/textexplore"
)

// Envelope 是所有会话工具的统一返回。
type Envelope struct {
	Output          string         `json:"output"`
	State           string         `json:"state"`
	Prompt          string         `json:"prompt,omitempty"`
	ExitCode        *int           `json:"exit_code,omitempty"`
	Truncated       bool           `json:"truncated,omitempty"`
	OutputRef       string         `json:"output_ref,omitempty"`
	OutputSizeBytes int64          `json:"output_size_bytes,omitempty"`
	Explore         *ExploreResult `json:"explore,omitempty"`
	Held            bool           `json:"held,omitempty"`
	Error           string         `json:"error,omitempty"`
}

// ExploreResult 是 mode=explore 的导航元数据；正文继续放在 Envelope.Output。
type ExploreResult struct {
	Op             string `json:"op"`
	SizeBytes      int64  `json:"size_bytes,omitempty"`
	LineCount      int    `json:"line_count,omitempty"`
	MaxLineBytes   int    `json:"max_line_bytes,omitempty"`
	NextLineOffset int    `json:"next_line_offset,omitempty"`
	ByteOffset     int    `json:"byte_offset,omitempty"`
	EOF            bool   `json:"eof,omitempty"`
}

// heldMsg 是人工接管期间拦截模型写操作的统一提示。
const heldMsg = "会话被人工接管中，暂不能 send/close/control。建议持续用 ssh_read（mode=since_last）观察：" +
	"接管期间 read 会把人输入的每条命令还原成 \"[rc=n] $ 命令\" 连同输出一并返回，可据此学习排查步骤。" +
	"你也可以先去做其他任务或等待，待 held 变为 false（人退出接管）后再继续操作。"

// selfAddr 返回本实例监听地址（取配置 listen_addr）。
func selfAddr() string { return config.Get().ListenAddr }

// sessionInitScript 返回会话初始化脚本：哨兵 PS1 + 可选资源限制（ulimit）+ 配置的 init_commands。
// 会话启动、每次切进新层 shell 的重新布哨（rearm）、hard reset 均复用它，保证：重开/切换 shell 后
// 别名等仍生效，且资源限制在每个新 shell 上下文里重新注入（对模型透明，随布哨噪声被 since_last 跳过）。
// 顺序：哨兵先行（供提示符检测）→ 资源限制次之（使随后的 init 命令也在限额内运行）→ init 命令。
func sessionInitScript() string {
	s := interp.SetBashSentinelCmd()
	if rl := strings.TrimSpace(config.Get().ResourceLimitCmd); rl != "" {
		s += "\n" + rl
	}
	for _, c := range config.Get().InitCommands {
		if strings.TrimSpace(c) != "" {
			s += "\n" + c
		}
	}
	return s
}

// startSession 起子进程并异步跑哨兵 PS1 初始化，就绪后置 ready。
func startSession(id, host, mode, name string, args []string) (*Session, error) {
	sess := &Session{ID: id, Host: host, Mode: mode, Status: "loading"}
	if !theStore.add(sess) {
		return nil, fmt.Errorf("已达并发上限 %d", config.Get().MaxSessions)
	}
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
	go func() {
		proc.Write(sessionInitScript() + "\n")
		to := time.Duration(config.Get().OpenReadyTimeoutMinutes) * time.Minute
		if to <= 0 {
			to = time.Minute
		}
		deadline := time.Now().Add(to)
		for time.Now().Before(deadline) {
			if _, _, _, ok := interp.DetectPromptAtTail(proc.Tail(4096)); ok {
				sess.setStatus("ready", "")
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		proc.Close()
		sess.setStatus("dead", "shell not ready: sentinel timeout")
		theStore.remove(id)
	}()
	return sess, nil
}

// computeState 依据存活/末尾提示符算出当前 state/prompt/exit_code。
func computeState(sess *Session) (state, prompt string, code *int) {
	st, _ := sess.snapshotStatus()
	if st == "loading" {
		return "loading", "", nil
	}
	if st == "dead" || st == "closed" {
		return "dead", "", nil
	}
	proc := sess.getProc()
	if proc == nil || proc.IsDead() {
		return "dead", "", nil
	}
	if _, pr, c, ok := interp.DetectPromptAtTail(proc.Tail(4096)); ok {
		return "idle", pr, c
	}
	// 未停在已知提示符：命令仍在进行（可能正在刷、也可能静默但没结束，如大 core 加载）
	return "running", "", nil
}

// capBlock 把毫秒等待封顶在 MaxBlockSeconds 内。
func capBlock(ms int, def int) time.Duration {
	if ms <= 0 {
		ms = def
	}
	max := config.Get().MaxBlockSeconds * 1000
	if max > 0 && ms > max {
		ms = max
	}
	return time.Duration(ms) * time.Millisecond
}

// waitSettled 轮询直到静默且末尾停在提示符，或超时。
func waitSettled(proc *pty.ProcSession, deadline time.Time) {
	quiet := time.Duration(config.Get().QuietWindowMs) * time.Millisecond
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		if time.Since(proc.LastByteTime()) >= quiet {
			if _, _, _, ok := interp.DetectPromptAtTail(proc.Tail(4096)); ok {
				return
			}
		}
	}
}

// Send 向会话输入 input，最多等 waitMs（封顶）。返回本次输入产生的新增输出 + 状态。
func Send(id, input string, waitMs int) Envelope {
	sess := theStore.get(id)
	if sess == nil {
		return Envelope{State: "dead", Error: "session not found: " + id}
	}
	if st, _ := sess.snapshotStatus(); st != "ready" {
		return Envelope{State: st, Error: "session not ready: " + st}
	}
	if sess.held() {
		state, prompt, code := computeState(sess)
		return Envelope{State: state, Prompt: prompt, ExitCode: code, Held: true, Error: heldMsg}
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.touch()
	proc := sess.getProc()
	if proc == nil {
		return Envelope{State: "dead", Error: "no live process"}
	}
	start := proc.Len()
	proc.Write(input + "\n")
	deadline := time.Now().Add(capBlock(waitMs, 30000))
	// 切换候选：命中切换注册表，或上一条切换命令停在鉴权问询、本条属于续作（pendingSwitch）。
	// 续作场景（如 ssh 弹 (yes/no)? 后输入 yes、或再输入 password）真正进 shell 发生在本条输入上，
	// 但本条命令本身不在注册表里，若不识别为切换候选就会用 waitSettled 干等不存在的哨兵直到超时、且不布哨。
	switchCandidate := config.Get().AutoRearm && (matchesShellSwitch(input) || sess.pendingSwitchFlag())
	if switchCandidate {
		// 切进新层 shell 后新 shell 无哨兵，waitSettled 永远等不到已知提示符而空耗到 deadline；
		// 改用 waitForShellPromptSince：轮询到"疑似 shell 提示符/已知提示符"再返回（ssh 分段吐输出、
		// 远端提示符晚到也能等到），不像 waitQuiet 一遇静默就退。非切换命令保持原 waitSettled 行为不变。
		waitForShellPromptSince(proc, start, deadline)
	} else {
		waitSettled(proc, deadline)
	}
	// 命令跑完后按尾部形态决定去向（仅切换候选走这套）：
	//   - 已到自家哨兵提示符：切换完成，清 pendingSwitch。
	//   - 停在"疑似 shell 提示符"（新 shell 无哨兵）：清 pendingSwitch 并重新布哨。
	//   - 停在交互鉴权问询（(yes/no)?/password:）：真正进 shell 在后续输入，置 pendingSwitch，本次如实返回
	//     让上层继续输入（如 yes / 密码），绝不把 PS1 误注入鉴权输入。
	if switchCandidate {
		tail := proc.Tail(4096)
		if _, _, _, ok := interp.DetectPromptAtTail(tail); ok {
			sess.setPendingSwitch(false)
		} else if looksLikeShellPrompt(tail) {
			sess.setPendingSwitch(false)
			return sendWithRearm(sess, proc, input, start)
		} else if looksLikeAuthPrompt(tail) {
			sess.setPendingSwitch(true)
		}
	}
	raw := proc.Since(start)
	state, prompt, code := computeState(sess)
	// 只交付到"转义边界"：running 时暂缓末尾被切断的半截转义，并按实际交付字节推进游标
	// （既杜绝半截颜色漏给 LLM，也顺带修掉 setDelivered(Len()) 与 since() 分锁的丢字节竞态）。
	k := clean.DeliverBoundary(raw, state)
	sess.setDelivered(start + int64(k))
	return finalizeScope(clean.CleanOutput(raw[:k], input), start, start+int64(k), state, prompt, code, input, false)
}

// sendWithRearm 在检测到切进新层 shell 后重新布哨（对模型透明，做进同一次 Send）。
// start 为本次命令写入前的缓冲偏移；调用前已确认 AutoRearm 且 matchesShellSwitch 命中且尾部非哨兵。
// 交付切一刀：[start,armStart) 为命令真实输出（清洗后交付）；[armStart,end) 为布哨噪声（PS1= 回显 +
// 新哨兵）一律丢弃，delivered 推进到末尾使噪声永不进 since_last。
func sendWithRearm(sess *Session, proc *pty.ProcSession, input string, start int64) Envelope {
	armStart := proc.Len()
	proc.Write(sessionInitScript() + "\n")
	armed := waitForBashSentinelSince(proc, armStart, time.Now().Add(rearmTimeout()))
	// 命令真实输出段 [start, armStart)：布哨噪声在其之后，切出来单独清洗交付。
	raw := proc.Since(start)
	realLen := armStart - start
	if realLen < 0 {
		realLen = 0
	}
	if realLen > int64(len(raw)) {
		realLen = int64(len(raw))
	}
	real := raw[:int(realLen)]
	state, prompt, code := computeState(sess)
	k := clean.DeliverBoundary(real, state)
	out := clean.CleanOutput(real[:k], input)
	// 布哨噪声一律丢弃：delivered 推进到当前末尾，噪声永不进 since_last。
	sess.setDelivered(proc.Len())
	if !armed {
		// 目标非 bash（sh/zsh 等）：一次性降级提示，绝不反复注入 PS1=。
		note := "[进入非 bash shell，命令边界检测降级；可 terminal_control(rearm) 或退出该 shell]"
		if out != "" {
			out += "\n" + note
		} else {
			out = note
		}
	}
	return finalizeScope(out, start, armStart, state, prompt, code, input, false)
}

// ReadArgs 承载 explore 的全部可选参数，避免 Read 参数爆炸。
type ReadArgs struct {
	Mode       string
	WaitMs     int
	OutputRef  string
	Op         string
	LineOffset int
	Limit      int
	Pattern    string
	Before     int
	After      int
	MaxBytes   int
}

// Read 观察输出。mode=tail：立即取尾部窗口；mode=since_last：取交付游标之后；
// mode=explore：按 output_ref 在固定区间内做 stat/read/grep 探索（不推进 since_last 游标）。
func Read(id string, a ReadArgs) Envelope {
	sess := theStore.get(id)
	if sess == nil {
		return Envelope{State: "dead", Error: "session not found: " + id}
	}
	proc := sess.getProc()
	if proc == nil {
		return Envelope{State: "dead", Error: "no live process"}
	}
	sess.touch()
	if a.WaitMs > 0 && a.Mode != "explore" {
		waitSettled(proc, time.Now().Add(capBlock(a.WaitMs, 500)))
	}
	if a.Mode == "explore" {
		return doExplore(proc, a)
	}
	state, prompt, code := computeState(sess)
	heldNow := sess.held()
	if a.Mode == "since_last" {
		off := sess.delivered()
		raw := proc.Since(off)
		// 只按本次实际交付的字节数推进游标，绝不推到 Len()：
		// since() 与取长度分两次加锁，其间后台 reader 可能又 append，
		// 若推到当前末尾会跳过这段未交付字节，导致下次 since_last 丢内容。
		// 同时 running 时暂缓末尾被切断的半截转义，避免半截颜色以文本漏给 LLM（下次补齐再交付）。
		k := clean.DeliverBoundary(raw, state)
		sess.setDelivered(off + int64(k))
		// observe 判定按"字节是否属于人工接管区间"，而非读取瞬间是否 held：
		// 正常交接顺序是人先交回控制权（held 置 false）模型才来读，若只看 held 就会把接管内容
		// 丢进常规 CleanOutput（哨兵行连同人敲的命令一起被剥掉），导致交回后读不到人做了什么。
		observe := heldNow
		if !heldNow {
			if _, end, active := sess.takeoverRange(); active && end != 0 {
				if off < end { // 本次交付仍落在接管区间内 → 还原人敲的命令
					observe = true
				}
				if off+int64(k) >= end { // 接管内容已全部交付，恢复常规清洗
					sess.clearTakeover()
				}
			}
		}
		env := finalizeScope(clean.ObserveOrClean(raw[:k], observe), off, off+int64(k), state, prompt, code, "", observe)
		env.Held = heldNow
		return env
	}
	// tail（默认）：只取尾部窗口用于判断是否结束，不推进 since_last 游标，可反复调用。
	total := proc.Len()
	full := proc.Tail(config.Get().TailBytes)
	if state == "running" {
		full = full[:clean.PendingEscBoundary(full)] // 剪掉窗口末尾被切断的半截转义
	}
	// 接管中、或存在尚未交付完的接管内容时，tail 也用 observe 还原人敲的命令（与 since_last 一致）。
	observeTail := heldNow
	if !observeTail {
		if _, _, active := sess.takeoverRange(); active {
			observeTail = true
		}
	}
	env := finalize(clean.ObserveOrClean(full, observeTail), 0, 0, state, prompt, code)
	env.Held = heldNow
	if total > int64(config.Get().TailBytes) {
		env.Truncated = true
		// tail 不做 spill：明确告诉调用方，要拿完整输出必须改用 since_last。
		env.Output += fmt.Sprintf(
			"\n[tail 仅显示尾部约 %d 字节，前面还有约 %d 字节未展示；要拿完整输出请改用 mode=since_last]",
			config.Get().TailBytes, total-int64(config.Get().TailBytes))
	}
	return env
}

// normExplore 把 explore 参数收敛到配置硬上限内（<=0 视为取默认硬上限）。
func normExplore(a *ReadArgs) {
	c := config.Get()
	if a.MaxBytes <= 0 || int64(a.MaxBytes) > c.ExploreMaxBytesHard {
		a.MaxBytes = int(c.ExploreMaxBytesHard)
	}
	switch a.Op {
	case "read":
		if a.Limit <= 0 || a.Limit > c.ExploreReadLimitHard {
			a.Limit = c.ExploreReadLimitHard
		}
	case "grep":
		if a.Limit <= 0 || a.Limit > c.ExploreGrepLimitHard {
			a.Limit = c.ExploreGrepLimitHard
		}
		if a.Before < 0 {
			a.Before = 0
		} else if a.Before > c.ExploreCtxHard {
			a.Before = c.ExploreCtxHard
		}
		if a.After < 0 {
			a.After = 0
		} else if a.After > c.ExploreCtxHard {
			a.After = c.ExploreCtxHard
		}
	}
}

// doExplore 在 output_ref 指向的固定区间内做 stat/read/grep；只读，不推进 since_last 游标。
func doExplore(proc *pty.ProcSession, a ReadArgs) Envelope {
	sc, err := outputref.Parse(a.OutputRef)
	if err != nil {
		return Envelope{State: "idle", Error: "invalid output_ref: " + err.Error()}
	}
	if sc.From < 0 || sc.To > proc.Len() || sc.From > sc.To {
		return Envelope{State: "idle", Error: "output_ref range out of bounds"}
	}
	normExplore(&a)
	src := func() io.Reader { return proc.RangeReader(sc.From, sc.To) }
	opt := textexplore.Options{Observe: sc.Observe, Input: sc.Input}
	switch a.Op {
	case "stat":
		r, err := textexplore.Stat(src, opt)
		if err != nil {
			return Envelope{State: "idle", Error: err.Error()}
		}
		r.SizeBytes = sc.To - sc.From
		return Envelope{State: "idle", Explore: toExplore(r)}
	case "read":
		body, r, err := textexplore.Read(src, opt, a.LineOffset, 0, a.Limit, a.MaxBytes)
		if err != nil {
			return Envelope{State: "idle", Error: err.Error()}
		}
		return Envelope{State: "idle", Output: body, Explore: toExplore(r)}
	case "grep":
		if strings.TrimSpace(a.Pattern) == "" {
			return Envelope{State: "idle", Error: "grep requires pattern"}
		}
		body, r, err := textexplore.Grep(src, opt, a.Pattern, a.LineOffset, a.Before, a.After, a.Limit, a.MaxBytes)
		if err != nil {
			return Envelope{State: "idle", Error: err.Error()}
		}
		return Envelope{State: "idle", Output: body, Explore: toExplore(r)}
	default:
		return Envelope{State: "idle", Error: "unknown explore op: " + a.Op}
	}
}

func toExplore(r textexplore.Result) *ExploreResult {
	return &ExploreResult{
		Op: r.Op, SizeBytes: r.SizeBytes, LineCount: r.LineCount,
		MaxLineBytes: r.MaxLineBytes, NextLineOffset: r.NextLineOffset,
		ByteOffset: r.ByteOffset, EOF: r.EOF,
	}
}

// controlKeys 把可读的控制键名映射到写入 PTY 的控制字节（多为 C0 控制码）。
var controlKeys = map[string]byte{
	"ctrl-c":    0x03, // ETX  SIGINT：打断当前运行/卡住的命令
	"ctrl-d":    0x04, // EOT  EOF：结束输入 / 退出交互式解释器或 shell
	"ctrl-z":    0x1a, // SUB  SIGTSTP：挂起当前进程到后台
	"ctrl-\\":   0x1c, // FS   SIGQUIT：退出并尝试生成 core
	"ctrl-l":    0x0c, // FF   清屏 / 重绘
	"ctrl-u":    0x15, // NAK  清除当前行光标前的输入
	"ctrl-k":    0x0b, // VT   清除当前行光标后的输入
	"ctrl-a":    0x01, // SOH  光标移到行首
	"ctrl-e":    0x05, // ENQ  光标移到行尾
	"ctrl-w":    0x17, // ETB  删除光标前一个单词
	"ctrl-r":    0x12, // DC2  反向历史搜索
	"ctrl-g":    0x07, // BEL  响铃 / 取消当前编辑或搜索
	"tab":       0x09, // HT   补全 / 制表
	"esc":       0x1b, // ESC  Escape
	"enter":     0x0d, // CR   回车
	"backspace": 0x7f, // DEL  退格
}

// Control 向会话发送控制键，或执行恢复动作。
//
//	控制键：ctrl-c/ctrl-d/ctrl-z/ctrl-\/ctrl-l/ctrl-u/ctrl-k/ctrl-a/ctrl-e/
//	        ctrl-w/ctrl-r/ctrl-g/tab/esc/enter/backspace —— 直接把对应控制字节写入 PTY。
//	恢复动作：flush（清输入队列 + 清当前行 + 回车）、hard（重开 shell）、
//	        rearm（切进新一层 shell 后重新布哨：写哨兵 PS1、等自家哨兵、把 delivered 推到末尾，噪声不外露）。
//
// 人工接管期间被拦截。
func Control(id, key string) Envelope {
	sess := theStore.get(id)
	if sess == nil {
		return Envelope{State: "dead", Error: "session not found: " + id}
	}
	if sess.held() {
		state, prompt, code := computeState(sess)
		return Envelope{State: state, Prompt: prompt, ExitCode: code, Held: true, Error: heldMsg}
	}
	sess.touch()
	switch key {
	case "flush":
		proc := sess.getProc()
		if proc == nil {
			return Envelope{State: "dead", Error: "no live process"}
		}
		_ = proc.FlushInput()
		proc.Interrupt()
		proc.KillLine()
		proc.Write("\n")
	case "rearm":
		// 确定性手动布哨：一切自动布哨覆盖不到场景的兜底口（未注册命令、alias、脚本 exec、接管切 shell）。
		proc := sess.getProc()
		if proc == nil {
			return Envelope{State: "dead", Error: "no live process"}
		}
		armStart := proc.Len()
		proc.Write(sessionInitScript() + "\n")
		waitForBashSentinelSince(proc, armStart, time.Now().Add(rearmTimeout()))
		// 把 delivered 推到当前末尾：PS1= 回显 + 新哨兵等布哨噪声不再由 since_last 外露。
		sess.setDelivered(proc.Len())
		state, prompt, code := computeState(sess)
		return finalize("", 0, 0, state, prompt, code)
	case "hard":
		old := sess.getProc()
		if old != nil {
			old.Close()
		}
		logPath, ok := pty.TranscriptPath(config.Get().TranscriptDir, id)
		if !ok {
			sess.setStatus("dead", "invalid session id for transcript path")
			return Envelope{State: "dead", Error: "invalid session id"}
		}
		p, err := pty.NewProcSession(logPath, config.Get().MaxBufferBytes, sess.reopenName, sess.reopenArgs...)
		if err != nil {
			sess.setStatus("dead", err.Error())
			return Envelope{State: "dead", Error: err.Error()}
		}
		sess.setProc(p)
		sess.setDelivered(p.Len())
		p.Write(sessionInitScript() + "\n")
	default:
		b, ok := controlKeys[key]
		if !ok {
			return Envelope{State: "idle", Error: "unknown control key: " + key}
		}
		proc := sess.getProc()
		if proc == nil {
			return Envelope{State: "dead", Error: "no live process"}
		}
		proc.Write(string([]byte{b}))
	}
	time.Sleep(time.Duration(config.Get().QuietWindowMs) * time.Millisecond)
	state, prompt, code := computeState(sess)
	return finalize("", 0, 0, state, prompt, code)
}

// Status 轻量返回状态（output 空）。
func Status(id string) Envelope {
	sess := theStore.get(id)
	if sess == nil {
		return Envelope{State: "dead", Error: "session not found: " + id}
	}
	state, prompt, code := computeState(sess)
	return Envelope{State: state, Prompt: prompt, ExitCode: code, Held: sess.held()}
}

// Close 关会话。
func Close(id string) Envelope {
	sess := theStore.get(id)
	if sess == nil {
		return Envelope{State: "dead"}
	}
	if sess.held() {
		state, prompt, code := computeState(sess)
		return Envelope{State: state, Prompt: prompt, ExitCode: code, Held: true, Error: heldMsg}
	}
	if proc := sess.getProc(); proc != nil {
		proc.Close()
	}
	sess.setStatus("closed", "")
	theStore.remove(id)
	return Envelope{State: "dead"}
}

// finalize 组装返回；交付区间未超上限时等价于普通 Envelope（from==to==0 的场景永不超限）。
func finalize(output string, from, to int64, state, prompt string, code *int) Envelope {
	return finalizeScope(output, from, to, state, prompt, code, "", false)
}

// finalizeScope 组装返回；交付区间超 exec_output_max_bytes 时只回预览 + output_ref（携带清洗语义供 explore 复原）。
func finalizeScope(output string, from, to int64, state, prompt string, code *int, input string, observe bool) Envelope {
	env := Envelope{Output: output, State: state, Prompt: prompt, ExitCode: code}
	if to-from > config.Get().ExecOutputMaxBytes {
		const previewMax = 2048
		preview := output
		if len(preview) > previewMax {
			preview = preview[:previewMax]
		}
		env.Output = fmt.Sprintf("%s\n[输出 %d 字节超过上限，仅显示头部；用 terminal_read(mode=explore, output_ref=..., op=stat|grep|read) 探索完整内容]", preview, to-from)
		env.OutputRef = outputref.Sign(outputref.Scope{From: from, To: to, Input: input, Observe: observe})
		env.OutputSizeBytes = to - from
		env.Truncated = true
	}
	return env
}

// OpenLocalForTest 本地 bash 会话（测试用）。
func OpenLocalForTest() (string, error) {
	config.Load("")
	if theStore == nil {
		InitStore(config.Get().MaxSessions)
	}
	id := newSessionID()
	name, args := BuildStartArgs("local", "")
	_, err := startSessionTracked(id, "local", "local", name, args)
	return id, err
}

// startSessionTracked 记录 reopen 参数供 hard reset 用，再调 startSession。
func startSessionTracked(id, host, mode, name string, args []string) (*Session, error) {
	sess, err := startSession(id, host, mode, name, args)
	if err == nil {
		sess.reopenName, sess.reopenArgs = name, args
	}
	return sess, err
}

// sessionReq 是会话操作的统一入参。
type sessionReq struct {
	Op         string `json:"op"`
	SessionID  string `json:"session_id"`
	Input      string `json:"input"`
	WaitMs     int    `json:"wait_ms"`
	Mode       string `json:"mode"`
	Key        string `json:"key"`
	OutputRef  string `json:"output_ref"`
	ExploreOp  string `json:"explore_op"`
	LineOffset int    `json:"line_offset"`
	Limit      int    `json:"limit"`
	Pattern    string `json:"pattern"`
	Before     int    `json:"before"`
	After      int    `json:"after"`
	MaxBytes   int    `json:"max_bytes"`
}

// dispatch 执行会话操作（单节点，本地执行）。
func dispatch(r sessionReq) Envelope { return execLocal(r) }

func execLocal(r sessionReq) Envelope {
	switch r.Op {
	case "send":
		return Send(r.SessionID, r.Input, r.WaitMs)
	case "read":
		return Read(r.SessionID, ReadArgs{
			Mode: r.Mode, WaitMs: r.WaitMs, OutputRef: r.OutputRef, Op: r.ExploreOp,
			LineOffset: r.LineOffset, Limit: r.Limit, Pattern: r.Pattern,
			Before: r.Before, After: r.After, MaxBytes: r.MaxBytes,
		})
	case "control":
		return Control(r.SessionID, r.Key)
	case "status":
		return Status(r.SessionID)
	case "close":
		return Close(r.SessionID)
	default:
		return Envelope{State: "dead", Error: "unknown op: " + r.Op}
	}
}
