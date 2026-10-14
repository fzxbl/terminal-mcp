package session

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/fzxbl/terminal-mcp/internal/config"
)

// runStartArgs 直接 exec BuildStartArgs 的产物并返回合并输出，用于验证「首进程启动时」的真实环境。
func runStartArgs(t *testing.T, command string) (string, error) {
	t.Helper()
	name, args := BuildStartArgs("local", command)
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// TestBuildStartArgsLocalCommandResourceLimit 回归：配置 resource_limit_cmd 时，local 自定义命令
// 必须在其第一条指令执行前就处于限额内（此前限额经 PTY 事后注入，自定义命令完全绕过）。
func TestBuildStartArgsLocalCommandResourceLimit(t *testing.T) {
	c := config.Load("")
	c.ResourceLimitCmd = "ulimit -n 137"
	defer func() { c.ResourceLimitCmd = "" }()

	out, err := runStartArgs(t, "echo s=$(ulimit -Sn) h=$(ulimit -Hn)")
	if err != nil || out != "s=137 h=137" {
		t.Fatalf("custom command must start under resource limit, got %q err=%v", out, err)
	}
	// 自定义命令按位置参数透传，不被包装脚本二次解析（含引号/$1 也原样执行）。
	out, err = runStartArgs(t, `printf '%s\n' "a'b" '$1'`)
	if err != nil || out != "a'b\n$1" {
		t.Fatalf("custom command altered by wrapper: %q err=%v", out, err)
	}
}

// TestBuildStartArgsLocalCommandResourceLimitBestEffort 与普通会话一致：单条限额失败不阻断，
// 其余限额照常生效，自定义命令仍会执行。
func TestBuildStartArgsLocalCommandResourceLimitBestEffort(t *testing.T) {
	c := config.Load("")
	c.ResourceLimitCmd = "ulimit -n bogus 2>/dev/null; ulimit -n 137"
	defer func() { c.ResourceLimitCmd = "" }()

	out, err := runStartArgs(t, "echo h=$(ulimit -Hn)")
	if err != nil || out != "h=137" {
		t.Fatalf("best-effort limits should still apply and command run, got %q err=%v", out, err)
	}
}
