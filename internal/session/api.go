package session

import (
	"fmt"
	"net"
	"strconv"
	"sync/atomic"

	"github.com/fzxbl/terminal-mcp/internal/config"
)

// 本文件为 mcpserver 提供稳定的导出访问面：所有 MCP 工具只经这些薄包装调用会话逻辑，
// 不直接触碰 Session 的非导出字段、theStore 或 dispatch 等内部符号。
// Send/Read/Control/Status/Close/Envelope/BuildStartArgs 已在 session.go 中导出，直接复用。

// advertiseAddr 覆盖 terminal_url 的 host:port（嵌入宿主时用）；未设置则回退 listen_addr。
var advertiseAddr atomic.Value // string

// SetAdvertiseAddr 覆盖 terminal_url 使用的 host:port。把本模块嵌入到别的 HTTP 服务里、
// 由宿主进程持有监听 socket（其地址/端口与本模块 listen_addr 不一致）时，用它指向宿主的
// 实际对外地址，terminal_url 才点得开。传空串恢复用 listen_addr 的默认行为。并发安全。
func SetAdvertiseAddr(hostPort string) {
	advertiseAddr.Store(hostPort)
	if currentNodeToken() == "" {
		SetNodeToken(hostPort)
	}
}

// terminalURL 拼只读终端页地址（TerminalHandler 提供该页面）。优先用 SetAdvertiseAddr 设定的
// host:port；否则回退 listen_addr。host 为通配（0.0.0.0/::/空）时用本机实际 IP 替换，保证可点。
func terminalURL(id string) string {
	a, _ := advertiseAddr.Load().(string)
	if a == "" {
		a = config.Get().ListenAddr
	}
	if a == "" {
		return ""
	}
	return "http://" + urlHostPort(a) + "/debug/terminal/" + id
}

// urlHostPort 把 listen_addr 归一为可访问的 host:port：通配 host 换成本机 IP，其余原样返回。
func urlHostPort(listenAddr string) string {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return listenAddr // 非 host:port 形态，原样返回
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		if ip := localIP(); ip != "" {
			host = ip
		}
	}
	return net.JoinHostPort(host, port)
}

// ReachableHostPort 由监听地址推导出"兄弟节点可直连的本机地址"，供分布式部署自动确定属主节点 token，
// 免去每实例配不同地址：把 listen_addr 里的通配 host（空/0.0.0.0/::）替换为进程自动探测到的本机 IP，
// 其余（如显式 IP 或 127.0.0.1）原样返回。这样所有实例可共用同一份 `listen_addr = "0.0.0.0:<port>"` 配置，
// 每个进程各自解析出自己的可达地址。跨 NAT/需对外映射地址时，仍可用 SetAdvertiseAddr 显式覆盖。
func ReachableHostPort(listenAddr string) string { return urlHostPort(listenAddr) }

// localIP 纯本地探测本机首选 IPv4：枚举已启用、非回环网卡的地址，取第一个全局单播 IPv4。
// 不连接任何外部地址。都拿不到返回空。
func localIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			v4 := ipn.IP.To4()
			if v4 != nil && ipn.IP.IsGlobalUnicast() && !ipn.IP.IsLoopback() {
				return v4.String()
			}
		}
	}
	return ""
}

// Open 起持久真 PTY 会话，返回内嵌会话 id 与网页终端地址 terminal_url。
// mode 必须为 local 或 ssh；ssh 模式必须给 host。
//
//	mode=local: 起本地 shell，command 为可选自定义命令（空则默认 shell）
//	mode=ssh:   ssh 到 host 上起 bash，command 忽略
//
// 用 Status 轮询到 idle/ready 后再交互；terminal_url 供人在浏览器观看并可"人工接管"。
//
// Open 起持久真 PTY 会话。owner 为调用方归属签名（见 internal/identity），写入会话用于归属隔离。
func Open(mode, command, host, owner string) (map[string]string, error) {
	switch mode {
	case "local", "ssh":
	default:
		return nil, fmt.Errorf("invalid mode %q: must be \"local\" or \"ssh\"", mode)
	}
	if mode == "local" && config.Get().DisableLocalMode {
		return nil, fmt.Errorf("local mode is disabled by config (disable_local_mode); use mode=ssh")
	}
	if mode == "ssh" && host == "" {
		return nil, fmt.Errorf("mode=ssh requires a non-empty host")
	}
	hostOrCommand := command
	if mode == "ssh" {
		hostOrCommand = host
	}
	name, args := BuildStartArgs(mode, hostOrCommand)
	id := newSessionID()
	if _, err := startSessionTracked(id, host, mode, name, args); err != nil {
		return nil, err
	}
	if s := theStore.get(id); s != nil {
		s.Owner = owner
	}
	return map[string]string{
		"session_id":   id,
		"state":        "loading",
		"terminal_url": terminalURL(id),
	}, nil
}

// List 列出本实例中属于 owner 的会话及状态快照。owner 为空则不过滤（内部/兼容用途）。
func List(owner string) []map[string]string {
	if theStore == nil {
		return nil
	}
	var out []map[string]string
	for _, s := range theStore.list() {
		if owner != "" && s.Owner != owner {
			continue
		}
		st, _ := s.snapshotStatus()
		held := "false"
		if s.held() {
			held = "true"
		}
		out = append(out, map[string]string{
			"session_id":   s.ID,
			"host":         s.Host,
			"status":       st,
			"idle_seconds": strconv.Itoa(int(s.idleSince().Seconds())),
			"held":         held,
		})
	}
	return out
}

// Owner 返回本机会话的归属签名；found=false 表示本机无此会话。
func Owner(id string) (owner string, found bool) {
	if theStore == nil {
		return "", false
	}
	s := theStore.get(id)
	if s == nil {
		return "", false
	}
	return s.Owner, true
}
