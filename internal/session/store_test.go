package session

import "testing"

func TestDeliveredOffset(t *testing.T) {
	s := &Session{}
	s.setDelivered(10)
	if s.delivered() != 10 {
		t.Fatalf("delivered=%d", s.delivered())
	}
}

func TestNewSessionIDUnique(t *testing.T) {
	a, b := newSessionID(), newSessionID()
	if a == "" || a == b {
		t.Fatalf("ids should be non-empty and unique: %q %q", a, b)
	}
}

func TestEncodeDecodeSessionID(t *testing.T) {
	SetNodeToken("10.0.0.1:8900")
	id := newSessionID()
	tok, uuid := decodeSessionID(id)
	if tok != "10.0.0.1:8900" {
		t.Fatalf("node token = %q", tok)
	}
	if uuid == "" || uuid == id {
		t.Fatalf("uuid part not extracted: %q", uuid)
	}
}

func TestDecodeLegacyID(t *testing.T) {
	// 旧格式（纯 uuid，无分隔符）：node token 视为空。
	tok, uuid := decodeSessionID("bare-uuid-no-sep")
	if tok != "" || uuid != "bare-uuid-no-sep" {
		t.Fatalf("legacy decode got (%q,%q)", tok, uuid)
	}
}
