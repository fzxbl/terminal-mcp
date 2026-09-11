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
	SetSelfAddr("10.0.0.1:8900")
	id := newSessionID()
	tok, uuid := decodeSessionID(id)
	if tok != "10.0.0.1:8900" {
		t.Fatalf("自身直连地址 = %q", tok)
	}
	if uuid == "" || uuid == id {
		t.Fatalf("uuid part not extracted: %q", uuid)
	}
}

func TestDecodeOwnerlessID(t *testing.T) {
	// 单机未配置自身直连地址时，session_id 不带属主，路由应留在本地处理。
	tok, uuid := decodeSessionID("bare-uuid-no-sep")
	if tok != "" || uuid != "bare-uuid-no-sep" {
		t.Fatalf("ownerless decode got (%q,%q)", tok, uuid)
	}
}
