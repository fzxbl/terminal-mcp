// Package oplog 是会话输出的 append-only 日志：磁盘 .raw 文件为唯一真相源，
// 内存只保留最近 cacheBytes 字节的尾部缓存；偏移为日志绝对字节位置（单调）。
// 早于缓存窗口的字节从文件回读，永不空洞。单 writer（PTY reader goroutine）+ 多 reader。
package oplog

import (
	"errors"
	"os"
	"sync"
)

type Log struct {
	mu       sync.RWMutex
	f        *os.File
	total    int64
	cache    []byte
	capacity int
	failed   bool
}

// Open 打开（追加）日志文件；打不开即返回错误（.raw 为强依赖）。
func Open(path string, cacheBytes int) (*Log, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if cacheBytes < 4096 {
		cacheBytes = 4096
	}
	l := &Log{f: f, total: st.Size(), capacity: cacheBytes}
	if l.total > 0 {
		n := int64(cacheBytes)
		if n > l.total {
			n = l.total
		}
		buf := make([]byte, n)
		if _, err := readFullAt(f, buf, l.total-n); err == nil {
			l.cache = buf
		}
	}
	return l, nil
}

func readFullAt(f *os.File, p []byte, off int64) (int, error) {
	rf, err := os.Open(f.Name())
	if err != nil {
		return 0, err
	}
	defer rf.Close()
	return rf.ReadAt(p, off)
}

// Append 唯一写入口：写文件 + 更新尾部缓存 + 推进 total。写盘失败返回错误。
func (l *Log) Append(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed || l.f == nil {
		return 0, errors.New("oplog: log is closed or in a failed state")
	}
	if _, err := l.f.Write(b); err != nil {
		l.failed = true // 短写/写错后不再接受追加，保持 total 与文件逻辑长度一致
		return 0, err
	}
	l.total += int64(len(b))
	if len(b) >= l.capacity {
		l.cache = append(l.cache[:0], b[len(b)-l.capacity:]...)
	} else {
		l.cache = append(l.cache, b...)
		if len(l.cache) > l.capacity {
			l.cache = l.cache[len(l.cache)-l.capacity:]
		}
	}
	return len(b), nil
}

// Len 返回绝对总字节数。
func (l *Log) Len() int64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.total
}

// ReadRange 返回绝对区间 [from,to) 的字节；越界 clamp 到 [0,total]，永不空洞、不报越界错。
func (l *Log) ReadRange(from, to int64) ([]byte, error) {
	l.mu.RLock()
	if l.f == nil {
		l.mu.RUnlock()
		return []byte{}, nil
	}
	total := l.total
	cacheLen := int64(len(l.cache))
	cacheStart := total - cacheLen
	var cacheCopy []byte
	if cacheLen > 0 {
		cacheCopy = append([]byte(nil), l.cache...)
	}
	name := l.f.Name()
	l.mu.RUnlock()

	if from < 0 {
		from = 0
	}
	if to > total {
		to = total
	}
	if from >= to {
		return []byte{}, nil
	}
	out := make([]byte, 0, to-from)
	if from < cacheStart {
		coldEnd := to
		if coldEnd > cacheStart {
			coldEnd = cacheStart
		}
		cold := make([]byte, coldEnd-from)
		rf, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		_, err = rf.ReadAt(cold, from)
		_ = rf.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, cold...)
		from = coldEnd
	}
	if from < to {
		lo := from - cacheStart
		hi := to - cacheStart
		if lo < 0 {
			lo = 0
		}
		if hi > cacheLen {
			hi = cacheLen
		}
		if lo < hi {
			out = append(out, cacheCopy[lo:hi]...)
		}
	}
	return out, nil
}

// Tail 返回最近 n 字节（缓存足够时纯内存；n 超过缓存则回退 ReadRange 从文件补齐）。
func (l *Log) Tail(n int) []byte {
	l.mu.RLock()
	total := l.total
	l.mu.RUnlock()
	if int64(n) >= total {
		b, _ := l.ReadRange(0, total)
		return b
	}
	b, _ := l.ReadRange(total-int64(n), total)
	return b
}

// Close 关闭底层文件。
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
