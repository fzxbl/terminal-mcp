package session

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/fzxbl/terminal-mcp/internal/pty"
)

// theStore 是全局会话表，由 InitStore() 初始化。
var theStore *store

// Session 是一个持久会话。live 句柄 proc 只在内存；元信息可落盘。
type Session struct {
	ID         string
	Owner      string // 客户端归属签名（见 internal/identity）；空表示无归属（历史/匿名）
	Host       string
	Mode       string
	Status     string // loading | ready | dead | closed
	Err        string
	CreatedAt  time.Time
	lastUsed   time.Time
	proc       *pty.ProcSession
	reopenName string     // hard reset 重开用的 exec 名
	reopenArgs []string   // hard reset 重开用的 exec 参数
	mu         sync.Mutex // 串行化单会话内 send
	stateMu    sync.Mutex // 守护 Status/Err/lastUsed/deliveredOffset

	deliveredOffset int64 // since_last 交付游标

	humanHold bool      // 人工接管标志：为真时拦截模型的 send/close/reset
	holdOwner string    // 当前接管持有者标识（浏览器签名）；空表示无归属（force/测试路径）
	holdSince time.Time // 接管起始时刻（观测用）

	// pendingSwitch：上一条 shell 切换命令停在了交互鉴权问询（如 ssh 的 (yes/no)? / password:），
	// 真正进入新一层 shell 发生在后续输入上。置位后，下一条 Send 无论命令是否在切换注册表里，
	// 都按 shell 切换续作处理（等 shell 提示符 + 自动布哨），落到 shell/哨兵提示符后清除。
	pendingSwitch bool

	// 人工接管产生的缓冲区间 [takeoverStart, takeoverEnd)：用于在人交回控制权（humanHold 已置 false）后，
	// 模型仍能以 observe 方式把这段字节里人敲的命令还原成 "[rc=n] $ 命令"。
	// takeoverActive 为真表示存在尚未被 since_last 全部交付的接管内容；takeoverEnd==0 表示仍在接管中。
	takeoverStart  int64
	takeoverEnd    int64
	takeoverActive bool
}

// setStatus 加 stateMu 写 Status/Err（errMsg 为空时不覆盖 Err）。
func (s *Session) setStatus(status, errMsg string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.Status = status
	if errMsg != "" {
		s.Err = errMsg
	}
}

// snapshotStatus 加 stateMu 读取 Status/Err 快照。
func (s *Session) snapshotStatus() (status, errMsg string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.Status, s.Err
}

// touch 加 stateMu 更新 lastUsed。
func (s *Session) touch() {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.lastUsed = time.Now()
}

// idleSince 加 stateMu 读取距上次使用的时长。
func (s *Session) idleSince() time.Duration {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return time.Since(s.lastUsed)
}

// setDelivered 加 stateMu 写交付游标。
func (s *Session) setDelivered(n int64) { s.stateMu.Lock(); s.deliveredOffset = n; s.stateMu.Unlock() }

// delivered 加 stateMu 读交付游标。
func (s *Session) delivered() int64 {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.deliveredOffset
}

// setHold 加 stateMu 置/清人工接管标志，置时记录 holdSince。
// 同时维护人工接管的缓冲区间：置位时记录起点（当前缓冲末尾）、清位时记录终点，
// 以便交回控制权后 Read 仍能以 observe 方式还原这段区间内人敲的命令。
// 注意：Len 走的是 proc.mu，getProc 走 stateMu，均在锁 stateMu 之前调用，避免自锁。
func (s *Session) setHold(on bool) {
	var off int64
	if p := s.getProc(); p != nil {
		off = p.Len()
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.humanHold = on
	if on {
		s.holdSince = time.Now()
		s.takeoverStart = off
		s.takeoverEnd = 0
		s.takeoverActive = true
	} else {
		s.holdOwner = "" // 交回控制权：清除归属，便于他人重新接管
		if s.takeoverActive {
			s.takeoverEnd = off
		}
	}
}

// acquireHold 尝试以 owner（浏览器签名）获取人工接管：未被接管、或已由本人持有时成功；
// 被他人持有时失败并回传当前持有者标识。owner 为空视为无归属抢占（force/测试路径）。
// 首次进入接管态时记录缓冲起点；同一持有者续持不重置区间，保证 observe 还原连续。
func (s *Session) acquireHold(owner string) (ok bool, curOwner string) {
	var off int64
	if p := s.getProc(); p != nil {
		off = p.Len()
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.humanHold && s.holdOwner != "" && s.holdOwner != owner {
		return false, s.holdOwner // 已被他人接管
	}
	fresh := !s.humanHold
	s.humanHold = true
	s.holdOwner = owner
	s.holdSince = time.Now()
	if fresh {
		s.takeoverStart = off
		s.takeoverEnd = 0
		s.takeoverActive = true
	}
	return true, owner
}

// releaseHold 以 owner 释放人工接管：owner 与当前持有者匹配（或当前无归属）才成功；
// 非持有者释放失败。已非接管态视为成功（幂等）。
func (s *Session) releaseHold(owner string) bool {
	var off int64
	if p := s.getProc(); p != nil {
		off = p.Len()
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if !s.humanHold {
		return true
	}
	if s.holdOwner != "" && s.holdOwner != owner {
		return false // 非持有者不能释放
	}
	s.humanHold = false
	s.holdOwner = ""
	if s.takeoverActive {
		s.takeoverEnd = off
	}
	return true
}

// holdOwnerID 读当前接管持有者标识（空表示未接管或无归属）。
func (s *Session) holdOwnerID() string {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.holdOwner
}

// takeoverRange 读接管缓冲区间快照：active 为真时 [start,end) 有效，end==0 表示仍在接管中。
func (s *Session) takeoverRange() (start, end int64, active bool) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.takeoverStart, s.takeoverEnd, s.takeoverActive
}

// clearTakeover 接管内容已被 since_last 全部交付后清除区间，Read 恢复常规清洗。
func (s *Session) clearTakeover() {
	s.stateMu.Lock()
	s.takeoverStart, s.takeoverEnd, s.takeoverActive = 0, 0, false
	s.stateMu.Unlock()
}

// setPendingSwitch / pendingSwitchFlag：读写"待完成的 shell 切换"标记。
func (s *Session) setPendingSwitch(on bool) {
	s.stateMu.Lock()
	s.pendingSwitch = on
	s.stateMu.Unlock()
}
func (s *Session) pendingSwitchFlag() bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.pendingSwitch
}

// held 加 stateMu 读人工接管标志。
func (s *Session) held() bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.humanHold
}

