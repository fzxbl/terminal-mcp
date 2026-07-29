package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFanoutAggregates(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(forwardedHeader) != "1" {
			t.Errorf("fanout request should carry forwarded header")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"sessions":[{"session_id":"peer~s9","host":"h","status":"ready"}]}}}`))
	}))
	defer peer.Close()
	peerHost := peer.URL[len("http://"):]

	local := []map[string]string{{"session_id": "local~s1", "status": "ready"}}
	got := fanoutList(local, []string{peerHost}, http.Header{}, "alice")
	if len(got) != 2 {
		t.Fatalf("aggregated = %v (want local + peer)", got)
	}
}

// 动态 provider 覆盖静态 peers，且 peerList 实时反映 provider 返回值（支持任意服务发现）。
func TestPeerProviderOverridesStatic(t *testing.T) {
	defer setPeerProvider(nil)

	setPeers([]string{"static:1"})
	if got := peerList(); len(got) != 1 || got[0] != "static:1" {
		t.Fatalf("static peers = %v", got)
	}

	dyn := []string{"dyn-a:1", "dyn-b:2"}
	setPeerProvider(func() []string { return dyn })
	if got := peerList(); len(got) != 2 || got[0] != "dyn-a:1" {
		t.Fatalf("provider peers = %v", got)
	}

	// provider 返回值实时变化应被 peerList 反映
	dyn = []string{"dyn-c:3"}
	if got := peerList(); len(got) != 1 || got[0] != "dyn-c:3" {
		t.Fatalf("provider live update = %v", got)
	}
}
