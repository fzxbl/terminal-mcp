package pty

import (
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/fzxbl/terminal-mcp/internal/oplog"
	"golang.org/x/sys/unix"
)

// ProcSession 持有一个 live 子进程（顶层 shell），经 PTY master 读写；
// 后台 goroutine 持续把 master 输出 append 到 oplog（磁盘 .raw 为唯一真相源）。
type ProcSession struct {
	cmd      *exec.Cmd
	ptmx     *os.File
	mu       sync.Mutex
	lastByte time.Time
	dead     bool
	log      *oplog.Log
}

// PTY 默认窗口：故意设很宽，避免模型的宽输出被终端宽度硬换行污染；
// 人工接管时会临时同步成浏览器终端尺寸（vim/top 才能正确绘制），退出接管后恢复此默认值。
const (
	defaultPtyRows = 200
	defaultPtyCols = 1000
)

// NewProcSession 用真 PTY 启动子进程。手动开 pty 而非用 pty.Start：给子进程一个 tty 作为
// stdin/stdout/stderr（配合 ssh -tt 恢复行缓冲、可交互），并用 Setsid+Setctty 让子进程成为
// 会话首进程、把该 tty 设为其【受控终端】——这样内核才会维护前台进程组，ctrl-c/ctrl-z 等
// 控制字节能作为信号（SIGINT/SIGTSTP…）投递到前台命令，vim/top 等也能正常接收窗口变化。
// pty.Setsize 必须在任何写入前调用，避免宽输出被终端宽度硬换行污染。
// logPath/cacheBytes 用于打开 oplog（.raw 为强依赖，打不开即返回错误）。
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
	// Setsid + Setctty：子进程新建会话并把 tty（stdin, fd 0）设为受控终端。close 时对
	// 进程组（pgid==pid，会话首进程）发信号即可一次性带走其 fork 的 gdb/python 等孙进程。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		_ = ptmx.Close()
		_ = tty.Close()
		_ = lg.Close()
		return nil, err
	}
	_ = tty.Close() // 父进程持有 master 即可，slave 交给子进程
	p := &ProcSession{cmd: cmd, ptmx: ptmx, log: lg}
	go func() {
		b := make([]byte, 4096)
		for {
			n, rerr := ptmx.Read(b)
			if n > 0 {
				if _, werr := p.log.Append(b[:n]); werr != nil {
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

// Write 向 PTY 写入（即向远端 tty 输入）。
func (p *ProcSession) Write(s string) { _, _ = io.WriteString(p.ptmx, s) }

// SetSize 调整 PTY 窗口大小（人工接管时与浏览器终端尺寸同步；行/列为 0 时忽略）。
// 子进程已有受控终端，改 master 窗口（TIOCSWINSZ）内核会自动向前台组投递 SIGWINCH；
// 这里再显式补发一次，兼顾极早期尚未建立前台组的时序，确保 ssh 转发窗口、vim/top 及时重绘。
func (p *ProcSession) SetSize(rows, cols uint16) {
	if rows == 0 || cols == 0 {
		return
	}
	_ = pty.Setsize(p.ptmx, &pty.Winsize{Rows: rows, Cols: cols})
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Signal(syscall.SIGWINCH)
	}
}

// Interrupt 向 PTY 写入 Ctrl-C（ETX, 0x03），打断当前正在运行/卡住的命令。
// 故意【不加锁】：exec 阻塞时持有 sess.mu，中断必须绕过该锁才能送达；写 pty 与读 oplog 互不冲突。
func (p *ProcSession) Interrupt() { _, _ = p.ptmx.Write([]byte{0x03}) }

// Len 返回日志绝对总字节数。
func (p *ProcSession) Len() int64 { return p.log.Len() }

// ReadRange 返回绝对区间 [from,to) 内容。
func (p *ProcSession) ReadRange(from, to int64) string {
	b, _ := p.log.ReadRange(from, to)
	return string(b)
}

// RangeReader 返回底层日志固定区间的流式 reader。
func (p *ProcSession) RangeReader(from, to int64) io.Reader { return p.log.RangeReader(from, to) }

// Since 返回从绝对偏移 off 起到末尾的内容。
func (p *ProcSession) Since(off int64) string { return p.ReadRange(off, p.log.Len()) }

// Tail 返回最近 n 字节。
func (p *ProcSession) Tail(n int) string { return string(p.log.Tail(n)) }

// LastByteTime 返回最近一次收到输出的时刻（判定静默用）。
func (p *ProcSession) LastByteTime() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastByte
}

// IsDead 报告后台 reader 是否已因 EOF/错误退出（子进程/PTY 终止）。
func (p *ProcSession) IsDead() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dead
}

// FlushInput 清空内核 PTY 输入队列（丢弃排队但未被程序读取的输入），用于 reset。
func (p *ProcSession) FlushInput() error {
	return unix.IoctlSetInt(int(p.ptmx.Fd()), unix.TCFLSH, unix.TCIFLUSH)
}

// KillLine 发送 Ctrl-U（NAK 0x15），清掉当前行编辑缓冲里的半行输入。
func (p *ProcSession) KillLine() { _, _ = p.ptmx.Write([]byte{0x15}) }

// Close 关闭 PTY 并杀掉子进程整组，回收其在会话内启动的 gdb/python 等孙进程，避免遗留。
// 对 ssh 远端会话：kill 本地 ssh 客户端 + 关 PTY 会令远端 sshd 挂断（SIGHUP），远端前台
// gdb 随之退出；本地 gdb（local 模式）则由进程组 kill 直接带走。最后 Wait 回收僵尸。
func (p *ProcSession) Close() {
	_ = p.ptmx.Close()
	if p.cmd.Process != nil {
		pid := p.cmd.Process.Pid
		// 子进程以 Setpgid 建组，pgid==pid，负号对整组发信号，带走全部孙进程。
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = p.cmd.Process.Kill() // 兜底：万一未成组，至少杀掉直接子进程
	}
	go func() { _ = p.cmd.Wait() }() // 回收僵尸进程
	_ = p.log.Close()
}
