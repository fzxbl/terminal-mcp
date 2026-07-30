package clean

import (
	"strings"

	"github.com/fzxbl/terminal-mcp/internal/interp"
)

// LineCleaner 是 CleanOutput/ObserveOrClean 的逐行流式等价实现，供 textexplore 单遍清洗。
// 用法：对每条原始物理行（按 '\n' 切分，可含 CRLF/ANSI）调用 Clean；keep=false 表示该行被丢弃。
// 首尾空行的 Trim 语义：前导空行直接不发；尾随空行先缓冲，遇到下一非空行再补发（Flush 丢弃末尾残留空行）。
type LineCleaner struct {
	input        string
	observe      bool
	echoStripped bool
	started      bool // 是否已发出过任何非空行（用于跳过前导空行）
	pendingBlank int  // 已 keep 但尚未确认要保留的尾随空行数
}

func NewLineCleaner(input string, observe bool) *LineCleaner {
	return &LineCleaner{input: strings.TrimSpace(input), observe: observe}
}

// Clean 处理一条原始行，返回清洗后文本与是否保留。调用方只对 keep==true 的行按顺序收集。
// 注意：为实现首尾空行 Trim，本方法可能返回多行（用 '\n' 连接）或触发对之前缓冲空行的补发，
// 因此返回的是「本次应追加到输出的片段」；keep==false 表示无追加。
func (c *LineCleaner) Clean(rawLine string) (string, bool) {
	// 等价性关键：CleanOutput 对整段先 StripANSI（含 \r\n→\n、裸 \r→\n）再按 \n 切行；
	// 这里调用方按原始 \n 切分物理行，导致每条 CRLF 行末尾残留一个 \r（原本是 \r\n 的 CR）。
	// 若直接对其 StripANSI，裸 \r 会被转成 \n 形成一条多余空行，与整段清洗结果不一致。
	// 该末尾 \r 恒为行分隔符 \r\n 的 CR（物理行由 \n 切出，其后紧跟被切掉的 \n），故先剥掉它；
	// 行内其余裸 \r 仍交给 StripANSI 归一为 \n（等价于整段处理时的换行拆分）。
	rawLine = strings.TrimSuffix(rawLine, "\r")
	s := interp.StripANSI(rawLine) // 单行 StripANSI（ANSI 不跨行）；也统一 CRLF
	// StripANSI 可能把行内裸 \r 归一为 \n，从而把单条物理行再拆成多逻辑行，逐条处理。
	var kept []string
	for _, ln := range strings.Split(s, "\n") {
		out, ok := c.cleanOne(ln)
		if !ok {
			continue
		}
		if out == "" {
			if !c.started {
				continue // 丢前导空行
			}
			c.pendingBlank++ // 缓冲尾随空行
			continue
		}
		// 非空行：先补发之前缓冲的空行
		for c.pendingBlank > 0 {
			kept = append(kept, "")
			c.pendingBlank--
		}
		kept = append(kept, out)
		c.started = true
	}
	if len(kept) == 0 {
		return "", false
	}
	return strings.Join(kept, "\n"), true
}

// cleanOne 处理单条已 StripANSI 的行，返回渲染文本与是否保留（未做空行 Trim）。
func (c *LineCleaner) cleanOne(ln string) (string, bool) {
	if c.observe {
		if !strings.Contains(ln, interp.SentinelTag) {
			return ln, true
		}
		loc := reSentinelPrompt.FindStringSubmatchIndex(ln)
		if loc == nil {
			return "", false
		}
		var out []string
		if prefix := ln[:loc[0]]; prefix != "" {
			out = append(out, prefix)
		}
		rc := ln[loc[2]:loc[3]]
		cmd := strings.TrimRight(ln[loc[1]:], " ")
		if cmd != "" {
			out = append(out, "[rc="+rc+"] $ "+cmd)
		}
		if len(out) == 0 {
			return "", false
		}
		return strings.Join(out, "\n"), true
	}
	// 非 observe
	if strings.Contains(ln, interp.SentinelTag) {
		return "", false
	}
	if !c.echoStripped && c.input != "" {
		t := strings.TrimSpace(ln)
		if t == c.input || strings.HasSuffix(t, c.input) {
			c.echoStripped = true
			return "", false
		}
	}
	return ln, true
}

// Flush 在所有行处理完后调用：丢弃末尾缓冲的空行（对应 CleanOutput 的尾部 Trim("\n")）。
// pendingBlank 是尾随空行，直接丢弃即可；out 保持不变。
func (c *LineCleaner) Flush(out []string) []string {
	return out
}
