// Package textexplore 在固定文本快照内做纯文本探索（stat/read/grep）。
// 输入为「可重复创建的 io.Reader 工厂」，每次调用从起点重新逐行扫描并清洗，
// 保证 stat/read/grep 的逻辑行号一致。不依赖 session/oplog/config。
package textexplore

import (
	"bufio"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/fzxbl/terminal-mcp/internal/clean"
)

// maxRawLine 单行原始扫描上限（换行前的字节数）。
const maxRawLine = 4 << 20

// Options 控制清洗语义，对齐 session 的 ObserveOrClean。
type Options struct {
	Observe bool   // 人工接管区间：还原 [rc=n] $ cmd
	Input   string // 命令回显剥离（仅首个匹配行）
}

// Result 是导航元数据（正文由调用方单独返回）。
type Result struct {
	Op             string
	SizeBytes      int64
	LineCount      int
	MaxLineBytes   int
	NextLineOffset int
	ByteOffset     int // 超长行字节切分时行内续读游标；0 表示整行已消费
	EOF            bool
	Truncated      bool
}

type longLineError struct{}

func (*longLineError) Error() string { return "textexplore: single line exceeds scan limit" }

var errLongLine = &longLineError{}

// readLimitedLine 读到 '\n' 或 EOF，返回不含 '\n'（及尾随 '\r'）的行。
// failOnLongLine=true（grep）：单行超过 maxRawLine 返回 errLongLine（不整行加载）。
// failOnLongLine=false（stat/read）：容忍超长行，用无界 ReadString 把整行读完，
// 以便 Read 能对超长行做行内字节切片续读。
func readLimitedLine(br *bufio.Reader, failOnLongLine bool) (string, error) {
	if !failOnLongLine {
		// 容忍超长行：一次读到 '\n' 或 EOF，不设 maxRawLine 上限。
		s, err := br.ReadString('\n')
		if err != nil {
			// EOF（无尾随换行）：返回原样剩余，与有界路径的 EOF 行为一致。
			return s, err
		}
		return strings.TrimSuffix(strings.TrimSuffix(s, "\n"), "\r"), nil
	}
	var sb strings.Builder
	for {
		b, err := br.ReadByte()
		if err != nil {
			return sb.String(), err
		}
		if b == '\n' {
			return strings.TrimSuffix(sb.String(), "\r"), nil
		}
		if sb.Len() >= maxRawLine {
			return sb.String(), errLongLine
		}
		sb.WriteByte(b)
	}
}

// cleanLines 从工厂 reader 逐物理行读取，拼回后走一次现有清洗，
// 与 session 语义完全一致（含首行 echo-strip / observe 还原），再拆成清洗后逻辑行。
// failOnLongLine=true 时单行超 maxRawLine 立即 errLongLine（grep）；false 时容忍并整行加载（stat/read）。
func cleanLines(src func() io.Reader, opt Options, failOnLongLine bool) ([]string, error) {
	br := bufio.NewReader(src())
	var raw []string
	for {
		line, err := readLimitedLine(br, failOnLongLine)
		if err == errLongLine {
			return nil, errLongLine
		}
		if err == nil {
			raw = append(raw, line)
			continue
		}
		if err == io.EOF {
			if line != "" {
				raw = append(raw, line)
			}
			break
		}
		return nil, err
	}
	joined := strings.Join(raw, "\n")
	var cleaned string
	if opt.Observe {
		cleaned = clean.ObserveOrClean(joined, true)
	} else {
		cleaned = clean.CleanOutput(joined, opt.Input)
	}
	if cleaned == "" {
		return nil, nil
	}
	return strings.Split(cleaned, "\n"), nil
}

// Stat 返回清洗后行数与最大行字节（SizeBytes 由调用方按原始 scope 长度填充）。
func Stat(src func() io.Reader, opt Options) (Result, error) {
	lines, err := cleanLines(src, opt, false)
	if err != nil {
		return Result{Op: "stat"}, err
	}
	res := Result{Op: "stat", LineCount: len(lines)}
	for _, ln := range lines {
		if len(ln) > res.MaxLineBytes {
			res.MaxLineBytes = len(ln)
		}
	}
	return res, nil
}

// Read 从 lineOffset（负值从尾部倒数）起读最多 limit 行、正文总字节尽量不超 maxBytes。
// 若某行本身超 maxBytes，则从 byteOffset 起在行内切一段返回，NextLineOffset 停在本行、ByteOffset 前进。
func Read(src func() io.Reader, opt Options, lineOffset, byteOffset, limit, maxBytes int) (string, Result, error) {
	lines, err := cleanLines(src, opt, false)
	if err != nil {
		return "", Result{Op: "read"}, err
	}
	n := len(lines)
	start := lineOffset
	if start < 0 {
		start = n + start
	}
	if start < 0 {
		start = 0
	}
	res := Result{Op: "read", LineCount: n}
	if start >= n {
		res.EOF = true
		res.NextLineOffset = n
		return "", res, nil
	}
	if cur := lines[start]; maxBytes > 0 && len(cur)-byteOffset > maxBytes {
		seg := cur[byteOffset : byteOffset+maxBytes]
		res.NextLineOffset = start
		res.ByteOffset = byteOffset + maxBytes
		res.Truncated = true
		return seg, res, nil
	}
	var out []string
	used := byteOffset
	i := start
	for ; i < n && len(out) < limit; i++ {
		ln := lines[i]
		if i == start && byteOffset > 0 {
			ln = ln[byteOffset:]
		}
		if maxBytes > 0 && used+len(ln) > maxBytes && len(out) > 0 {
			break
		}
		out = append(out, ln)
		used += len(ln) + 1
	}
	res.NextLineOffset = i
	res.EOF = i >= n
	return strings.Join(out, "\n"), res, nil
}

// Grep 从 lineOffset 起逐行匹配 pattern，最多 limit 个命中，各带 before/after 上下文行，
// 相邻/重叠上下文只出现一次。命中行前缀 "> <行号>: "，上下文行前缀 "  <行号>: "。
func Grep(src func() io.Reader, opt Options, pattern string, lineOffset, before, after, limit, maxBytes int) (string, Result, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", Result{Op: "grep"}, err
	}
	lines, err := cleanLines(src, opt, true)
	if err != nil {
		return "", Result{Op: "grep"}, err
	}
	n := len(lines)
	res := Result{Op: "grep", LineCount: n}
	if lineOffset < 0 {
		lineOffset = 0
	}
	emitted := make([]bool, n)
	var sb strings.Builder
	hits := 0
	last := lineOffset - 1
	i := lineOffset
	for ; i < n; i++ {
		if !re.MatchString(lines[i]) {
			continue
		}
		lo := i - before
		if lo < 0 {
			lo = 0
		}
		if lo <= last {
			lo = last + 1
		}
		hi := i + after
		if hi >= n {
			hi = n - 1
		}
		for j := lo; j <= hi; j++ {
			if emitted[j] {
				continue
			}
			marker := "  "
			if j == i {
				marker = "> "
			}
			line := marker + strconv.Itoa(j) + ": " + lines[j] + "\n"
			if maxBytes > 0 && sb.Len()+len(line) > maxBytes && sb.Len() > 0 {
				res.NextLineOffset = i
				res.EOF = false
				return strings.TrimRight(sb.String(), "\n"), res, nil
			}
			sb.WriteString(line)
			emitted[j] = true
		}
		last = hi
		hits++
		if limit > 0 && hits >= limit {
			i++
			break
		}
	}
	res.NextLineOffset = i
	res.EOF = i >= n
	return strings.TrimRight(sb.String(), "\n"), res, nil
}
