package clean

import "testing"

func TestCleanStripsSentinelPromptAndEcho(t *testing.T) {
	raw := "@@PTYSESS@@EXIT=0@@PTYSESS@@> echo hi\nhi\n@@PTYSESS@@EXIT=0@@PTYSESS@@> "
	got := CleanOutput(raw, "echo hi")
	if got != "hi" {
		t.Fatalf("got %q", got)
	}
}

func TestObserveKeepsTypedCommands(t *testing.T) {
	// 人工接管观察：人敲的命令要能被看到，还原成 "[rc=n] $ 命令"。
	raw := "@@PTYSESS@@EXIT=0@@PTYSESS@@> pwd\n/home/u\n" +
		"@@PTYSESS@@EXIT=0@@PTYSESS@@> whoami\nu\n@@PTYSESS@@EXIT=0@@PTYSESS@@> "
	got := CleanOutputObserve(raw)
	want := "[rc=0] $ pwd\n/home/u\n[rc=0] $ whoami\nu"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestObserveShowsExitCodeAndDropsBarePrompt(t *testing.T) {
	// 上一条非零退出码要带出；光提示符（无命令）丢弃。
	if got := CleanOutputObserve("@@PTYSESS@@EXIT=2@@PTYSESS@@> "); got != "" {
		t.Fatalf("bare prompt should be dropped, got %q", got)
	}
	if got := CleanOutputObserve("@@PTYSESS@@EXIT=1@@PTYSESS@@> ls /nope"); got != "[rc=1] $ ls /nope" {
		t.Fatalf("got %q", got)
	}
}

func TestObserveOrCleanGatesOnHeld(t *testing.T) {
	raw := "@@PTYSESS@@EXIT=0@@PTYSESS@@> id\noutput\n@@PTYSESS@@EXIT=0@@PTYSESS@@> "
	if got := ObserveOrClean(raw, false); got != "output" {
		t.Fatalf("non-held should drop command, got %q", got)
	}
	if got := ObserveOrClean(raw, true); got != "[rc=0] $ id\noutput" {
		t.Fatalf("held should keep command, got %q", got)
	}
}

func TestCleanStripsANSIAndCRLF(t *testing.T) {
	if got := CleanOutput("\x1b[31mred\x1b[0m\r\nplain\r\n", ""); got != "red\nplain" {
		t.Fatalf("got %q", got)
	}
}

func TestCleanStripsPromptPrefixedEcho(t *testing.T) {
	if got := CleanOutput("(gdb) bt\nframe0\nframe1\n", "bt"); got != "frame0\nframe1" {
		t.Fatalf("got %q", got)
	}
}

func TestCleanStripsOSC(t *testing.T) {
	if got := CleanOutput("\x1b]0;title\x07data\n", ""); got != "data" {
		t.Fatalf("got %q", got)
	}
}

func TestCleanEmptyInputKeepsContent(t *testing.T) {
	if got := CleanOutput("line1\nline2\n", ""); got != "line1\nline2" {
		t.Fatalf("got %q", got)
	}
}

func TestCleanStripsColonTruecolorAndBracketedPaste(t *testing.T) {
	// colon 形真彩色 SGR + 括号粘贴 DECSET，二者旧正则都漏剥。
	raw := "\x1b[38:2::255:0:0mred\x1b[0m\x1b[?2004hplain"
	if got := CleanOutput(raw, ""); got != "redplain" {
		t.Fatalf("got %q", got)
	}
}

func TestCleanStripsDCSAndCharsetAndBareEsc(t *testing.T) {
	// DCS ... ST、字符集切换 \x1b(B、以及裸 ESC/控制字符，均应清除。
	raw := "\x1bPq;body\x1b\\\x1b(Bhello\x1bworld\x07"
	if got := CleanOutput(raw, ""); got != "helloworld" {
		t.Fatalf("got %q", got)
	}
}

func TestPendingEscBoundaryHoldsSplitSequences(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int // 期望的可交付边界
	}{
		{"no esc", "plain text", len("plain text")},
		{"complete sgr at end", "x\x1b[0m", len("x\x1b[0m")},
		{"complete sgr then text", "\x1b[31mred", len("\x1b[31mred")},
		{"split truecolor csi", "green\x1b[38;2;25", len("green")},
		{"split 256 csi", "a\x1b[38;5", len("a")},
		{"bare trailing esc", "hello\x1b", len("hello")},
		{"split bracketed paste", "done\x1b[?2004", len("done")},
		{"split osc", "t\x1b]0;title", len("t")},
		{"complete osc bel", "\x1b]0;t\x07", len("\x1b]0;t\x07")},
		{"complete fe two-char", "x\x1bM", len("x\x1bM")},
		{"complete then split", "\x1b[0mok\x1b[1;3", len("\x1b[0mok")},
	}
	for _, c := range cases {
		if got := PendingEscBoundary(c.in); got != c.want {
			t.Errorf("%s: PendingEscBoundary(%q)=%d want %d", c.name, c.in, got, c.want)
		}
	}
}

func TestDeliverBoundaryFlushesWhenNotRunning(t *testing.T) {
	raw := "green\x1b[38;2;25" // 末尾半截颜色
	if got := DeliverBoundary(raw, "running"); got != len("green") {
		t.Fatalf("running: got %d", got)
	}
	if got := DeliverBoundary(raw, "idle"); got != len(raw) {
		t.Fatalf("idle: got %d", got)
	}
}
