package config

import (
	"path/filepath"
	"sync"

	"github.com/BurntSushi/toml"
)

type Config struct {
	DataDir                 string   `toml:"data_dir"`
	ListenAddr              string   `toml:"listen_addr"`
	SSHUser                 string   `toml:"ssh_user"`
	DefaultShell            string   `toml:"default_shell"`
	MaxSessions             int      `toml:"max_sessions"`
	IdleTTLMinutes          int      `toml:"idle_ttl_minutes"`
	ExecOutputMaxBytes      int64    `toml:"exec_output_max_bytes"`
	MaxBufferBytes          int      `toml:"max_buffer_bytes"`
	OpenReadyTimeoutMinutes int      `toml:"open_ready_timeout_minutes"`
	SSHOpts                 string   `toml:"ssh_opts"`
	MaxBlockSeconds         int      `toml:"max_block_seconds"`
	QuietWindowMs           int      `toml:"quiet_window_ms"`
	TailBytes               int      `toml:"tail_bytes"`

	// explore（mode=explore）服务端硬上限：调用方传入更大值会被 clamp，不能扩大单次 MCP 返回体积。
	ExploreMaxBytesHard  int64 `toml:"explore_max_bytes_hard"`  // explore 正文单次返回硬上限，默认 128 KiB
	ExploreReadLimitHard int   `toml:"explore_read_limit_hard"` // read 行数上限，默认 1000
	ExploreGrepLimitHard int   `toml:"explore_grep_limit_hard"` // grep 匹配数上限，默认 500
	ExploreCtxHard       int   `toml:"explore_ctx_hard"`        // grep before/after 各自上限，默认 20
	InitCommands            []string `toml:"init_commands"`
	DefaultFont             string   `toml:"default_font"`
	DefaultFontSize         int      `toml:"default_font_size"`
	DefaultTheme            string   `toml:"default_theme"`
	DefaultRenderer         string   `toml:"default_renderer"`
	TranscriptDir           string   `toml:"transcript_dir"`
	TranscriptRetentionDays int      `toml:"transcript_retention_days"`
	AuditLog                string   `toml:"audit_log"`

	// 日志目录与切割/保存策略（审计日志与服务运行日志都落到 LogDir 下）。
	LogDir        string `toml:"log_dir"`          // 默认 <data_dir>/logs
	LogRotate     string `toml:"log_rotate"`       // 切割周期：hourly | daily，默认 daily（按时间切割，不按大小）
	LogMaxAgeDays int    `toml:"log_max_age_days"` // 旧日志保存周期（天），默认 30；<=0 表示永久保留

	// ShellSwitchCommands 是"会切进新一层 shell"的命令注册表（明文、可追加）。命中且哨兵丢失时触发自动布哨。
	ShellSwitchCommands []string `toml:"shell_switch_commands"`
	// AutoRearm 控制是否在命中切换命令后自动重新布哨。默认 true；置 false 仅保留手动 terminal_control(rearm)。
	// bool 默认真的实现见 Load：DecodeFile 前先置 true，缺省保持 true，显式 auto_rearm=false 可覆盖。
	AutoRearm bool `toml:"auto_rearm"`

	// ToolDescriptions 允许按工具名覆盖 MCP 工具的对外描述（key 为工具名，如 terminal_open/terminal_send/
	// terminal_read/terminal_control/terminal_status/terminal_close/terminal_list）。
	// 缺省或空串的条目沿用内置默认描述；集成到外部 MCP、需要按自家话术改写工具说明时用它。
	ToolDescriptions map[string]string `toml:"tool_descriptions"`

	// SSHLoginPrologue 可选，默认留空。它是 ssh 前在本地 shell 执行的一条命令，常用于鉴权登录
	// （凭证需与 ssh 同会话时）。需要多条命令用 && 连接即可——整体被包在
	// `sh -c '{ <prologue>; } && exec ssh ...'` 里，成功后 exec 顶替进程、不残留本地交互 shell，
	// 前置失败则短路退出结束会话，均不会逃逸到本地 shell。留空则 ssh 直连。
	SSHLoginPrologue string `toml:"ssh_login_prologue"`

	// ResourceLimitCmd 可选，默认留空。它是会话启动后对模型「透明」注入的一条资源限制命令，
	// 通常是 ulimit（如 "ulimit -v 4194304; ulimit -t 600; ulimit -u 4096"）。
	// 它随哨兵一起写在初始化脚本最前面，且在每次「切进新一层 shell」的自动/手动重新布哨（rearm）、
	// hard reset 时都会重新注入，因此：
	//   - 启动时即作用于会话根 shell，被其所有子进程继承（ulimit 无 -S/-H 时同时设软硬限，硬限一经设定
	//     非特权进程无法再调高，故本地子 shell / 普通命令即便再 `ulimit` 也无法逃逸出该上限）；
	//   - 切进 ssh 远端 / su / docker exec / matrix_jail 等新 shell（见 shell_switch_commands）后由 rearm
	//     在新上下文里重新注入，把限制带进跨机/跨容器/跨用户的新 shell。
	// 边界（诚实告知）：ulimit 无法约束「提权到 root 后主动调高自身硬限」，也无法约束「由守护进程另起、
	// 不继承本会话的进程树」（如 docker run 经 dockerd 拉起的容器）。要对本机进程树做与权限无关的强约束，
	// 应改用 cgroup v2（memory.max/pids.max/cpu.max）把 PTY 首进程整棵树关进 cgroup——本字段不覆盖该场景。
	// 注入内容与哨兵同属布哨噪声，被 since_last 游标跳过，模型侧 terminal_read 看不到。
	ResourceLimitCmd string `toml:"resource_limit_cmd"`

	// DisableLocalMode 是「可选」加固，默认 false。为 true 时禁止 mode=local 开会话（terminal_open 直接
	// 返回错误）。仅在集成到外部 MCP、不希望暴露 MCP 宿主机本地 shell 时按需开启；可与 ssh_login_prologue
	// 搭配，仅允许 ssh 会话直达远端。
	DisableLocalMode bool `toml:"disable_local_mode"`

	// Peers 是兄弟节点对外可达地址列表（host:port），仅用于 terminal_list 跨节点聚合（fan-out）。
	// 反代路由不需要它——属主地址已编码在 session_id 内。单机部署留空即可。
	Peers []string `toml:"peers"`

	// Identity 定义"如何从请求头识别同一个客户端"。归属键 = 按 Headers 顺序取值拼接后按 Mode 处理。
	Identity IdentityConfig `toml:"identity"`
}

