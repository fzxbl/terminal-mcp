package session

import (
	"strings"
	"testing"
	"time"

	"github.com/fzxbl/terminal-mcp/internal/config"
	"github.com/fzxbl/terminal-mcp/internal/interp"
)

func openLocalReady(t *testing.T) string {
	t.Helper()
	id, err := OpenLocalForTest()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		if env := Status(id); env.State == "idle" {
			return id
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("session not idle")
	return ""
}

func TestSendReturnsCleanOutputAndExitCode(t *testing.T) {
	id := openLocalReady(t)
	defer Close(id)
	env := Send(id, "echo hello; echo world", 5000)
	if env.Output != "hello\nworld" {
		t.Fatalf("output=%q", env.Output)
	}
	if env.State != "idle" || env.ExitCode == nil || *env.ExitCode != 0 {
		t.Fatalf("state=%q code=%v", env.State, env.ExitCode)
	}
}

func TestSendNonZeroExitCode(t *testing.T) {
	id := openLocalReady(t)
	defer Close(id)
	env := Send(id, "false", 5000)
	if env.ExitCode == nil || *env.ExitCode != 1 {
		t.Fatalf("expected exit code 1, got %v (state=%q)", env.ExitCode, env.State)
	}
	env2 := Send(id, "true", 5000)
	if env2.ExitCode == nil || *env2.ExitCode != 0 {
		t.Fatalf("expected exit code 0 after true, got %v", env2.ExitCode)
	}
}

func TestReadTailSeesBufferedOutput(t *testing.T) {
	id := openLocalReady(t)
	defer Close(id)
	Send(id, "echo buffered", 5000)
	env := Read(id, 0, "tail", 0, 0)
	if !strings.Contains(env.Output, "buffered") {
		t.Fatalf("tail missed buffered output: %q", env.Output)
	}
}

func TestReadSinceLastDrains(t *testing.T) {
	id := openLocalReady(t)
	defer Close(id)
	Send(id, "echo first", 5000) // Send advances delivered cursor
	Send(id, "echo second", 5000)
	// since_last should return only new output since last delivery, then be empty on repeat
	env := Read(id, 0, "since_last", 0, 0)
	_ = env
	env2 := Read(id, 0, "since_last", 0, 0)
	if strings.TrimSpace(env2.Output) != "" {
		t.Fatalf("since_last should be empty on immediate repeat, got %q", env2.Output)
	}
}

func TestControlFlushClearsUnterminatedQuote(t *testing.T) {
	id := openLocalReady(t)
	defer Close(id)
	proc := theStore.get(id).proc
	proc.Write("echo \"oops")
	time.Sleep(100 * time.Millisecond)
	Control(id, "flush")
	env := Send(id, "echo recovered", 5000)
	if env.Output != "recovered" {
		t.Fatalf("not recovered: %q", env.Output)
	}
}

// TestControlCtrlUKillsPendingLine 验证控制字节送达 PTY 行编辑：
// 先输入一段未回车的命令，再用 ctrl-u 清掉当前行，随后正常命令不应被污染。
func TestControlCtrlUKillsPendingLine(t *testing.T) {
	id := openLocalReady(t)
	defer Close(id)
	proc := theStore.get(id).proc
	proc.Write("echo SHOULD_NOT_APPEAR")
	time.Sleep(100 * time.Millisecond)
	Control(id, "ctrl-u")
	env := Send(id, "echo clean", 5000)
	if env.Output != "clean" {
		t.Fatalf("ctrl-u did not clear pending line, output=%q", env.Output)
	}
}

// TestControlCtrlCInterruptsRunningCommand 验证 ctrl-c 作为信号打断前台命令。
// 子进程建有受控终端（Setsid+Setctty），本地模式亦可投递 SIGINT。
func TestControlCtrlCInterruptsRunningCommand(t *testing.T) {
	id := openLocalReady(t)
	defer Close(id)
	proc := theStore.get(id).proc
	proc.Write("sleep 30\n")
	time.Sleep(300 * time.Millisecond)
	if env := Status(id); env.State != "running" {
		t.Fatalf("expected running while sleeping, got %q", env.State)
	}
	Control(id, "ctrl-c")
	for i := 0; i < 100; i++ {
		if env := Status(id); env.State == "idle" {
			env2 := Send(id, "echo back", 5000)
			if env2.Output != "back" {
				t.Fatalf("shell not responsive after ctrl-c: %q", env2.Output)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("ctrl-c did not return session to idle")
}

func TestControlUnknownKeyErrors(t *testing.T) {
	id := openLocalReady(t)
	defer Close(id)
	env := Control(id, "ctrl-nope")
	if env.Error == "" {
		t.Fatal("expected error for unknown control key")
	}
}

// TestAutoRearmAfterShellSwitch 验证命中注册表的切换命令跑完后自动重新布哨：
// 本地用 `bash --norc --noprofile` 模拟无哨兵的新层 shell（把 "bash" 临时加进注册表）。
// 期望：布哨成功→state=idle，且交付输出不含布哨噪声（PS1= / 裸哨兵）。
func TestAutoRearmAfterShellSwitch(t *testing.T) {
	id := openLocalReady(t)
	defer Close(id)
	orig := config.Get().ShellSwitchCommands
	config.Get().ShellSwitchCommands = append([]string{"bash"}, orig...)
	defer func() { config.Get().ShellSwitchCommands = orig }()

	env := Send(id, "bash --norc --noprofile", 5000)
	if env.State != "idle" {
		t.Fatalf("expected idle after re-arm, got state=%q output=%q", env.State, env.Output)
	}
	if strings.Contains(env.Output, "PS1=") || strings.Contains(env.Output, "@@PTYSESS@@") {
		t.Fatalf("re-arm noise leaked into output: %q", env.Output)
	}
	// 新 shell 内命令边界应已恢复。
	env2 := Send(id, "echo inner", 5000)
	if env2.Output != "inner" {
		t.Fatalf("inner echo output=%q (state=%q)", env2.Output, env2.State)
	}
	// 手动 rearm 亦应可用。
	if env3 := Control(id, "rearm"); env3.State != "idle" {
		t.Fatalf("manual rearm state=%q", env3.State)
	}
	// 退出嵌套 shell，回到原 shell。
	Send(id, "exit", 5000)
}

// TestGrepSshNotRearmed 误命中防护：命令文本含 ssh 但首 token 非切换命令，
// 哨兵始终在→不应触发布哨，输出干净、state=idle。
func TestGrepSshNotRearmed(t *testing.T) {
	id := openLocalReady(t)
	defer Close(id)
	env := Send(id, "echo ssh-in-text", 5000)
	if env.Output != "ssh-in-text" {
		t.Fatalf("output=%q", env.Output)
	}
	if strings.Contains(env.Output, "PS1=") || strings.Contains(env.Output, "@@PTYSESS@@") {
		t.Fatalf("unexpected re-arm noise: %q", env.Output)
	}
	if env.State != "idle" {
		t.Fatalf("state=%q", env.State)
	}
}

// TestManualRearm 直接写入切换命令（绕过 Send 的自动布哨），再用 terminal_control(rearm)
// 手动恢复哨兵；随后普通 Send 应正常工作、state=idle。
func TestManualRearm(t *testing.T) {
	id := openLocalReady(t)
	defer Close(id)
	proc := theStore.get(id).proc
	proc.Write("bash --norc --noprofile\n")
	time.Sleep(500 * time.Millisecond)
	if env := Control(id, "rearm"); env.State != "idle" {
		t.Fatalf("manual rearm state=%q", env.State)
	}
	env := Send(id, "echo manual", 5000)
	if env.Output != "manual" {
		t.Fatalf("output after manual rearm=%q (state=%q)", env.Output, env.State)
	}
	Send(id, "exit", 5000)
}

// TestAuthPromptSetsPendingSwitch（问题1）：切换命令停在交互鉴权问询（(yes/no)?）时，
// 应置 pendingSwitch 并返回 running（不误把 PS1 打进鉴权输入）；回答后回到带哨兵的当前 shell
// 应清除 pendingSwitch 并回到 idle。这里用 read -p 模拟 ssh 的 host-key 问询。
func TestAuthPromptSetsPendingSwitch(t *testing.T) {
	id := openLocalReady(t)
	defer Close(id)
	sess := theStore.get(id)
	orig := config.Get().ShellSwitchCommands
	config.Get().ShellSwitchCommands = append([]string{"read"}, orig...)
	defer func() { config.Get().ShellSwitchCommands = orig }()

	env := Send(id, `read -p "continue (yes/no)? " ans`, 2000)
	if env.State != "running" {
		t.Fatalf("expected running at auth prompt, got %q (%q)", env.State, env.Output)
	}
	if !sess.pendingSwitchFlag() {
		t.Fatal("expected pendingSwitch set after stopping at auth prompt")
	}
	env2 := Send(id, "yes", 3000)
	if env2.State != "idle" {
		t.Fatalf("expected idle after answering, got %q (%q)", env2.State, env2.Output)
	}
	if sess.pendingSwitchFlag() {
		t.Fatal("pendingSwitch should be cleared once back at sentinel prompt")
	}
}

// TestPendingSwitchTreatsNextCmdAsSwitch（问题1核心）：pendingSwitch 置位时，下一条本不在
// 切换注册表里的命令也应按 shell 切换续作处理——进入无哨兵的新层 shell 时自动布哨回到 idle，
// 而非用 waitSettled 干等不存在的哨兵直到超时。模拟 ssh 回答 yes 后落到远端 shell 那一刻。
func TestPendingSwitchTreatsNextCmdAsSwitch(t *testing.T) {
	id := openLocalReady(t)
	defer Close(id)
	sess := theStore.get(id)
	// 注册表不含 bash：正常 `bash --norc` 不会被当切换命令；靠 pendingSwitch 触发。
	sess.setPendingSwitch(true)
	env := Send(id, "bash --norc --noprofile", 5000)
	if env.State != "idle" {
		t.Fatalf("pending switch should rearm sentinel-less shell to idle, got %q (%q)", env.State, env.Output)
	}
	if sess.pendingSwitchFlag() {
		t.Fatal("pendingSwitch should be cleared after switch completed")
	}
	if strings.Contains(env.Output, "PS1=") || strings.Contains(env.Output, "@@PTYSESS@@") {
		t.Fatalf("re-arm noise leaked: %q", env.Output)
	}
	env2 := Send(id, "echo inner", 5000)
	if env2.Output != "inner" {
		t.Fatalf("inner echo=%q (state=%q)", env2.Output, env2.State)
	}
	Send(id, "exit", 5000)
}

// TestTakeoverContentVisibleAfterRelease（问题2）：人工接管产生的命令，在人交回控制权
// （held 已置 false）之后，模型用 tail / since_last 仍应能读到，并还原成 "[rc=n] $ 命令"。
// 旧实现按读取瞬间的 held 决定清洗方式，交回后走常规 CleanOutput 会把命令连同哨兵行一起剥掉。
func TestTakeoverContentVisibleAfterRelease(t *testing.T) {
	id := openLocalReady(t)
	defer Close(id)
	sess := theStore.get(id)

	// 模拟人工接管：置 hold，直接把人敲的命令写进 PTY（绕过模型 Send）。
	sess.SetHold(true)
	sess.WriteInput("echo human_typed\n")
	// 等人敲的命令真正被回显+执行（缓冲里出现输出且回到哨兵提示符）后再交回控制权，
	// 否则可能在 shell 处理该行之前就误判为 idle。
	proc := sess.getProc()
	for i := 0; i < 200; i++ {
		if _, _, _, ok := interp.DetectPromptAtTail(proc.Tail(4096)); ok &&
			strings.Contains(proc.Tail(1<<16), "human_typed") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	// 人交回控制权：此后模型才来读，读取瞬间 held 已为 false。
	sess.SetHold(false)

	tail := Read(id, 0, "tail", 0, 0)
	if !strings.Contains(tail.Output, "echo human_typed") {
		t.Fatalf("tail after release missing human command: %q", tail.Output)
	}
	env := Read(id, 0, "since_last", 0, 0)
	if !strings.Contains(env.Output, "echo human_typed") {
		t.Fatalf("since_last after release missing human command: %q", env.Output)
	}
	if !strings.Contains(env.Output, "[rc=") {
		t.Fatalf("since_last should render human command as [rc=n] $ cmd: %q", env.Output)
	}
	// 接管内容交付完后应恢复常规清洗：后续模型命令输出不再被 observe 影响。
	env2 := Send(id, "echo after", 5000)
	if env2.Output != "after" {
		t.Fatalf("post-takeover normal send output=%q (state=%q)", env2.Output, env2.State)
	}
}

// TestResourceLimitInjectedAndInheritedUnescapable 校验 resource_limit_cmd 注入的 ulimit：
// 启动即作用于会话根 shell（软硬限都被设定），并被子进程继承（含硬限）。硬限被继承是「非特权
// 子进程无法调高、无法逃逸」的基础；能否真正调高取决于是否具备 CAP_SYS_RESOURCE（root 可调高，
// 属 ulimit 固有边界，非本功能可控），故此处只断言注入与继承，不断言「调不高」。
// 用 RLIMIT_NOFILE（-n）验证，避免像 -v 那样过低导致 bash 起不来。
func TestResourceLimitInjectedAndInheritedUnescapable(t *testing.T) {
	config.Load("")
	config.Get().ResourceLimitCmd = "ulimit -n 137"
	defer func() { config.Get().ResourceLimitCmd = "" }()

	id := openLocalReady(t)
	defer Close(id)

	// 1) 会话根 shell 的软硬限都被设为 137
	if env := Send(id, "echo s=$(ulimit -Sn) h=$(ulimit -Hn)", 5000); !strings.Contains(env.Output, "s=137 h=137") {
		t.Fatalf("root shell should have soft&hard nofile=137, got %q", env.Output)
	}
	// 2) 子进程继承软硬限（硬限继承 = 非特权子进程无法逃逸出该上限）
	if env := Send(id, "bash -c 'echo s=$(ulimit -Sn) h=$(ulimit -Hn)'", 5000); !strings.Contains(env.Output, "s=137 h=137") {
		t.Fatalf("child should inherit soft&hard nofile=137, got %q", env.Output)
	}
}

// TestReadRangePagesOversizedOutput 校验：单次输出超过 exec_output_max_bytes 时返回 Range 引用，
// 且用 mode=range 逐页能把整段区间完整取回（分页器不再自我引用同一区间）。
func TestReadRangePagesOversizedOutput(t *testing.T) {
	config.Load("")
	old := config.Get().ExecOutputMaxBytes
	config.Get().ExecOutputMaxBytes = 256
	defer func() { config.Get().ExecOutputMaxBytes = old }()

	id := openLocalReady(t)
	defer Close(id)

	env := Send(id, "for i in $(seq 1 500); do echo LINE$i; done", 8000)
	if !env.Truncated || env.Range == nil {
		t.Fatalf("expected truncated+range, got truncated=%v range=%v output=%q", env.Truncated, env.Range, env.Output)
	}

	var acc strings.Builder
	from, to := env.Range.From, env.Range.To
	for i := 0; i < 2000; i++ {
		p := Read(id, 0, "range", from, to)
		acc.WriteString(p.Output)
		if p.Range == nil {
			break
		}
		from, to = p.Range.From, p.Range.To
	}
	out := acc.String()
	if !strings.Contains(out, "LINE1") || !strings.Contains(out, "LINE500") {
		t.Fatalf("paged range missing content (len=%d)", len(out))
	}
}

// TestHardResetAdvancesDeliveredNotZero 校验 Q4-A：hard reset 后 delivered 不回退到 0，
// 而是跳到当前日志末尾（从新 shell 看起），且后续命令输出正常交付。
func TestHardResetAdvancesDeliveredNotZero(t *testing.T) {
	id := openLocalReady(t)
	defer Close(id)
	Send(id, "echo before_reset", 5000)
	before := theStore.get(id).delivered()
	Control(id, "hard")
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
	env := Send(id, "echo after_reset", 5000)
	if env.Output != "after_reset" {
		t.Fatalf("post-hard-reset output=%q (state=%q)", env.Output, env.State)
	}
}
