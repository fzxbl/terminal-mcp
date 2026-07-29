package session

import (
	"strings"
	"testing"

	"github.com/fzxbl/terminal-mcp/internal/config"
)

func TestBuildStartArgsLocal(t *testing.T) {
	config.Load("")
	name, _ := BuildStartArgs("local", "")
	if name == "" {
		t.Fatalf("local name empty")
	}
}

func TestBuildStartArgsLocalCommand(t *testing.T) {
	config.Load("")
	name, args := BuildStartArgs("local", "echo hi")
	if name != "sh" || len(args) != 2 || args[0] != "-c" || args[1] != "echo hi" {
		t.Fatalf("local command build wrong: %q %v", name, args)
	}
}

func TestBuildStartArgsSSH(t *testing.T) {
	c := config.Load("")
	c.SSHUser = "alice"
	name, args := BuildStartArgs("ssh", "host1")
	if name != "ssh" {
		t.Fatalf("ssh name = %q, want ssh", name)
	}
	found := false
	for _, a := range args {
		if a == "alice@host1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("args %v missing alice@host1", args)
	}
}

// TestBuildStartArgsSSHPrologue 验证配置了 ssh_login_prologue 时，ssh 会话以
// sh -c '<prologue> </dev/null && exec "$@"' sh ssh <opts...> <target> <remoteRun> 启动：
// 本地先跑鉴证前置命令、再 exec 进 ssh，host 作为位置参数透传（不被 shell 二次解析）。
func TestBuildStartArgsSSHPrologue(t *testing.T) {
	c := config.Load("")
	c.SSHUser = "op_stability"
	oldProl := c.SSHLoginPrologue
	c.SSHLoginPrologue = "baas login --baas_user=op_stability"
	defer func() { c.SSHLoginPrologue = oldProl }()

	name, args := BuildStartArgs("ssh", "host1")
	if name != "sh" {
		t.Fatalf("name=%q, want sh", name)
	}
	if len(args) < 6 || args[0] != "-c" {
		t.Fatalf("unexpected args: %v", args)
	}
	if !strings.Contains(args[1], "baas login --baas_user=op_stability") ||
		!strings.Contains(args[1], "</dev/null && exec \"$@\"") {
		t.Fatalf("inner script wrong: %q", args[1])
	}
	// 位置参数布局：[-c, inner, sh(=$0), ssh(=$1), opts..., target, remoteRun]
	if args[2] != "sh" || args[3] != "ssh" {
		t.Fatalf("positional layout wrong: %v", args)
	}
	if args[len(args)-2] != "op_stability@host1" {
		t.Fatalf("target not passed as positional arg: %v", args)
	}
	if !strings.Contains(args[len(args)-1], "bash --norc --noprofile") {
		t.Fatalf("remote run cmd missing: %v", args)
	}
}

// TestOpenLocalDisabled 验证 disable_local_mode=true 时 mode=local 被拒；ssh 的入参校验不受影响。
func TestOpenLocalDisabled(t *testing.T) {
	c := config.Load("")
	c.DisableLocalMode = true
	defer func() { c.DisableLocalMode = false }()

	if _, err := Open("local", "", "", ""); err == nil {
		t.Fatal("expected error when local mode disabled")
	}
	if _, err := Open("ssh", "", "", ""); err == nil {
		t.Fatal("ssh without host should still error")
	}
}
