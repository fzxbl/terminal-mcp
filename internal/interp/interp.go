package interp

import (
	"regexp"
	"strconv"
	"strings"
)

// SentinelTag 是 bash 提示符哨兵的分隔标记：可打印 ASCII，正常输出几乎不会出现；
// 不能用不可打印控制字符（如 0x1e）——readline 渲染 PS1 时会把它们剥掉，导致哨兵永不出现。
const SentinelTag = "@@PTYSESS@@"

// bashSentinelPS1：@@PTYSESS@@EXIT=<$?>@@PTYSESS@@> ，$? 保持字面由 bash 每次提示符展开。
// 用 \[\e[1;32m\]…\[\e[0m\] 给提示符上绿色：`\[ \]` 是 readline 的"零宽"标记，告诉它括起来的
// 字节不计入光标列宽（bash 输出到 PTY 前会吃掉 \[ \]，线上字节只剩标准 SGR），因此人工接管行内
// 编辑时光标不会错位；模型侧 detectPromptAtTail/cleanOutput 都先 StripANSI，颜色被剥净不影响解析。
const bashSentinelPS1 = "\\[\\e[1;32m\\]" + SentinelTag + "EXIT=$?" + SentinelTag + ">\\[\\e[0m\\] "

// SetBashSentinelCmd 返回让 bash 采用哨兵提示符的初始化命令（另关 PROMPT_COMMAND 干扰）。
// 用单引号包裹整个 PS1，让 $? 保持字面量、由 bash 在每次显示提示符时才展开（promptvars 默认开），
// 从而每条命令后反映其真实退出码；若用双引号会在赋值时就把 $? 冻结成当时的值（bug）。
// 会话以带色 TERM 启动，故顺手关掉 bracketed-paste，避免每个提示符前后夹 \x1b[?2004h/l 噪声。
func SetBashSentinelCmd() string {
	return "PS1='" + bashSentinelPS1 + "';PS2='';PROMPT_COMMAND='';bind 'set enable-bracketed-paste off' 2>/dev/null"
}

var (
	// ANSI/控制序列匹配。顺序敏感：先 OSC/DCS 吃掉带 body 的序列，再 CSI，最后兜底单字符转义。
	ansiOSC   = regexp.MustCompile("\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)") // OSC ... BEL/ST
	ansiDCS   = regexp.MustCompile("\x1b[P^_X][^\x1b]*\x1b\\\\")           // DCS/SOS/PM/APC ... ST
	ansiCSI   = regexp.MustCompile("\x1b\\[[0-9:;<=>?]*[ -/]*[@-~]")       // CSI（参数含 : <=>，覆盖 colon 形真彩色）
	ansiNF    = regexp.MustCompile("\x1b[ -/][0-~]")                       // nF（字符集切换等双字符转义）
	ansiFe    = regexp.MustCompile("\x1b[@-Z\\\\-_]")                      // 其余 Fe 双字符转义（不含 '[' CSI）
	ctrlChars = regexp.MustCompile("[\x00-\x08\x0b\x0c\x0e-\x1f]")         // 剩余控制字符（含裸 ESC；保留 \t \n）
)

// StripANSI 去尽 ANSI 转义/控制序列与裸控制字符，并把 CRLF 归一为 LF；不做任何行级过滤。
// 供 cleanOutput（喂 LLM）与 detectPromptAtTail（提示符检测）共用：会话开着色后，
// 输出可能夹带 SGR 颜色、括号粘贴 \x1b[?2004h 等，靠它统一剥净，保证解析与匹配不受干扰。
func StripANSI(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = ansiOSC.ReplaceAllString(s, "")
	s = ansiDCS.ReplaceAllString(s, "")
	s = ansiCSI.ReplaceAllString(s, "")
	s = ansiNF.ReplaceAllString(s, "")
	s = ansiFe.ReplaceAllString(s, "")
	return ctrlChars.ReplaceAllString(s, "")
}

var (
	reBashSentinel = regexp.MustCompile(`@@PTYSESS@@EXIT=(-?\d+)@@PTYSESS@@> $`)
	rePythonPrompt = regexp.MustCompile(`(?:^|\n)(>>> |\.\.\. )$`)
	reGdbPrompt    = regexp.MustCompile(`(?:^|\n)\(gdb\) $`)
)

// DetectPromptAtTail 判断缓冲末尾是否停在某解释器提示符。
// 先 StripANSI 去尽颜色/括号粘贴等控制序列，避免着色开启后提示符尾部被 \x1b[?2004h 等破坏 $ 锚定。
// 返回解释器名、对外暴露的 prompt 字符串（bash 哨兵不外露，返回 ""）、bash 退出码、命中与否。
func DetectPromptAtTail(buf string) (interp, prompt string, exitCode *int, ok bool) {
	buf = StripANSI(buf)
	if m := reBashSentinel.FindStringSubmatch(buf); m != nil {
		n, _ := strconv.Atoi(m[1])
		return "bash", "", &n, true
	}
	if reGdbPrompt.MatchString(buf) {
		return "gdb", "(gdb) ", nil, true
	}
	if m := rePythonPrompt.FindStringSubmatch(buf); m != nil {
		return "python", strings.TrimRight(m[1], " ") + " ", nil, true
	}
	return "", "", nil, false
}
