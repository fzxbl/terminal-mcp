package main

import (
	"context"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/fzxbl/terminal-mcp/internal/config"
	"github.com/fzxbl/terminal-mcp/internal/logging"
	"github.com/fzxbl/terminal-mcp/internal/session"
	"github.com/fzxbl/terminal-mcp/mcpserver"
)

func main() {
	confPath := flag.String("config", "", "path to config.toml")
	listen := flag.String("listen", "", "override listen_addr")
	flag.Parse()

	c := config.Load(*confPath)
	if *listen != "" {
		c.ListenAddr = *listen
	}
	session.InitStore(c.MaxSessions)

	// 分布式：进程自动探测本机可达地址作为 session_id 的属主 token，无需每实例配不同地址
	// （通配监听 0.0.0.0 会被解析为本机实际 IP）。跨 NAT/需对外映射时用 SetAdvertiseAddr 覆盖。
	if c.ListenAddr != "" {
		mcpserver.SetNodeToken(session.ReachableHostPort(c.ListenAddr))
	}
	mcpserver.SetPeers(c.Peers)

	if err := os.MkdirAll(c.LogDir, 0o755); err != nil {
		log.Fatalf("create log dir %s: %v", c.LogDir, err)
	}

	// 服务运行日志：按时间切割的滚动文件 + stderr（交互运行时仍能看到启动行）。
	srvLog := logging.New(c.LogDir, "terminal-mcp", c.LogRotate, c.LogMaxAgeDays)
	defer srvLog.Close()
	log.SetOutput(io.MultiWriter(os.Stderr, srvLog))

	// 审计日志：按时间切割落到 <log_dir> 下（前缀取 audit_log 的文件名，或默认 "audit"）。
	auditPrefix := "audit"
	if c.AuditLog != "" {
		auditPrefix = auditFilePrefix(c.AuditLog)
	}
	auditW := logging.New(c.LogDir, auditPrefix, c.LogRotate, c.LogMaxAgeDays)
	defer auditW.Close()

	ctx, cancel := context.WithCancel(context.Background())
	session.StartIdleGC(ctx)

	srv := &http.Server{Addr: c.ListenAddr, Handler: mcpserver.NewHTTPHandler(auditW)}
	go func() {
		log.Printf("terminal-mcp listening on %s (logs in %s, rotate=%s; no auth — bind loopback or add a reverse proxy)", c.ListenAddr, c.LogDir, c.LogRotate)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	cancel()
	session.Shutdown()
	_ = srv.Close()
}

// auditFilePrefix 从 audit_log 配置里取文件名前缀（去掉目录与 .log 扩展名），用作滚动文件前缀。
func auditFilePrefix(p string) string {
	base := strings.TrimSuffix(filepath.Base(p), ".log")
	if base == "" || base == "." {
		return "audit"
	}
	return base
}
