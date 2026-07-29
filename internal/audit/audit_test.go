package audit

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestLogEntry(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf)
	l.Log(Entry{
		CallerIP: "10.0.0.1",
		Tool:     "terminal_send",
		Params:   map[string]any{"session_id": "s1"},
		State:    "idle",
	})
	var got Entry
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not valid json line: %v", err)
	}
	if got.CallerIP != "10.0.0.1" || got.Tool != "terminal_send" {
		t.Fatalf("bad entry: %+v", got)
	}
}
