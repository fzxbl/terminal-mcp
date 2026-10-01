package terminal

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/fzxbl/terminal-mcp/internal/config"
	"github.com/fzxbl/terminal-mcp/internal/session"
)

// 宽 PTY 默认尺寸：模型侧运行时用超宽终端，避免输出被硬换行。
// 人工接管时按浏览器终端尺寸同步；退出接管即恢复此宽尺寸。
const (
	defaultPtyRows uint16 = 200
	defaultPtyCols uint16 = 1000
)

// TerminalHandler 提供只读、可滚动的网页终端，供人（运维/开发）实时观看模型在会话里的操作。
// 默认只读；点「人工接管」后经 WebSocket 转发按键到 PTY。
//
// handler 内部固定按 /terminal/ 前缀解析；若外围需要额外前缀（如 /view），
// 在挂载时用 http.StripPrefix 把它剥掉，本 handler 无需感知。
//
//	GET  /terminal/<id>          -> 内嵌渲染器的 HTML 页面
//	GET  /terminal/<id>/stream   -> SSE，先推全量 scrollback 再持续推增量
//	GET  /terminal/<id>/takeover -> 持 capability 返回当前接管态；POST 置/清接管态
//	GET  /terminal/<id>/ws       -> capability subprotocol 鉴权后，接管态下转发按键/resize 到 PTY
func TerminalHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rest := strings.TrimPrefix(req.URL.Path, "/terminal/")
		rest = strings.Trim(rest, "/")
		if rest == "" {
			http.Error(w, "missing session id", http.StatusBadRequest)
			return
		}
		if name, ok := strings.CutPrefix(rest, "assets/"); ok {
			serveTerminalAsset(w, req, name)
			return
		}
		if id, ok := strings.CutSuffix(rest, "/stream"); ok {
			serveTerminalStream(w, req, strings.Trim(id, "/"))
			return
		}
		if id, ok := strings.CutSuffix(rest, "/takeover"); ok {
			serveTakeover(w, req, strings.Trim(id, "/"))
			return
		}
		if id, ok := strings.CutSuffix(rest, "/ws"); ok {
			serveTerminalInput(w, req, strings.Trim(id, "/"))
			return
		}
		serveTerminalPage(w, rest)
	})
}

