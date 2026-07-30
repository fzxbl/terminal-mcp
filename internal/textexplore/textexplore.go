// Package textexplore 在固定文本快照内做纯文本探索（stat/read/grep）。
// 输入为「可重复创建的 io.Reader 工厂」，每次调用从起点重新逐行扫描并清洗，
// 保证 stat/read/grep 的逻辑行号一致。不依赖 session/oplog/config。
//
// 实现为单遍流式：逐条读原始物理行 → clean.LineCleaner 逐行清洗 → 对每条清洗后
// 逻辑行回调消费。任何算子都不再把全部逻辑行物化进内存（负向 read 仅缓冲窗口、
// grep 仅缓冲 before 上下文，均为 O(窗口) 而非 O(全部)）。
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

// eachCleanLine 逐条读原始物理行 → LineCleaner 清洗 → 对每条「清洗后逻辑行」调 fn(idx, line)。
// idx 从 0 递增；fn 返回 false 提前停止扫描。failOnLongLine=true 时超长行返回 errLongLine（grep 用）。
//
// 一次 LineCleaner.Clean 可能返回含 '\n' 的片段（observe 还原、裸 CR 拆行等），因此需按 '\n'
// 拆开、逐条当作独立逻辑行计数，才能与旧的 strings.Split(CleanOutput(...), "\n") 行集完全一致。
// LineCleaner 内部已丢弃首部空行、缓冲尾随空行（Flush 为 no-op），无需在此额外 Trim。
func eachCleanLine(src func() io.Reader, opt Options, failOnLongLine bool, fn func(idx int, line string) bool) error {
	br := bufio.NewReader(src())
	lc := clean.NewLineCleaner(opt.Input, opt.Observe)
	idx := 0
	// emit 拆分清洗片段为逐条逻辑行，返回 false 表示 fn 要求提前停止。
	emit := func(fragment string) bool {
		for _, piece := range strings.Split(fragment, "\n") {
			if !fn(idx, piece) {
				return false
			}
			idx++
		}
		return true
	}
	for {
		line, err := readLimitedLine(br, failOnLongLine)
		if err == errLongLine {
			return errLongLine
		}
		if err == nil {
			if frag, keep := lc.Clean(line); keep {
				if !emit(frag) {
					return nil
				}
			}
			continue
		}
		if err == io.EOF {
			if line != "" {
				if frag, keep := lc.Clean(line); keep {
					if !emit(frag) {
						return nil
					}
				}
			}
			return nil
		}
		return err
	}
}

// Stat 返回清洗后行数与最大行字节（SizeBytes 由调用方按原始 scope 长度填充）。O(1) 内存。
func Stat(src func() io.Reader, opt Options) (Result, error) {
	res := Result{Op: "stat"}
	err := eachCleanLine(src, opt, false, func(_ int, line string) bool {
		res.LineCount++
		if len(line) > res.MaxLineBytes {
			res.MaxLineBytes = len(line)
		}
		return true
	})
	if err != nil {
		return Result{Op: "stat"}, err
	}
	return res, nil
}

// Read 从 lineOffset（负值从尾部倒数）起读最多 limit 行、正文总字节尽量不超 maxBytes。
// 若某行本身超 maxBytes，则从 byteOffset 起在行内切一段返回，NextLineOffset 停在本行、ByteOffset 前进。
func Read(src func() io.Reader, opt Options, lineOffset, byteOffset, limit, maxBytes int) (string, Result, error) {
	if lineOffset < 0 {
		return readNegative(src, opt, lineOffset, byteOffset, limit, maxBytes)
	}
	return readForward(src, opt, lineOffset, byteOffset, limit, maxBytes)
}

