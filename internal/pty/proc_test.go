package pty

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProcEchoAndSnapshot(t *testing.T) {
	p, err := NewProcSession(filepath.Join(t.TempDir(), "s.raw"), 1<<20, "bash", "--norc", "--noprofile")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.Write("echo hi\n")
	time.Sleep(300 * time.Millisecond)
	if !strings.Contains(p.Since(0), "hi") {
		t.Fatalf("buffer missing output: %q", p.Since(0))
	}
	if p.Len() == 0 {
		t.Fatal("len 0")
	}
	if time.Since(p.LastByteTime()) > time.Second {
		t.Fatal("lastByteTime not updated")
	}
}

func TestProcCloseKillsChildTree(t *testing.T) {
	p, err := NewProcSession(filepath.Join(t.TempDir(), "s.raw"), 1<<20, "bash", "--norc", "--noprofile")
	if err != nil {
		t.Fatal(err)
	}
	pgid := p.cmd.Process.Pid // Setpgid 令 pgid==pid
	p.Write("sleep 300\n")    // 前台孙进程，应随整组一起被杀
	time.Sleep(300 * time.Millisecond)
	// 关闭前进程组应存在
	if err := syscall.Kill(-pgid, syscall.Signal(0)); err != nil {
		t.Fatalf("process group not alive before close: %v", err)
	}
	p.Close()
	// 关闭后进程组应整体消失（ESRCH）
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-pgid, syscall.Signal(0)); err == syscall.ESRCH {
			return // 整组已回收，无遗留
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("process group still alive after close: leaked child process")
}

func TestProcFlushInput(t *testing.T) {
	p, err := NewProcSession(filepath.Join(t.TempDir(), "s.raw"), 1<<20, "bash", "--norc", "--noprofile")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.Write("echo \"unterminated")
	time.Sleep(100 * time.Millisecond)
	if err := p.FlushInput(); err != nil {
		t.Fatalf("flushInput: %v", err)
	}
	p.KillLine()
	p.Write("\n")
	off := p.Len()
	p.Write("echo clean\n")
	time.Sleep(300 * time.Millisecond)
	if !strings.Contains(p.Since(off), "clean") {
		t.Fatalf("session not clean after flush: %q", p.Since(off))
	}
}

func TestProcTranscriptWriteThrough(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "t.raw")
	p, err := NewProcSession(logPath, 1<<20, "bash", "--norc", "--noprofile")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.Write("echo hi\n")
	time.Sleep(300 * time.Millisecond)
	b, _ := os.ReadFile(logPath)
	if !strings.Contains(string(b), "hi") {
		t.Fatalf("transcript missing output: %q", string(b))
	}
}
