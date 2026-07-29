package pty

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// transcriptPath 返回会话落盘文件路径 <dir>/<id>.raw。
// dir 由上层（session/http 层）在调用时注入，不耦合任何配置全局。
func transcriptPath(dir, id string) string {
	return filepath.Join(dir, id+".raw")
}

// TranscriptPath 返回会话日志文件路径 <dir>/<id>.raw（供 oplog.Open 使用）；确保目录存在。
// id 含路径分隔符或建目录失败时返回 ok=false。
func TranscriptPath(dir, id string) (string, bool) {
	if strings.ContainsAny(id, "/\\") {
		return "", false
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false
	}
	return filepath.Join(dir, id+".raw"), true
}

// ReadTranscript 读取会话历史全量；文件不存在或 id 非法返回 ok=false。dir 为 transcript 落盘根目录。
func ReadTranscript(dir, id string) ([]byte, bool) {
	if strings.ContainsAny(id, "/\\") {
		return nil, false
	}
	b, err := os.ReadFile(transcriptPath(dir, id))
	if err != nil {
		return nil, false
	}
	return b, true
}

// SweepTranscripts 删除 dir 下 mtime 超过 maxAge 的 .raw 文件。
func SweepTranscripts(dir string, maxAge time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".raw") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
