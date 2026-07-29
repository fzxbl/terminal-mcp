package session

import (
	"regexp"
	"strings"
	"time"

	"github.com/fzxbl/terminal-mcp/internal/config"
	"github.com/fzxbl/terminal-mcp/internal/interp"
	"github.com/fzxbl/terminal-mcp/internal/pty"
)

// matchesShellSwitch 判断 input 是否命中"会切进新一层 shell"的命令注册表。
// 尽力（非完整 shell 解析）：按 && / ; / || / | 切，取最后一段；先按整段前缀匹配多词注册项
// （如 sudo -i / docker exec，必须在剥 sudo 之前），再剥 env/VAR=val/sudo 前缀后按首 token 匹配。
func matchesShellSwitch(input string) bool {
	seg := strings.TrimSpace(lastSegment(input))
	if seg == "" {
		return false
	}
	cmds := config.Get().ShellSwitchCommands
	// 1) 整段前缀匹配（覆盖 "sudo -i"/"sudo su"/"docker exec x" 等多词项，须早于剥 sudo）。
	if matchAny(seg, cmds) {
		return true
	}
	// 2) 剥掉 env / VAR=val / 一个 leading sudo 前缀后再匹配（含首 token）。
	stripped := stripPrefixTokens(seg)
	if stripped == "" {
		return false
	}
	if matchAny(stripped, cmds) {
		return true
	}
	return matchAny(firstToken(stripped), cmds)
}

// lastSegment 尽力按顶层 && / ; / || / | 切分，返回最后一段非空片段。
// 非完整 shell 解析：简单字符串替换即可满足注册表首 token 前缀匹配的需要。
func lastSegment(input string) string {
	s := input
	for _, sep := range []string{"&&", "||", ";", "|"} {
		s = strings.ReplaceAll(s, sep, "\n")
	}
	parts := strings.Split(s, "\n")
	for i := len(parts) - 1; i >= 0; i-- {
		if strings.TrimSpace(parts[i]) != "" {
			return parts[i]
		}
	}
	return ""
}

// stripPrefixTokens 剥掉命令前的 env 关键字、VAR=val 环境赋值、以及一个 leading sudo（后面仍有命令时）。
func stripPrefixTokens(seg string) string {
	toks := strings.Fields(seg)
	i := 0
	if i < len(toks) && toks[i] == "env" {
		i++
	}
	for i < len(toks) && strings.Contains(toks[i], "=") {
		i++
	}
	if i < len(toks) && toks[i] == "sudo" && i+1 < len(toks) {
		i++
		for i < len(toks) && strings.Contains(toks[i], "=") {
			i++
		}
	}
	return strings.Join(toks[i:], " ")
}

// firstToken 返回按空白切分后的首 token。
func firstToken(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// matchAny 判断 s 是否以注册表中某条目为"词边界前缀"（s==entry 或 s 以 "entry " 开头）。
// 词边界避免 "sshd foo" 误命中 "ssh"。
func matchAny(s string, entries []string) bool {
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if s == e || strings.HasPrefix(s, e+" ") {
			return true
		}
	}
	return false
}

// rearmTimeout 布哨后复检自家哨兵的等待上界（几秒足矣，封顶在 MaxBlockSeconds）。
func rearmTimeout() time.Duration {
	d := 3 * time.Second
	if max := time.Duration(config.Get().MaxBlockSeconds) * time.Second; max > 0 && d > max {
		d = max
	}
	return d
}

var (
	// reShellPromptTail：末尾停在"疑似 shell 提示符"——非空白 + 可选空格 + [$#%>] + 可选空格结尾。
	reShellPromptTail = regexp.MustCompile(`[^\s][ \t]*[$#%>][ \t]*$`)
	// reAuthPromptTail：交互鉴权问询（password/passphrase/yes-no/host-key 指纹），命中则绝不注入。
	reAuthPromptTail = regexp.MustCompile(`(?i)(password|passphrase|\(yes/no(/\[fingerprint\])?\)\??)[^\n]*$`)
)

// lastNonEmptyLine 返回 StripANSI 后最后一非空行（用于提示符判定）。
func lastNonEmptyLine(tail string) string {
	s := interp.StripANSI(tail)
	last := ""
	for _, ln := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if strings.TrimSpace(ln) != "" {
			last = ln
		}
	}
	return last
}

// looksLikeShellPrompt 判断 tail 是否停在"疑似新 shell 提示符"：末行以 $ # % > 结尾，
// 且不是 ssh 的密码/口令/host-key 交互问询。自动布哨前的安全闸——只有确实到了一个新 shell
// 的提示符才注入 PS1，避免把哨兵命令误打进 ssh 的 password:/(yes/no)? 等交互输入里。
func looksLikeShellPrompt(tail string) bool {
	last := lastNonEmptyLine(tail)
	if last == "" || reAuthPromptTail.MatchString(last) {
		return false
	}
	return reShellPromptTail.MatchString(last)
}

// looksLikeAuthPrompt 判断 tail 是否停在交互鉴权问询（password/passphrase/yes-no/host-key）。
func looksLikeAuthPrompt(tail string) bool {
	last := lastNonEmptyLine(tail)
	return last != "" && reAuthPromptTail.MatchString(last)
}

// waitForShellPromptSince 轮询直到 since 之后的新输出停在"疑似 shell 提示符"或已知提示符，或到 deadline。
// 专为切换命令（ssh 等分段吐输出、远端提示符姗姗来迟）设计：不因第一个静默间隙就返回（那正是
// waitQuiet 在 ssh "Warning..." 后误判、错过远端 shell 的原因）。若停在交互鉴权提示符且已静默，则提前返回
// （不再干等），交由 Send 的安全闸跳过布哨、返回 running 让人工接管。
func waitForShellPromptSince(proc *pty.ProcSession, since int64, deadline time.Time) {
	quiet := time.Duration(config.Get().QuietWindowMs) * time.Millisecond
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		seg := proc.Since(since)
		if _, _, _, ok := interp.DetectPromptAtTail(seg); ok || looksLikeShellPrompt(seg) {
			return
		}
		if looksLikeAuthPrompt(seg) && time.Since(proc.LastByteTime()) >= quiet {
			return
		}
	}
}

// waitForBashSentinelSince 有界轮询，直到 since 偏移之后的新输出末尾出现自家 bash 哨兵，返回是否出现。
// 只看 since 之后的片段：避免把布哨前就存在的旧哨兵误当作本次布哨成功（否则紧随的 computeState
// 会撞上 PS1= 回显中途、误判 running）。新哨兵一旦出现即在真实末尾，后续 computeState 稳定为 idle。
func waitForBashSentinelSince(proc *pty.ProcSession, since int64, deadline time.Time) bool {
	for time.Now().Before(deadline) {
		if itp, _, _, ok := interp.DetectPromptAtTail(proc.Since(since)); ok && itp == "bash" {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}
