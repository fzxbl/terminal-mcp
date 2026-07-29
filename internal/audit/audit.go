package audit

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

type Entry struct {
	Time     string         `json:"time"`
	CallerIP string         `json:"caller_ip"`
	User     string         `json:"user,omitempty"`
	Tool     string         `json:"tool"`
	Params   map[string]any `json:"params,omitempty"`
	State    string         `json:"state,omitempty"`
	ExitCode *int           `json:"exit_code,omitempty"`
	Held     bool           `json:"held,omitempty"`
	Bytes    int            `json:"bytes,omitempty"`
	Error    string         `json:"error,omitempty"`
}

type Logger struct {
	mu sync.Mutex
	w  io.Writer
}

func New(w io.Writer) *Logger { return &Logger{w: w} }

func (l *Logger) Log(e Entry) {
	if e.Time == "" {
		e.Time = time.Now().Format(time.RFC3339)
	}
	b, _ := json.Marshal(e)
	b = append(b, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(b)
}
