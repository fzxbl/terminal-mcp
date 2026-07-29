package session

import (
	"github.com/fzxbl/terminal-mcp/internal/config"
	"github.com/fzxbl/terminal-mcp/internal/pty"
)

// 本文件为网页终端（internal/terminal）提供最小必要的导出访问面：
// terminal 只经这些薄包装读写会话/PTY，不直接触碰 Session 的非导出字段或内部 proc 状态。

// Lookup 按 id 取会话；store 未初始化或不存在均返回 nil。
func Lookup(id string) *Session {
	if theStore == nil {
		return nil
	}
	return theStore.get(id)
}

// ReadTranscript 读取会话历史全量（供网页终端在会话关闭/断开后回看）。
// 内部按当前配置的 TranscriptDir 定位落盘文件；不存在或 id 非法返回 ok=false。
func ReadTranscript(id string) ([]byte, bool) {
	return pty.ReadTranscript(config.Get().TranscriptDir, id)
}

// Live 报告会话是否仍有存活子进程（可实时流、可接管）。
// 关闭/死亡/进程重启后（无此会话或 proc 已死）均为 false，此时走历史回看路径。
// nil 会话安全返回 false。
func (s *Session) Live() bool {
	if s == nil {
		return false
	}
	if st, _ := s.snapshotStatus(); st == "closed" || st == "dead" {
		return false
	}
	p := s.getProc()
	return p != nil && !p.IsDead()
}

// Held 读人工接管标志（导出包装 held）。
func (s *Session) Held() bool { return s.held() }

// SetHold 置/清人工接管标志（导出包装 setHold）。无归属的强制置位，供内部/测试使用。
func (s *Session) SetHold(on bool) { s.setHold(on) }

// AcquireHold 以 owner（浏览器签名）尝试获取人工接管：成功返回 (true, owner)；
// 已被他人接管返回 (false, 当前持有者标识)。用于保证同一时刻仅一人可接管。
func (s *Session) AcquireHold(owner string) (ok bool, curOwner string) { return s.acquireHold(owner) }

// ReleaseHold 以 owner 释放人工接管：owner 匹配当前持有者（或无归属）才成功。
func (s *Session) ReleaseHold(owner string) bool { return s.releaseHold(owner) }

// HoldOwner 返回当前接管持有者标识（空表示未接管或无归属）。
func (s *Session) HoldOwner() string { return s.holdOwnerID() }

// Touch 刷新最近使用时间（导出包装 touch）。
func (s *Session) Touch() { s.touch() }

// State 返回当前会话状态字符串（loading|idle|running|dead），供 SSE 推送状态。
func (s *Session) State() string {
	state, _, _ := computeState(s)
	return state
}

// Len 返回日志绝对总字节数，用作 SSE 增量读取的偏移基准（无 proc 为 0）。
func (s *Session) Len() int64 {
	if p := s.getProc(); p != nil {
		return p.Len()
	}
	return 0
}

// Since 返回 proc 从偏移 off 起的增量输出（无 proc 为空）。
func (s *Session) Since(off int64) string {
	if p := s.getProc(); p != nil {
		return p.Since(off)
	}
	return ""
}

// ReadRange 返回 proc 中 [from,to) 字节区间（无 proc 为空）。
func (s *Session) ReadRange(from, to int64) string {
	if p := s.getProc(); p != nil {
		return p.ReadRange(from, to)
	}
	return ""
}

// WriteInput 把人工接管输入写入 PTY（无 live proc 时静默忽略）。
func (s *Session) WriteInput(data string) {
	if p := s.getProc(); p != nil {
		p.Write(data)
	}
}

// SetSize 同步 PTY 窗口尺寸到 proc（行/列为 0 由 proc 侧忽略；无 live proc 时静默忽略）。
func (s *Session) SetSize(rows, cols uint16) {
	if p := s.getProc(); p != nil {
		p.SetSize(rows, cols)
	}
}
