package logging

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRotatorWritesTimestampedFile(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, "audit", "daily", 30)
	defer r.Close()
	if _, err := r.Write([]byte("hello\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	want := filepath.Join(dir, "audit."+time.Now().Format("20060102")+".log")
	b, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("expected file %s: %v", want, err)
	}
	if string(b) != "hello\n" {
		t.Fatalf("content=%q", string(b))
	}
}

func TestRotatorHourlyLayout(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, "terminal-mcp", "hourly", 0)
	defer r.Close()
	r.Write([]byte("x"))
	want := filepath.Join(dir, "terminal-mcp."+time.Now().Format("2006010215")+".log")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected hourly file %s: %v", want, err)
	}
}

func TestRotatorCleanupRemovesOld(t *testing.T) {
	dir := t.TempDir()
	// 预置一个"旧"日志文件（同前缀），mtime 设为很久以前。
	old := filepath.Join(dir, "audit.20000101.log")
	if err := os.WriteFile(old, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	// 保存周期 1 天：写入触发切桶清理，旧文件应被删。
	r := New(dir, "audit", "daily", 1)
	defer r.Close()
	r.Write([]byte("new\n"))
	// cleanup 异步，给它一点时间。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(old); os.IsNotExist(err) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("old log %s was not cleaned up", old)
}