// readForward 正向 offset 单遍流式：跳过 idx<start，从 idx==start 起收集，受 limit/maxBytes 约束，
// 收满即提前停。EOF 需知道窗口后是否还有行——多读一行来判定（仍 O(1) 内存）。
func readForward(src func() io.Reader, opt Options, start, byteOffset, limit, maxBytes int) (string, Result, error) {
	res := Result{Op: "read"}
	var out []string
	used := byteOffset
	count := 0
	resolved := false     // 已在回调内确定 NextLineOffset/EOF 并提前停
	limitReached := false // 已收满 limit，等待判定窗口后是否还有行
	pendingNext := 0      // limit 收满后的 NextLineOffset 候选（末尾已收行号+1）
	err := eachCleanLine(src, opt, false, func(idx int, line string) bool {
		count = idx + 1
		if idx < start {
			return true
		}
		if limitReached {
			// 窗口后仍有行：NextLineOffset=收满处，未到末尾。
			res.NextLineOffset = pendingNext
			res.EOF = false
			resolved = true
			return false
		}
		// idx==start 必为首个可收集行（idx 连续递增）。超长行行内字节窗口只作用于起始行。
		if idx == start && maxBytes > 0 && len(line)-byteOffset > maxBytes {
			out = append(out, line[byteOffset:byteOffset+maxBytes])
			res.NextLineOffset = start
			res.ByteOffset = byteOffset + maxBytes
			res.Truncated = true
			resolved = true
			return false
		}
		ln := line
		if idx == start && byteOffset > 0 {
			ln = ln[byteOffset:]
		}
		if maxBytes > 0 && used+len(ln) > maxBytes && len(out) > 0 {
			res.NextLineOffset = idx
			res.EOF = false
			resolved = true
			return false
		}
		out = append(out, ln)
		used += len(ln) + 1
		if len(out) >= limit {
			pendingNext = idx + 1
			limitReached = true
		}
		return true
	})
	if err != nil {
		return "", Result{Op: "read"}, err
	}
	res.LineCount = count
	if !resolved {
		if limitReached {
			// 收满后扫描到末尾都没有更多行。
			res.NextLineOffset = pendingNext
			res.EOF = true
		} else {
			res.NextLineOffset = count
			res.EOF = true
		}
	}
	return strings.Join(out, "\n"), res, nil
}

