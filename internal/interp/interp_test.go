package interp

import "testing"

func TestBashSentinelParse(t *testing.T) {
	buf := "some output\n@@PTYSESS@@EXIT=0@@PTYSESS@@> "
	interp, prompt, code, ok := DetectPromptAtTail(buf)
	if !ok || interp != "bash" || prompt != "" {
		t.Fatalf("bash detect: interp=%q prompt=%q ok=%v", interp, prompt, ok)
	}
	if code == nil || *code != 0 {
		t.Fatalf("exit code: %v", code)
	}
}

func TestBashSentinelNonZero(t *testing.T) {
	buf := "err\n@@PTYSESS@@EXIT=137@@PTYSESS@@> "
	_, _, code, ok := DetectPromptAtTail(buf)
	if !ok || code == nil || *code != 137 {
		t.Fatalf("code=%v ok=%v", code, ok)
	}
}

// TestBashSentinelColored 验证带色 PS1（\e[1;32m…\e[0m 包裹哨兵）经 StripANSI 后仍被正确识别。
// 模拟 bash 吃掉 \[ \] 后落到 PTY 的真实字节：SGR 码裸露，哨兵文本不变。
func TestBashSentinelColored(t *testing.T) {
	buf := "ok\n\x1b[1;32m@@PTYSESS@@EXIT=0@@PTYSESS@@>\x1b[0m "
	interp, prompt, code, ok := DetectPromptAtTail(buf)
	if !ok || interp != "bash" || prompt != "" || code == nil || *code != 0 {
		t.Fatalf("colored bash detect: interp=%q prompt=%q code=%v ok=%v", interp, prompt, code, ok)
	}
}

func TestGdbPromptDetect(t *testing.T) {
	buf := "Reading symbols...\n(gdb) "
	interp, prompt, code, ok := DetectPromptAtTail(buf)
	if !ok || interp != "gdb" || prompt != "(gdb) " || code != nil {
		t.Fatalf("gdb: interp=%q prompt=%q code=%v ok=%v", interp, prompt, code, ok)
	}
}

func TestPythonPromptDetect(t *testing.T) {
	if interp, _, _, ok := DetectPromptAtTail(">>> "); !ok || interp != "python" {
		t.Fatalf("python: interp=%q ok=%v", interp, ok)
	}
}

func TestNoPromptWhileRunning(t *testing.T) {
	if _, _, _, ok := DetectPromptAtTail("still working..."); ok {
		t.Fatal("should not detect prompt mid-output")
	}
}
