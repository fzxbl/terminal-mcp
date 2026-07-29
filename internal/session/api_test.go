package session

import (
	"net"
	"strings"
	"testing"
)

func TestURLHostPort(t *testing.T) {
	// 非通配地址原样保留 host:port。
	if got := urlHostPort("127.0.0.1:8900"); got != "127.0.0.1:8900" {
		t.Fatalf("urlHostPort(127.0.0.1:8900)=%q, want unchanged", got)
	}
	// 通配地址：host 应被替换掉 0.0.0.0（若本机有可用 IP），端口必须保留。
	got := urlHostPort("0.0.0.0:8013")
	if !strings.HasSuffix(got, ":8013") {
		t.Fatalf("urlHostPort wildcard lost port: %q", got)
	}
	if strings.HasPrefix(got, "0.0.0.0:") && localIP() != "" {
		t.Fatalf("wildcard host not replaced though localIP=%q: %q", localIP(), got)
	}
	// 非 host:port 形态原样返回。
	if got := urlHostPort("not-a-hostport"); got != "not-a-hostport" {
		t.Fatalf("urlHostPort passthrough failed: %q", got)
	}
}

func TestListFilfersByOwner(t *testing.T) {
	InitStore(10)
	SetNodeToken("")
	a := &Session{ID: "a", Owner: "alice", Status: "ready"}
	b := &Session{ID: "b", Owner: "bob", Status: "ready"}
	theStore.add(a)
	theStore.add(b)

	got := List("alice")
	if len(got) != 1 || got[0]["session_id"] != "a" {
		t.Fatalf("List(alice) = %v", got)
	}
}

func TestOwnerLookup(t *testing.T) {
	InitStore(10)
	theStore.add(&Session{ID: "x", Owner: "alice"})
	owner, ok := Owner("x")
	if !ok || owner != "alice" {
		t.Fatalf("Owner(x) = (%q,%v)", owner, ok)
	}
	if _, ok := Owner("missing"); ok {
		t.Fatalf("Owner(missing) should be false")
	}
}

func TestReachableHostPort(t *testing.T) {
	// 显式 host 原样保留（含端口）。
	if got := ReachableHostPort("10.0.0.7:8900"); got != "10.0.0.7:8900" {
		t.Fatalf("explicit host = %q", got)
	}
	// 回环保留（单机场景）。
	if got := ReachableHostPort("127.0.0.1:8900"); got != "127.0.0.1:8900" {
		t.Fatalf("loopback = %q", got)
	}
	// 通配 host 应被替换为非通配（自动探测本机 IP），端口保持不变。
	got := ReachableHostPort("0.0.0.0:8900")
	if _, port, err := net.SplitHostPort(got); err != nil || port != "8900" {
		t.Fatalf("wildcard resolved = %q (port must stay 8900)", got)
	}
	// 仅当本机探测到具体 IP 时才断言通配 host 已被替换（隔离环境可能无非回环网卡）。
	if localIP() != "" && strings.HasPrefix(got, "0.0.0.0:") {
		t.Fatalf("wildcard host should be resolved to a concrete IP, got %q", got)
	}
}
