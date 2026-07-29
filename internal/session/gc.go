package session

import (
	"context"
	"time"

	"github.com/fzxbl/terminal-mcp/internal/config"
	"github.com/fzxbl/terminal-mcp/internal/pty"
)

// StartIdleGC 启动空闲会话回收协程；ctx 取消时回收全部会话并杀残留子进程。
// 每分钟一轮：按 IdleTTLMinutes 回收空闲会话，并按 TranscriptRetentionDays 清理过期落盘。
func StartIdleGC(ctx context.Context) {
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				if theStore != nil {
					theStore.closeAll()
				}
				return
			case <-t.C:
				c := config.Get()
				if theStore != nil {
					theStore.gcIdle(time.Duration(c.IdleTTLMinutes) * time.Minute)
				}
				pty.SweepTranscripts(c.TranscriptDir,
					time.Duration(c.TranscriptRetentionDays)*24*time.Hour)
			}
		}
	}()
}

// Shutdown 立即关闭全部会话并回收子进程组；幂等。
func Shutdown() {
	if theStore != nil {
		theStore.closeAll()
	}
}
