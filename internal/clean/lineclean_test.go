package clean

import (
	"strings"
	"testing"
)

// splitKeepNoTrailing 按 '\n' 把原始串切成物理行（strings.Split(raw, "\n")）。
// LineCleaner.Clean 内部会对每条物理行自行 StripANSI（含 CRLF 归一），因此这里无需预处理。
func splitKeepNoTrailing(raw string) []string {
	return strings.Split(raw, "\n")
}

// streamClean 用 LineCleaner 逐行处理，模拟 textexplore 的用法，产出与 CleanOutput 对齐的整串。
func streamClean(raw, input string, observe bool) string {
	lc := NewLineCleaner(input, observe)
	var out []string
	for _, ln := range splitKeepNoTrailing(raw) { // 按 '\n' 切分原始物理行
		s, keep := lc.Clean(ln)
		if keep {
			out = append(out, s)
		}
	}
	out = lc.Flush(out) // 处理尾随缓冲空行的丢弃
	return strings.Join(out, "\n")
}

func eq(t *testing.T, raw, input string, observe bool) {
	t.Helper()
	var want string
	if observe {
		want = ObserveOrClean(raw, true)
	} else {
		want = CleanOutput(raw, input)
	}
	got := streamClean(raw, input, observe)
	if got != want {
		t.Fatalf("mismatch\n raw=%q\n want=%q\n got =%q", raw, want, got)
	}
}

func TestLineCleanEquivalence(t *testing.T) {
	cases := []struct {
		raw, input string
		observe    bool
	}{
		// --- plan 基础用例 ---
		{"\x1b[31mred\x1b[0m\nplain\n", "", false},                      // ANSI 去色
		{"echo hi\nhi\n", "echo hi", false},                             // echo strip 首行
		{"\n\n\nfoo\n\n\n", "", false},                                  // 首尾空行 Trim
		{"a\r\nb\r\n", "", false},                                       // CRLF
		{"@@PTYSESS@@EXIT=0@@PTYSESS@@> \nout\n", "", false},            // 哨兵行丢弃
		{"prefix@@PTYSESS@@EXIT=1@@PTYSESS@@> ls -l\nfile\n", "", true}, // observe 还原
		{"only sentinel\n@@PTYSESS@@EXIT=0@@PTYSESS@@> \n", "", true},   // observe 光提示符丢弃
		{"", "", false},
		{"no newline tail", "", false},

		// --- 硬化：内部空行在内容之间保留 ---
		{"a\n\nb\n", "", false},
		{"a\n\n\nb\nc\n", "", false},
		{"head\n\ntail", "", false},

		// --- 硬化：多条回显样式行，只剥第一条 ---
		{"cmd\ncmd\ncmd\n", "cmd", false},
		{"$ ls\nls\nfoo\n", "ls", false}, // input 为后缀（HasSuffix 分支）
		{"run\nrun\n", "run", false},     // input 等于某行

		// --- 硬化：input 等于某行 vs input 为后缀 ---
		{"exactly\nafter\n", "exactly", false},      // 等于
		{"prompt> deploy\nnext\n", "deploy", false}, // 后缀

		// --- 硬化：observe 多个哨兵提示符 + 交错输出 ---
		{"out1\n@@PTYSESS@@EXIT=0@@PTYSESS@@> cmd1\nout2\n@@PTYSESS@@EXIT=2@@PTYSESS@@> cmd2\nout3\n", "", true},
		{"pre@@PTYSESS@@EXIT=0@@PTYSESS@@> a\nmid\npre2@@PTYSESS@@EXIT=3@@PTYSESS@@> b\n", "", true},

		// --- 硬化：内容后出现尾随哨兵行 ---
		{"data\n@@PTYSESS@@EXIT=0@@PTYSESS@@> \n", "", false},
		{"line1\nline2\n@@PTYSESS@@EXIT=0@@PTYSESS@@> \n", "", false},
		{"content\n@@PTYSESS@@EXIT=0@@PTYSESS@@> exit\n", "", true}, // observe 下尾随含命令

		// --- 硬化：纯空行输入、纯哨兵输入 ---
		{"\n\n\n", "", false},
		{"\n\n\n", "", true},
		{"@@PTYSESS@@EXIT=0@@PTYSESS@@> \n", "", false},
		{"@@PTYSESS@@EXIT=0@@PTYSESS@@> \n", "", true},
		{"@@PTYSESS@@EXIT=0@@PTYSESS@@> \n@@PTYSESS@@EXIT=0@@PTYSESS@@> \n", "", false},

		// --- 硬化：CRLF 各种组合 + ANSI + 首尾空行 ---
		{"\r\n", "", false},
		{"a\r\r\n", "", false},
		{"\r\na\r\nb\r\n\r\n", "", false},
		{"\x1b[32m$ ls\x1b[0m\r\nfile1\r\nfile2\r\n", "ls", false}, // ANSI + CRLF + echo 后缀
		{"a\rb\rc", "", false},                                     // 裸 CR 拆行
	}
	for i, c := range cases {
		c := c
		t.Run(string(rune('A'+i)), func(t *testing.T) { eq(t, c.raw, c.input, c.observe) })
	}
}
