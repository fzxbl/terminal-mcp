package mcpserver

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/fzxbl/terminal-mcp/internal/session"
	"github.com/fzxbl/terminal-mcp/internal/terminal"
)

// 本文件是「宿主随便挂，只说一次」这件事：网页终端的挂载动作交给本模块，宿主只给一个前缀。
//
// 内部路径（/view/terminal/、StripPrefix 掉 /view、外层的会话路由中间件）与 terminal_url
// 里的前缀都由本模块据这一个前缀推导，宿主不必知道这些细节，也就不会因为漏做一步而在
// 多节点部署下出现「terminal_url 点开是 404 或『会话不存在』」。

// MountWebTerminal 把网页终端（人工接管 UI，含 SSE 与 WebSocket）挂到宿主的 router 上，
// 同时记下宿主给的挂载前缀，供 terminal_url 与跨节点转发推导实际路径。
//
// prefix 是本模块在宿主里的挂载前缀（如 "/mcp"；空串表示挂在根上），必须以 "/" 开头、
// 不以 "/" 结尾、不含通配符与空白。mount 按宿主自己 router 的写法把这条路由挂上去：
//
//	// net/http
//	mcpserver.MountWebTerminal("/mcp", func(pattern string, h http.Handler) {
//		mux.Handle(pattern, h)
//	})
//	// 需要通配符的 router
//	mcpserver.MountWebTerminal("/mcp", func(pattern string, h http.Handler) {
//		router.HandleStd("ANY", pattern+"*", h)
//	})
//
// 交出去的 handler 已经包好会话级路由与前缀剥离，宿主不需要知道
// 本模块内部的路径结构。不要在 mount 里改写 pattern（再套一层前缀或 StripPrefix）：
// terminal_url 与跨节点转发都按这里的 prefix 推导，改写就会脱节。
//
// MCP 端点不在这里挂：嵌入宿主时通常与宿主自己的工具共用一个 /mcp（见 RegisterTools），
// 那条链路由宿主用 WithSessionRouting 包裹。
func MountWebTerminal(prefix string, mount func(pattern string, h http.Handler)) error {
	if mount == nil {
		return fmt.Errorf("%s", "MountWebTerminal 需要非空 mount 函数")
	}
	if err := validateMountPrefix(prefix); err != nil {
		return err
	}
	session.SetPathPrefix(prefix)
	mount(prefix+webTerminalPattern, withTerminalRouting(
		http.StripPrefix(prefix+viewPrefix, terminal.TerminalHandler())))
	return nil
}

// 本模块内部固定的两段路径：网页终端挂在 <prefix>/view/terminal/ 之下，
// 而 handler 自己按 /terminal/ 解析，所以要先剥掉 <prefix>/view。
const (
	viewPrefix         = "/view"
	webTerminalPattern = "/view/terminal/"
)

// validateMountPrefix 拒绝会静默变成 404 的写法，不做容错纠正：前缀写错的现象是外部请求
// 404，而 404 不指向前缀。宁可启动失败，让部署方立刻看到原因。
func validateMountPrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	switch {
	case !strings.HasPrefix(prefix, "/"):
		return fmt.Errorf("挂载前缀 %q 必须以 / 开头", prefix)
	case strings.HasSuffix(prefix, "/"):
		return fmt.Errorf("挂载前缀 %q 不能以 / 结尾（内部 pattern 自带前导斜杠）", prefix)
	case strings.ContainsAny(prefix, "*? \t"):
		return fmt.Errorf("挂载前缀 %q 不能含通配符或空白"+
			"（它是一段字面路径，通配由宿主在 mount 函数里自己加）", prefix)
	}
	return nil
}
