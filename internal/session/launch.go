package session

import (
	"strings"

	"github.com/fzxbl/terminal-mcp/internal/config"
)

// BuildStartArgs 构造 exec 的 name+args。
//
//	mode=local: 直接跑 shell/command（command 为空则用 default_shell）
//	mode=ssh:   ssh <ssh_opts> <ssh_user>@<host> 'TERM=xterm-256color bash --norc --noprofile'
//	            若配置了 ssh_login_prologue，则以 sh -c '<prologue> </dev/null && exec "$@"' 包一层，
//	            让鉴证在本地同一 PTY 会话内先完成、再 exec 进 ssh（不残留本地交互 shell）。
//
// hostOrCommand: ssh 模式为目标 host；local 模式为可选自定义命令（空则默认 shell）。
func BuildStartArgs(mode, hostOrCommand string) (string, []string) {
	c := config.Get()
	switch mode {
	case "ssh":
		sshOpts := strings.Fields(c.SSHOpts)
		target := hostOrCommand
		if c.SSHUser != "" {
			target = c.SSHUser + "@" + hostOrCommand
		}
		const remoteRun = "TERM=xterm-256color bash --norc --noprofile"
		if prologue := strings.TrimSpace(c.SSHLoginPrologue); prologue != "" {
			// sh -c '{ <prologue>; } </dev/null && exec "$@"' sh ssh <opts...> <target> <remoteRun>
			// 整条 prologue（可含多条 && 串接）用 { ...; } 分组，成功后 exec 用 ssh 顶替进程，
			// 本地不残留交互 shell；prologue 失败则 && 短路、sh 退出结束会话，同样不落到本地 shell。
			// ssh 及参数经位置参数 "$@" 透传，shell 不二次解析 host，避免注入；分组 stdin 重定向自
			// /dev/null，防止任一前置命令吞掉随后写入的哨兵 PS1 初始化。
			inner := "{ " + prologue + "; } </dev/null && exec \"$@\""
			args := []string{"-c", inner, "sh", "ssh"}
			args = append(args, sshOpts...)
			args = append(args, target, remoteRun)
			return "sh", args
		}
		args := append(sshOpts, target, remoteRun)
		return "ssh", args
	default: // local
		if hostOrCommand != "" {
			return "sh", []string{"-c", hostOrCommand}
		}
		return c.DefaultShell, []string{"--norc", "--noprofile"}
	}
}
