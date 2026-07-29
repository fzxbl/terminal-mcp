package textexplore

import (
	"io"
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