// IdentityConfig 客户端身份签名配置。
type IdentityConfig struct {
	// Headers 参与签名的 HTTP header 名（有序）。默认 ["X-MCP-USER"]。
	Headers []string `toml:"headers"`
	// Mode 签名算法：raw（拼接原值）| sha256（对拼接值取十六进制哈希）。默认 raw。
	Mode string `toml:"mode"`
	// OnMissing 任一 header 缺失时的行为：reject（拒绝调用）| allow_empty（缺失位视为空串）。默认 reject。
	OnMissing string `toml:"on_missing"`
}

func (c *Config) applyDefaults() {
	if c.DataDir == "" {
		c.DataDir = "./data"
	}
	if c.ListenAddr == "" {
		c.ListenAddr = "127.0.0.1:8900"
	}
	if c.MaxBufferBytes <= 0 {
		c.MaxBufferBytes = 8 << 20 // 8 MiB 尾部缓存上限
	}
	if c.DefaultShell == "" {
		c.DefaultShell = "bash"
	}
	if c.MaxSessions <= 0 {
		c.MaxSessions = 2
	}
	if c.IdleTTLMinutes <= 0 {
		c.IdleTTLMinutes = 30
	}
	if c.ExecOutputMaxBytes <= 0 {
		c.ExecOutputMaxBytes = 1 << 20
	}
	if c.OpenReadyTimeoutMinutes <= 0 {
		c.OpenReadyTimeoutMinutes = 20
	}
	if c.SSHOpts == "" {
		c.SSHOpts = "-tt -o LogLevel=error -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=4"
	}
	if c.MaxBlockSeconds <= 0 {
		c.MaxBlockSeconds = 30
	}
	if c.QuietWindowMs <= 0 {
		c.QuietWindowMs = 300
	}
	if c.TailBytes <= 0 {
		c.TailBytes = 64 * 1024
	}
	if c.ExploreMaxBytesHard <= 0 {
		c.ExploreMaxBytesHard = 128 << 10
	}
	if c.ExploreReadLimitHard <= 0 {
		c.ExploreReadLimitHard = 1000
	}
	if c.ExploreGrepLimitHard <= 0 {
		c.ExploreGrepLimitHard = 500
	}
	if c.ExploreCtxHard <= 0 {
		c.ExploreCtxHard = 20
	}
	if c.DefaultFont == "" {
		c.DefaultFont = "consolas"
	}
	if c.DefaultFontSize <= 0 {
		c.DefaultFontSize = 15
	}
	if c.DefaultTheme == "" {
		c.DefaultTheme = "catppuccin-mocha"
	}
	if c.DefaultRenderer == "" {
		c.DefaultRenderer = "dom"
	}
	if c.TranscriptDir == "" {
		c.TranscriptDir = filepath.Join(c.DataDir, "transcripts")
	}
	if c.TranscriptRetentionDays <= 0 {
		c.TranscriptRetentionDays = 7
	}
	if c.LogDir == "" {
		c.LogDir = "log"
	}
	if c.LogRotate != "hourly" && c.LogRotate != "daily" {
		c.LogRotate = "daily"
	}
	if c.LogMaxAgeDays <= 0 {
		c.LogMaxAgeDays = 30
	}
	if len(c.ShellSwitchCommands) == 0 {
		c.ShellSwitchCommands = []string{
			"ssh", "su", "sudo -i", "sudo su",
			"docker exec", "kubectl exec", "nsenter", "chroot",
		}
	}
	// 注意：AutoRearm 不在此处理——其默认真在 Load() 里 DecodeFile 前置位，避免零值 false 覆盖默认。
	if len(c.Identity.Headers) == 0 {
		c.Identity.Headers = []string{"X-MCP-USER"}
	}
	if c.Identity.Mode != "raw" && c.Identity.Mode != "sha256" {
		c.Identity.Mode = "raw"
	}
	if c.Identity.OnMissing != "reject" && c.Identity.OnMissing != "allow_empty" {
		c.Identity.OnMissing = "reject"
	}
	if c.Peers == nil {
		c.Peers = []string{}
	}
}

var (
	cfg     Config
	cfgOnce sync.Once
)

// Load 从指定路径解析配置（缺失用默认值，不 panic），幂等。
func Load(path string) *Config {
	cfgOnce.Do(func() {
		// AutoRearm 默认 true：先置 true 再 DecodeFile，缺省 key 保持 true，
		// 显式 auto_rearm=false 会被 DecodeFile 覆盖成 false。
		cfg.AutoRearm = true
		if path != "" {
			_, _ = toml.DecodeFile(path, &cfg)
		}
		cfg.applyDefaults()
	})
	return &cfg
}

// Get 返回已加载配置（须先 Load）。
func Get() *Config { return &cfg }