// getProc 加 stateMu 读 proc 指针（并发安全）。
func (s *Session) getProc() *pty.ProcSession {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.proc
}

// setProc 加 stateMu 写 proc 指针（并发安全）。
func (s *Session) setProc(p *pty.ProcSession) { s.stateMu.Lock(); s.proc = p; s.stateMu.Unlock() }

type store struct {
	mu  sync.Mutex
	max int
	m   map[string]*Session
}

func newStore(max int) *store { return &store{max: max, m: map[string]*Session{}} }

// InitStore 初始化全局会话表。
func InitStore(maxSessions int) { theStore = newStore(maxSessions) }

func (s *store) add(sess *Session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.m) >= s.max {
		return false
	}
	sess.CreatedAt = time.Now()
	sess.lastUsed = sess.CreatedAt
	s.m[sess.ID] = sess
	return true
}

func (s *store) get(id string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[id]
}

func (s *store) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
}

func (s *store) list() []*Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Session, 0, len(s.m))
	for _, v := range s.m {
		out = append(out, v)
	}
	return out
}

// gcIdle 关闭并移除空闲超过 ttl 的会话。
func (s *store) gcIdle(ttl time.Duration) {
	s.mu.Lock()
	victims := []*Session{}
	for id, v := range s.m {
		if v.idleSince() > ttl {
			victims = append(victims, v)
			delete(s.m, id)
		}
	}
	s.mu.Unlock()
	for _, v := range victims {
		if p := v.getProc(); p != nil {
			p.Close()
		}
		v.setStatus("closed", "")
	}
}

// closeAll 关闭并移除全部会话，杀掉各自子进程组（含 ssh 客户端）。
// 供进程优雅退出时回收子进程，避免残留 ssh/gdb 等；崩溃/被 SIGKILL 时无法执行，
// 但 PTY master 随进程关闭会令 ssh -tt 收到 EOF/SIGHUP 而多半自行退出。
func (s *store) closeAll() {
	s.mu.Lock()
	victims := make([]*Session, 0, len(s.m))
	for id, v := range s.m {
		victims = append(victims, v)
		delete(s.m, id)
	}
	s.mu.Unlock()
	for _, v := range victims {
		if p := v.getProc(); p != nil {
			p.Close()
		}
		v.setStatus("closed", "")
	}
}

// idSep 分隔 session_id 里的节点 token 与 uuid。host:port 与 uuid 均不含它。
const idSep = "~"

// nodeToken 是本节点对外可达地址（host:port），编码进 session_id 供跨节点路由。
var nodeToken atomic.Value // string

// SetNodeToken 设置本节点 token（对外可达 host:port）。空串则生成的 id 不带节点前缀（等价单机）。
func SetNodeToken(tok string) { nodeToken.Store(tok) }

func currentNodeToken() string {
	t, _ := nodeToken.Load().(string)
	return t
}

// NodeTokenForRouting 返回本节点 token（host:port），供路由中间件判定"本机会话"。
func NodeTokenForRouting() string { return currentNodeToken() }

// newSessionID 生成 <nodeToken>~<uuid>；nodeToken 为空时退化为纯 uuid（单机兼容）。
func newSessionID() string {
	u := uuid.NewString()
	if tok := currentNodeToken(); tok != "" {
		return tok + idSep + u
	}
	return u
}

// decodeSessionID 拆出节点 token 与 uuid。无分隔符（旧格式/非法）时 token 为空、uuid 为原串。
func decodeSessionID(id string) (token, uuid string) {
	if i := strings.Index(id, idSep); i >= 0 {
		return id[:i], id[i+len(idSep):]
	}
	return "", id
}

// DecodeSessionID 导出版本，供上层路由中间件解析属主节点。
func DecodeSessionID(id string) (token, uuid string) { return decodeSessionID(id) }