// readNegative 负向 offset：先不知道总数，用容量 |offset|+max(limit-1,0) 的行环形缓冲存尾部，
// 扫完得到总数后解析真实起点，再按与正向完全一致的窗口逻辑输出。O(窗口) 内存。
func readNegative(src func() io.Reader, opt Options, lineOffset, byteOffset, limit, maxBytes int) (string, Result, error) {
	extra := limit - 1
	if extra < 0 {
		extra = 0
	}
	capN := (-lineOffset) + extra
	if capN < 1 {
		capN = 1
	}
	buf := make([]string, 0, capN)
	firstIdx := 0 // buf[0] 的全局行号
	count := 0
	err := eachCleanLine(src, opt, false, func(_ int, line string) bool {
		count++
		buf = append(buf, line)
		if len(buf) > capN {
			buf = buf[1:]
			firstIdx++
		}
		return true
	})
	if err != nil {
		return "", Result{Op: "read"}, err
	}
	total := count
	start := total + lineOffset
	if start < 0 {
		start = 0
	}
	res := Result{Op: "read", LineCount: total}
	if start >= total {
		res.EOF = true
		res.NextLineOffset = total
		return "", res, nil
	}
	// buf 覆盖 [firstIdx, total-1]，已证 start>=firstIdx。
	get := func(gidx int) string { return buf[gidx-firstIdx] }
	if cur := get(start); maxBytes > 0 && len(cur)-byteOffset > maxBytes {
		seg := cur[byteOffset : byteOffset+maxBytes]
		res.NextLineOffset = start
		res.ByteOffset = byteOffset + maxBytes
		res.Truncated = true
		return seg, res, nil
	}
	var out []string
	used := byteOffset
	i := start
	for ; i < total && len(out) < limit; i++ {
		ln := get(i)
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
	res.EOF = i >= total
	return strings.Join(out, "\n"), res, nil
}

// Grep 从 lineOffset 起逐行匹配 pattern，最多 limit 个命中，各带 before/after 上下文行，
// 相邻/重叠上下文只出现一次。命中行前缀 "> <行号>: "，上下文行前缀 "  <行号>: "。
//
// 单遍流式：before 用容量 before 的行环形缓冲提供；after 因是「命中后再来的行」，用 emitUntil
// 上界在后续迭代里逐条补发；去重由 last（已输出到的最高行号）保证——只输出行号 > last 的行。
func Grep(src func() io.Reader, opt Options, pattern string, lineOffset, before, after, limit, maxBytes int) (string, Result, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", Result{Op: "grep"}, err
	}
	if lineOffset < 0 {
		lineOffset = 0
	}
	if before < 0 {
		before = 0
	}
	res := Result{Op: "grep"}
	var sb strings.Builder
	count := 0
	hits := 0
	last := lineOffset - 1      // 已输出到的最高行号
	emitUntil := lineOffset - 1 // after 上下文的上界（含）；< lineOffset 表示当前无活动窗口
	curMatch := lineOffset - 1  // 当前正在输出窗口所属的命中行号（决定 maxBytes 截断时的 NextLineOffset）
	limitReached := false       // 已达 limit 命中数：不再识别新命中，仅补完 after 上下文
	matchIdxLimit := 0          // 触发 limit 的命中行号
	sawBeyond := false          // limit 后是否见过 idx>matchIdxLimit 的行（判 EOF）
	resolved := false
	type rec struct {
		idx  int
		text string
	}
	var recent []rec // 最近 before 条行（作 pre-context 源）
	emit := func(marker string, j int, text string) bool {
		line := marker + strconv.Itoa(j) + ": " + text + "\n"
		if maxBytes > 0 && sb.Len()+len(line) > maxBytes && sb.Len() > 0 {
			res.NextLineOffset = curMatch
			res.EOF = false
			resolved = true
			return false
		}
		sb.WriteString(line)
		return true
	}
	err = eachCleanLine(src, opt, true, func(idx int, text string) bool {
		count = idx + 1
		if idx < lineOffset {
			return true
		}
		if limitReached {
			if idx > matchIdxLimit {
				sawBeyond = true
			}
			if idx <= emitUntil && idx > last {
				if !emit("  ", idx, text) {
					return false
				}
				last = idx
				return true
			}
			// 越过 after 窗口：收尾。
			res.EOF = !sawBeyond
			resolved = true
			return false
		}
		// 先补发上一命中的 after 上下文（本行落在窗口内且未输出过）。
		if idx <= emitUntil && idx > last {
			if !emit("  ", idx, text) {
				return false
			}
			last = idx
		}
		if re.MatchString(text) {
			curMatch = idx
			lo := idx - before
			if lo < 0 {
				lo = 0
			}
			if lo <= last {
				lo = last + 1
			}
			base := idx - len(recent) // recent 覆盖 [base, idx-1]
			for j := lo; j <= idx-1; j++ {
				if !emit("  ", j, recent[j-base].text) {
					return false
				}
				last = j
			}
			if idx > last { // 命中行未被作为上下文输出过时才以 "> " 输出
				if !emit("> ", idx, text) {
					return false
				}
				last = idx
			}
			emitUntil = idx + after
			hits++
			if limit > 0 && hits >= limit {
				res.NextLineOffset = idx + 1
				matchIdxLimit = idx
				limitReached = true
			}
		}
		// 当前行入 pre-context 环形缓冲（供后续命中回看）。
		recent = append(recent, rec{idx, text})
		if len(recent) > before {
			recent = recent[len(recent)-before:]
		}
		return true
	})
	if err != nil {
		return "", Result{Op: "grep"}, err
	}
	if !resolved {
		if limitReached {
			res.EOF = !sawBeyond // NextLineOffset 已在命中时设为 matchIdxLimit+1
		} else {
			res.NextLineOffset = count
			res.EOF = true
		}
	}
	res.LineCount = count
	return strings.TrimRight(sb.String(), "\n"), res, nil
}
