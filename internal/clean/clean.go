package clean

import (
	"regexp"
	"strings"

	"github.com/fzxbl/terminal-mcp/internal/interp"
)

var (
	// 锚定版：仅用于判定"某段 s[e:] 是否为已闭合的完整转义"，供 PendingEscBoundary 使用。
	reCSIFull = regexp.MustCompile("^\x1b\\[[0-9:;<=>?]*[ -/]*[@-~]")
	reOSCFull = regexp.MustCompile("^\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)")
	reDCSFull = regexp.MustCompile("^\x1b[P^_X][^\x1b]*\x1b\\\\")
	reNFFull  = regexp.MustCompile("^\x1b[ -/][0-~]")
)

// PendingEscBoundary 返回 s 末尾"被读取边界切断、尚未闭合的转义序列"的起始下标；
// 末尾无残缺转义时返回 len(s)。典型场景：命令仍在输出时，本次读到的尾部恰好切在
// 一条颜色序列中间（如 \x1b[38;2;25），若直接交给 CleanOutput，ctrlChars 只会抹掉裸 ESC，
// 残留的 "[38;2;25" 会以文本形式漏给 LLM，且续接的 "5;0m" 下次又成孤儿。
// 调用方据此暂缓 s[boundary:]、只交付 s[:boundary]，待下次读取补齐后整体清洗，杜绝半截颜色泄漏。
func PendingEscBoundary(s string) int {
	e := strings.LastIndexByte(s, 0x1b)
	if e < 0 {
		return len(s)
	}
	tail := s[e:]
	if reCSIFull.MatchString(tail) || reOSCFull.MatchString(tail) ||
		reDCSFull.MatchString(tail) || reNFFull.MatchString(tail) {
		return len(s) // 末尾转义已闭合
	}
	if len(tail) < 2 {
		return e // 仅有裸 \x1b，等下一字节
	}
	switch c := tail[1]; {
	case c == '[' || c == ']' || c == 'P' || c == 'X' || c == '^' || c == '_':
		return e // CSI/OSC/DCS 等变长序列已起始但未终止
	case c >= 0x20 && c <= 0x2f:
		return e // nF 序列还差终止字节
	default:
		return len(s) // 完整的双字符 Fe 转义
	}
}

// DeliverBoundary 决定本次可安全交付给 LLM 的字节数：命令仍在输出（running）时暂缓末尾残缺转义，
// 其余状态（idle/exited/dead，输出已定型）全部交付，避免把最后半截转义永久滞留。
func DeliverBoundary(raw, state string) int {
	if state == "running" {
		return PendingEscBoundary(raw)
	}
	return len(raw)
}

// reSentinelPrompt 匹配 bash 哨兵提示符前缀 "@@PTYSESS@@EXIT=<n>@@PTYSESS@@> "，捕获退出码。
var reSentinelPrompt = regexp.MustCompile("@@PTYSESS@@EXIT=(-?\\d+)@@PTYSESS@@> ")

// ObserveOrClean 按是否处于人工接管选择清洗方式：
// 接管中（observe=true）用 CleanOutputObserve 保留人敲的命令，便于模型观察/学习排查步骤；
// 否则走常规 CleanOutput（丢弃哨兵行、不显示命令回显）。
func ObserveOrClean(raw string, observe bool) string {
	if observe {
		return CleanOutputObserve(raw)
	}
	return CleanOutput(raw, "")
}

// CleanOutputObserve 供人工接管观察：先 StripANSI，再把哨兵提示符行还原成可读的 "[rc=n] $ <命令>"，
// 从而让模型能看到人实际输入的每条命令（含上一条的退出码）；光提示符（无命令）行丢弃。
func CleanOutputObserve(raw string) string {
	s := interp.StripANSI(raw)
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if !strings.Contains(ln, interp.SentinelTag) {
			out = append(out, ln)
			continue
		}
		loc := reSentinelPrompt.FindStringSubmatchIndex(ln)
		if loc == nil {
			continue // 含标记但不是完整提示符（残缺/被切断），丢弃
		}
		if prefix := ln[:loc[0]]; prefix != "" { // 提示符前若粘着上一条命令的输出，保留为独立行
			out = append(out, prefix)
		}
		rc := ln[loc[2]:loc[3]]
		cmd := strings.TrimRight(ln[loc[1]:], " ")
		if cmd != "" { // 有命令才渲染成提示符行；光提示符丢弃
			out = append(out, "[rc="+rc+"] $ "+cmd)
		}
	}
	return strings.Trim(strings.Join(out, "\n"), "\n")
}

// CleanOutput 清洗给 LLM 的输出：先 StripANSI 去尽颜色/控制序列，再丢弃含可打印哨兵标记 @@PTYSESS@@ 的行、
// 尽力剥掉开头被回显的 input 命令行（等于 input 或以 input 结尾，兼容 (gdb) 等提示符前缀）。
func CleanOutput(raw, input string) string {
	s := interp.StripANSI(raw)

	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	inputTrim := strings.TrimSpace(input)
	echoStripped := false
	for _, ln := range lines {
		if strings.Contains(ln, interp.SentinelTag) { // 哨兵提示符行
			continue
		}
		if !echoStripped && inputTrim != "" {
			t := strings.TrimSpace(ln)
			if t == inputTrim || strings.HasSuffix(t, inputTrim) {
				echoStripped = true
				continue
			}
		}
		out = append(out, ln)
	}
	return strings.Trim(strings.Join(out, "\n"), "\n")
}
