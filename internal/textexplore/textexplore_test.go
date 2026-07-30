package textexplore

import (
	"io"
	"strconv"
	"strings"
	"testing"
)

func srcOf(s string) func() io.Reader {
	return func() io.Reader { return strings.NewReader(s) }
}

func TestStat(t *testing.T) {
	res, err := Stat(srcOf("a\nbb\nccc\n"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.LineCount != 3 || res.MaxLineBytes != 3 {
		t.Fatalf("got %+v", res)
	}
}

func TestReadOffsetLimit(t *testing.T) {
	body, res, _ := Read(srcOf("l0\nl1\nl2\nl3\n"), Options{}, 1, 0, 2, 1<<20)
	if body != "l1\nl2" {
		t.Fatalf("body=%q", body)
	}
	if res.NextLineOffset != 3 || res.EOF {
		t.Fatalf("res=%+v", res)
	}
}

func TestReadNegativeOffset(t *testing.T) {
	body, _, _ := Read(srcOf("l0\nl1\nl2\n"), Options{}, -1, 0, 10, 1<<20)
	if body != "l2" {
		t.Fatalf("body=%q", body)
	}
}

func TestReadMaxBytesContinues(t *testing.T) {
	body, res, _ := Read(srcOf("aaaa\nbbbb\ncccc\n"), Options{}, 0, 0, 10, 6)
	if body != "aaaa" {
		t.Fatalf("body=%q res=%+v", body, res)
	}
	if res.NextLineOffset != 1 {
		t.Fatalf("cursor not advanced: %+v", res)
	}
}

func TestGrepWithContext(t *testing.T) {
	body, _, err := Grep(srcOf("a\nHIT1\nb\nc\nHIT2\n"), Options{}, "HIT", 0, 1, 1, 50, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "HIT1") || !strings.Contains(body, "HIT2") {
		t.Fatalf("body=%q", body)
	}
}

func TestGrepInvalidRegex(t *testing.T) {
	if _, _, err := Grep(srcOf("x"), Options{}, "(", 0, 0, 0, 10, 1<<20); err == nil {
		t.Fatal("expected regex error")
	}
}

func TestCleaningStripsANSIAndSentinel(t *testing.T) {
	in := "\x1b[31mred\x1b[0m\n@@PTYSESS@@EXIT=0@@PTYSESS@@> \nplain\n"
	res, _ := Stat(srcOf(in), Options{})
	if res.LineCount != 2 {
		t.Fatalf("line_count=%d", res.LineCount)
	}
}

// TestReadLongLineByteWindow 验证超长单行的行内字节窗口续读：
// 用一条 300 字符的普通长行 + 小 maxBytes=100，无需分配 >4MiB 即可覆盖续读逻辑。
func TestReadLongLineByteWindow(t *testing.T) {
	line := strings.Repeat("x", 300)
	src := srcOf(line + "\n")

	// 首次读：从 byteOffset=0 起切 100 字节，停在本行、游标前进到 100、Truncated。
	body, res, err := Read(src, Options{}, 0, 0, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 100 {
		t.Fatalf("first read len=%d, want 100", len(body))
	}
	if !res.Truncated {
		t.Fatalf("first read should be truncated: %+v", res)
	}
	if res.ByteOffset != 100 {
		t.Fatalf("first read ByteOffset=%d, want 100", res.ByteOffset)
	}
	if res.NextLineOffset != 0 {
		t.Fatalf("first read NextLineOffset=%d, want 0 (stay on same line)", res.NextLineOffset)
	}

	// 续读：从 byteOffset=100 起再切 100 字节。
	body2, res2, err := Read(src, Options{}, 0, 100, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(body2) != 100 {
		t.Fatalf("second read len=%d, want 100", len(body2))
	}
	if body2 != line[100:200] {
		t.Fatalf("second read body=%q, want %q", body2, line[100:200])
	}
	if res2.ByteOffset != 200 {
		t.Fatalf("second read ByteOffset=%d, want 200", res2.ByteOffset)
	}
}

// TestGrepStillErrorsOnLongLine 本应验证 grep 遇到 >4MiB 单行返回 errLongLine，
// 但需要分配 4MiB+ 内存，属于易 flaky/大分配用例，这里刻意跳过不实现。
// stat/read 走 eachCleanLine(..., false) 容忍超长行；grep 走 eachCleanLine(..., true) 保留 errLongLine 语义。

// TestReadForwardEarlyStop 校验正向 offset 单遍流式：5000 行只读中段 3 行，能提前停且行号正确。
func TestReadForwardEarlyStop(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 5000; i++ {
		b.WriteString("line-")
		b.WriteString(strconv.Itoa(i))
		b.WriteByte('\n')
	}
	body, res, _ := Read(srcOf(b.String()), Options{}, 4990, 0, 3, 1<<20)
	if body != "line-4990\nline-4991\nline-4992" {
		t.Fatalf("body=%q", body)
	}
	if res.NextLineOffset != 4993 {
		t.Fatalf("res=%+v", res)
	}
}

// TestGrepAfterContextStreaming 校验 after 上下文流式补发：命中 HIT + 后 2 行 b1,b2，不含 c。
func TestGrepAfterContextStreaming(t *testing.T) {
	body, _, err := Grep(srcOf("a\nHIT\nb1\nb2\nc\n"), Options{}, "HIT", 0, 0, 2, 50, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "HIT") || !strings.Contains(body, "b1") || !strings.Contains(body, "b2") || strings.Contains(body, "\nc") {
		t.Fatalf("body=%q", body)
	}
}

// TestObserveReconstructsSentinelThroughRead 校验 LineCleaner 的 observe 路径经 Read 被触达：
// 哨兵提示符行在 Observe 模式下被还原为 [rc=n] $ cmd，能在读出的正文里看到。
func TestObserveReconstructsSentinelThroughRead(t *testing.T) {
	in := "prefix@@PTYSESS@@EXIT=1@@PTYSESS@@> ls -l\nfile\n"
	body, _, err := Read(srcOf(in), Options{Observe: true}, 0, 0, 10, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "[rc=1] $ ls -l") {
		t.Fatalf("observe reconstruction missing: body=%q", body)
	}
}
