package oplog

import (
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func openTmp(t *testing.T, cacheBytes int) *Log {
	t.Helper()
	l, err := Open(filepath.Join(t.TempDir(), "s.raw"), cacheBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func TestAppendLenAndReadRange(t *testing.T) {
	l := openTmp(t, 1024)
	if _, err := l.Append([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append([]byte("world")); err != nil {
		t.Fatal(err)
	}
	if l.Len() != 10 {
		t.Fatalf("Len=%d want 10", l.Len())
	}
	if got, _ := l.ReadRange(0, 10); string(got) != "helloworld" {
		t.Fatalf("full=%q", got)
	}
	if got, _ := l.ReadRange(5, 10); string(got) != "world" {
		t.Fatalf("tail=%q", got)
	}
}

func TestReadRangeColdFromFile(t *testing.T) {
	l := openTmp(t, 8) // 极小缓存，强制淘汰
	l.Append([]byte("0123456789ABCDEF"))
	if l.Len() != 16 {
		t.Fatalf("Len=%d", l.Len())
	}
	if got, _ := l.ReadRange(0, 4); string(got) != "0123" {
		t.Fatalf("cold=%q", got)
	}
	if got, _ := l.ReadRange(2, 12); string(got) != "23456789AB" {
		t.Fatalf("span=%q", got)
	}
	got, err := l.ReadRange(-5, 999)
	if err != nil {
		t.Fatalf("clamp err: %v", err)
	}
	if string(got) != "0123456789ABCDEF" {
		t.Fatalf("clamp=%q", got)
	}
}

func TestTail(t *testing.T) {
	l := openTmp(t, 1024)
	l.Append([]byte(strings.Repeat("x", 100) + "END"))
	if got := l.Tail(3); string(got) != "END" {
		t.Fatalf("tail=%q", got)
	}
	if got := l.Tail(1000); len(got) != 103 {
		t.Fatalf("tail overshoot len=%d", len(got))
	}
}

func TestConcurrentAppendRead(t *testing.T) {
	l := openTmp(t, 64)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 2000; i++ {
			l.Append([]byte("abcd"))
		}
		close(done)
	}()
	for {
		select {
		case <-done:
			if l.Len() != 8000 {
				t.Fatalf("Len=%d want 8000", l.Len())
			}
			return
		default:
			end := l.Len()
			if end > 8 {
				if _, err := l.ReadRange(end-8, end); err != nil {
					t.Fatalf("read err: %v", err)
				}
			}
		}
	}
}

func TestReadRangeAfterCloseNoPanic(t *testing.T) {
	l := openTmp(t, 64)
	l.Append([]byte("data"))
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	// 关闭后读取应安全返回空，不 panic
	if got, err := l.ReadRange(0, 4); err != nil || len(got) != 0 {
		t.Fatalf("after close: got=%q err=%v", got, err)
	}
	if got := l.Tail(10); len(got) != 0 {
		t.Fatalf("after close tail: %q", got)
	}
}

func TestAppendAfterCloseErrors(t *testing.T) {
	l := openTmp(t, 64)
	l.Append([]byte("abc"))
	before := l.Len()
	_ = l.Close()
	if _, err := l.Append([]byte("xyz")); err == nil {
		t.Fatal("append after close should error")
	}
	if l.Len() != before {
		t.Fatalf("Len changed after failed append: %d want %d", l.Len(), before)
	}
}

func TestRangeReaderStreamsScopeOnly(t *testing.T) {
	l := openTmp(t, 4096)
	l.Append([]byte("0123456789abcdef"))
	r := l.RangeReader(3, 10)
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "3456789" {
		t.Fatalf("got %q want %q", got, "3456789")
	}
}

func TestRangeReaderFixedEndUnderAppend(t *testing.T) {
	l := openTmp(t, 4096)
	l.Append([]byte("aaaa"))
	r := l.RangeReader(0, 4)
	l.Append([]byte("bbbb"))
	got, _ := io.ReadAll(r)
	if string(got) != "aaaa" {
		t.Fatalf("got %q want aaaa", got)
	}
}

func TestRangeReaderSingleFDColdAndHot(t *testing.T) {
	l := openTmp(t, 8) // tiny cache forces cold path
	l.Append([]byte("0123456789abcdefghij"))
	r := l.RangeReader(2, 15)
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "23456789abcde" {
		t.Fatalf("got %q", got)
	}
}
