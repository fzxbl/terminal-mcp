package session

import (
	"testing"

	"github.com/fzxbl/terminal-mcp/internal/config"
)

// TestMatchesShellSwitch 覆盖注册表匹配的正例与误命中防护（item 2）。
func TestMatchesShellSwitch(t *testing.T) {
	config.Load("")
	orig := config.Get().ShellSwitchCommands
	// 临时把 matrix_jail 加进注册表，验证可追加的自定义项；结束后还原。
	config.Get().ShellSwitchCommands = append([]string{"matrix_jail"}, orig...)
	defer func() { config.Get().ShellSwitchCommands = orig }()

	cases := []struct {
		in   string
		want bool
	}{
		{"matrix_jail x", true},
		{"ssh host", true},
		{"cd /tmp && ssh h", true},
		{"grep ssh f", false},
		{"echo ssh", false},
		{"sudo -i", true},
		{"sudo su", true},
		{"su - root", true},
		{"docker exec -it c bash", true},
		{"FOO=bar ssh host", true},
		{"env FOO=bar ssh host", true},
		{"sudo ssh host", true},
		{"ls -l", false},
		{"", false},
	}
	for _, c := range cases {
		if got := matchesShellSwitch(c.in); got != c.want {
			t.Errorf("matchesShellSwitch(%q)=%v, want %v", c.in, got, c.want)
		}
	}
}

// TestLooksLikeShellPrompt 验证自动布哨前的"疑似 shell 提示符"安全闸：
// 只有真正停在 shell 提示符才返回 true；ssh 的密码/host-key 交互问询必须返回 false（否则会把 PS1 打进去）。
func TestLooksLikeShellPrompt(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"[root@host ~]# ", true},
		{"bash-5.1$ ", true},
		{"user@box:/tmp$ ", true},
		{"someshell> ", true},
		{"op_stability@njjs-ps-beehive-agent103049.njjs's password: ", false},
		{"Enter passphrase for key '/x/id_rsa': ", false},
		{"Are you sure you want to continue connecting (yes/no/[fingerprint])? ", false},
		{"Password: ", false},
		{"just some output line", false},
		{"", false},
	}
	for _, c := range cases {
		if got := looksLikeShellPrompt(c.in); got != c.want {
			t.Errorf("looksLikeShellPrompt(%q)=%v, want %v", c.in, got, c.want)
		}
	}
}
