// Package logging 提供按时间（小时/天）切割、按保存周期清理的日志写入器。
// 不按文件大小切割：每个时间桶一个文件，文件名形如 <prefix>.<时间戳>.log。
package logging

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Rotator 是一个 io.Writer：写入时按当前时间桶（小时/天）落到对应文件，
// 时间桶切换时自动开新文件，并异步清理超过保存周期的旧文件。并发写安全。
type Rotator struct {
	dir    string
	prefix string        // 文件名前缀，如 "audit" / "terminal-mcp"
	layout string        // 时间桶格式："2006010215"=按小时，"20060102"=按天
	maxAge time.Duration // 保存周期；<=0 表示永久保留

	mu     sync.Mutex
	curKey string
	f      *os.File
}

// New 构造 Rotator。rotate 取 "hourly" 或 "daily"（其它值按 daily）。
// maxAgeDays<=0 表示不清理旧文件。目录需已存在。
func New(dir, prefix, rotate string, maxAgeDays int) *Rotator {
	layout := "20060102"
	if rotate == "hourly" {
		layout = "2006010215"
	}
	var maxAge time.Duration
	if maxAgeDays > 0 {
		maxAge = time.Duration(maxAgeDays) * 24 * time.Hour
	}
	return &Rotator{dir: dir, prefix: prefix, layout: layout, maxAge: maxAge}
}

// Write 实现 io.Writer：必要时切换到当前时间桶对应的文件再写。
func (r *Rotator) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := time.Now().Format(r.layout)
	if r.f == nil || key != r.curKey {
		if r.f != nil {
			_ = r.f.Close()
		}
		name := filepath.Join(r.dir, r.prefix+"."+key+".log")
		f, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return 0, err
		}
		r.f = f
		r.curKey = key
		go r.cleanup() // 切桶时异步清理过期文件
	}
	return r.f.Write(p)
}

// Close 关闭当前文件。
func (r *Rotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f != nil {
		err := r.f.Close()
		r.f = nil
		return err
	}
	return nil
}

// cleanup 删除本 prefix 下 mtime 超过保存周期的旧日志文件（当前文件因 mtime 新不会被删）。
func (r *Rotator) cleanup() {
	if r.maxAge <= 0 {
		return
	}
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-r.maxAge)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, r.prefix+".") || !strings.HasSuffix(name, ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(r.dir, name))
		}
	}
}