// serveTerminalStream 以 SSE 推送会话输出：连接时先发全部已有 scrollback，之后每 200ms 发增量。
// 原始字节（含 ANSI）经 base64 编码承载，避免 SSE 行分隔破坏二进制/多字节内容，前端解码后渲染。
func serveTerminalStream(w http.ResponseWriter, req *http.Request, id string) {
	sess := session.LookupWithCapability(id, req.Header.Get("X-Terminal-Capability"))
	if sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// 无 live proc（关闭/死亡/进程重启）：从 transcript 文件推全量历史，标记 disconnected，结束。
	if !sess.Live() {
		data, has := session.ReadTranscript(id)
		if !has {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		if len(data) > 0 {
			fmt.Fprintf(w, "event: data\ndata: %s\n\n", base64.StdEncoding.EncodeToString(data))
		}
		fmt.Fprintf(w, "event: state\ndata: disconnected\n\n")
		flusher.Flush()
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ctx := req.Context()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	var off int64 = 0
	lastState := ""
	for {
		if total := sess.Len(); total > off {
			chunk := sess.Since(off)
			off = total
			fmt.Fprintf(w, "event: data\ndata: %s\n\n", base64.StdEncoding.EncodeToString([]byte(chunk)))
			flusher.Flush()
		}
		state := sess.State()
		disp := state
		if disp == "dead" {
			disp = "disconnected" // 运行中断开：统一以 disconnected 呈现，前端据此禁接管
		}
		if disp != lastState {
			lastState = disp
			fmt.Fprintf(w, "event: state\ndata: %s\n\n", disp)
			flusher.Flush()
		}
		if state == "dead" {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// serveTakeover 查询/切换会话的人工接管标志，并做单人持有校验。
// 所有请求先校验 session capability。GET ?owner=<浏览器签名> 返回 {held, mine}（mine=当前接管是否为本浏览器持有），供新窗口加载/轮询同步 UI。
// POST body {"on":true|false, "owner":..., "cols":..., "rows":...} 置/清接管：
//
//	on=true 时若已被他人接管则返回 409（保证同一时刻仅一人可接管）；
//	on=false 时若非持有者则返回 409（只有持有者能释放）。浏览器 hold-owner 校验与 capability 分开。
func serveTakeover(w http.ResponseWriter, req *http.Request, id string) {
	sess := session.LookupWithCapability(id, req.Header.Get("X-Terminal-Capability"))
	if sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	writeState := func(code int, held, mine bool) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]bool{"held": held, "mine": mine})
	}
	if req.Method == http.MethodGet {
		owner := req.URL.Query().Get("owner")
		held := sess.Held()
		writeState(http.StatusOK, held, held && owner != "" && sess.HoldOwner() == owner)
		return
	}
	var body struct {
		On    bool   `json:"on"`
		Owner string `json:"owner"`
		Cols  uint16 `json:"cols"`
		Rows  uint16 `json:"rows"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.On {
		if !sess.Live() {
			http.Error(w, "session not live: cannot take over", http.StatusConflict)
			return
		}
		if ok, _ := sess.AcquireHold(body.Owner); !ok {
			writeState(http.StatusConflict, true, false) // 已被他人接管
			return
		}
		// 接管即按浏览器终端尺寸同步 PTY（0 时忽略，随后由 WS resize 校准），
		// 不依赖 WS onopen 时序，vim/top 一开始就拿到正确尺寸。
		sess.SetSize(body.Rows, body.Cols)
		writeState(http.StatusOK, true, true)
		return
	}
	if !sess.ReleaseHold(body.Owner) {
		writeState(http.StatusConflict, true, false) // 非持有者不能释放
		return
	}
	// 退出接管：恢复宽 PTY，保证模型后续输出不被硬换行。
	sess.SetSize(defaultPtyRows, defaultPtyCols)
	writeState(http.StatusOK, false, false)
}

// wsUpgrader 升级人工接管输入连接。内网只读工具，放开 origin 校验。
var wsUpgrader = websocket.Upgrader{
	// 仅允许同源、或非浏览器（无 Origin）客户端：只读观看页升级为可写通道后，
	// 阻断外站借用户浏览器跨源连上 /ws 向 PTY 注入按键（drive-by 注入）。无鉴权决策不变。
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // 非浏览器客户端（含测试/CLI）无 Origin
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return u.Host == r.Host
	},
}

// serveTerminalInput 接受浏览器 WebSocket 的接管输入。仅 held=true 时可用。
// 帧为 JSON：{"t":"in","d":"<按键字节>"} 写入 PTY；{"t":"resize","cols":C,"rows":R} 同步窗口大小。
func serveTerminalInput(w http.ResponseWriter, req *http.Request, id string) {
	capability := websocketCapability(req)
	sess := session.LookupWithCapability(id, capability)
	if sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if !sess.Live() {
		http.Error(w, "session not live", http.StatusConflict)
		return
	}
	if !sess.Held() {
		http.Error(w, "session not under human takeover", http.StatusConflict)
		return
	}
	// 单人持有校验：接管有归属时，仅持有者（owner 匹配）可连上写通道；
	// 无归属（force/测试路径，holdOwner 为空）时放行。阻断第二人借 WS 同时注入按键。
	if owner := sess.HoldOwner(); owner != "" && owner != req.URL.Query().Get("owner") {
		http.Error(w, "session held by another operator", http.StatusConflict)
		return
	}
	// 回显浏览器提供的能力 subprotocol：浏览器在「提供了 subprotocol 却没被服务端回选」时会直接
	// Fail 握手（Chrome 报 "Sent non-empty 'Sec-WebSocket-Protocol' header but no response was
	// received"），而 Go 客户端对缺省回显是容忍的——所以此前只在网页端复现、单测/Go 拨号都抓不到。
	// 必须把它原样回选，否则人工接管连不上写通道、无法输入。
	var respHeader http.Header
	if proto := capabilitySubprotocol(req); proto != "" {
		respHeader = http.Header{"Sec-Websocket-Protocol": {proto}}
	}
	conn, err := wsUpgrader.Upgrade(w, req, respHeader)
	if err != nil {
		// 诊断：此前这里静默 return，接管输入升级失败时服务端无任何痕迹，排障困难。
		// 记一行足够定位失败层：是否实现 http.Hijacker（中间件包裹 ResponseWriter 常把它丢掉，
		// 表现为 SSE 可用但 WS 升级不了）、Origin/Host（同源校验）与底层错误。
		_, hijackable := w.(http.Hijacker)
		log.Printf("[terminal] ws upgrade failed id=%s hijackable=%v origin=%q host=%q err=%v",
			id, hijackable, req.Header.Get("Origin"), req.Host, err)
		return
	}
	defer conn.Close()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if !sess.Held() { // 人已退出接管，停止接受输入
			return
		}
		var msg struct {
			T    string `json:"t"`
			D    string `json:"d"`
			Cols uint16 `json:"cols"`
			Rows uint16 `json:"rows"`
		}
		if json.Unmarshal(data, &msg) != nil {
			continue // 非法帧忽略
		}
		switch msg.T {
		case "in":
			sess.WriteInput(msg.D)
			sess.Touch()
		case "resize":
			sess.SetSize(msg.Rows, msg.Cols)
			sess.Touch()
		}
	}
}

// websocketCapability reads the bearer key from a WebSocket subprotocol offered by
// the browser. This keeps it out of the URL while satisfying the browser WebSocket API.
func websocketCapability(req *http.Request) string {
	if p := capabilitySubprotocol(req); p != "" {
		return strings.TrimPrefix(p, "terminal-capability.")
	}
	return ""
}

// capabilitySubprotocol returns the full "terminal-capability.<key>" subprotocol the
// browser offered (empty if none). The server must echo it back on upgrade, or browsers
// fail the handshake ("Sent non-empty 'Sec-WebSocket-Protocol' header but no response").
func capabilitySubprotocol(req *http.Request) string {
	for _, protocol := range websocket.Subprotocols(req) {
		if strings.HasPrefix(protocol, "terminal-capability.") {
			return protocol
		}
	}
	return ""
}

// serveTerminalPage 返回加载本服务内嵌资源的终端页面（无需第三方运行时依赖）。
func serveTerminalPage(w http.ResponseWriter, id string) {
	if session.Lookup(id) == nil {
		if _, ok := session.ReadTranscript(id); !ok {
			http.Error(w, "session not found: "+id, http.StatusNotFound)
			return
		}
	}
	cfg := config.Get()
	nonceBytes := make([]byte, 18)
	if _, err := rand.Read(nonceBytes); err != nil {
		http.Error(w, "failed to initialize terminal page", http.StatusInternalServerError)
		return
	}
	nonce := base64.StdEncoding.EncodeToString(nonceBytes)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'self'; object-src 'none'; frame-ancestors 'none'; form-action 'none'; script-src 'nonce-"+nonce+"'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self' ws: wss:; worker-src 'self' blob:")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store") // 页面内嵌 JS 随版本变化，禁缓存避免浏览器用旧页
	page := strings.ReplaceAll(terminalPageHTML, "__SESSION_ID__", id)
	page = strings.ReplaceAll(page, "__CSP_NONCE__", nonce)
	page = strings.ReplaceAll(page, "__DEFAULT_FONT__", cfg.DefaultFont)
	page = strings.ReplaceAll(page, "__DEFAULT_FSIZE__", fmt.Sprintf("%d", cfg.DefaultFontSize))
	page = strings.ReplaceAll(page, "__DEFAULT_THEME__", cfg.DefaultTheme)
	page = strings.ReplaceAll(page, "__DEFAULT_RENDERER__", cfg.DefaultRenderer)
	_, _ = w.Write([]byte(page))
}
