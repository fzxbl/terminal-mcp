package mcpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// peerProvider 持有"实时返回兄弟节点列表(host:port)"的函数。terminal_list fan-out 与反代
// SSRF 白名单都经 peerList() 实时读取它，从而可对接任意服务发现（Consul/etcd/DNS 等）。
var peerProvider struct {
	sync.RWMutex
	fn func() []string
}

// setPeerProvider 注册动态兄弟节点发现函数。传 nil 清除（回退到无 peers 的单机行为）。
func setPeerProvider(fn func() []string) {
	peerProvider.Lock()
	peerProvider.fn = fn
	peerProvider.Unlock()
}

// setPeers 设置静态兄弟节点列表（通常来自配置），内部包装成一个返回该快照的 provider。
// 与 setPeerProvider 互为覆盖：后调用者生效。
func setPeers(p []string) {
	snapshot := append([]string{}, p...)
	setPeerProvider(func() []string { return snapshot })
}

// peerList 实时取兄弟节点列表；未注册 provider 时返回 nil（等价单机）。
func peerList() []string {
	peerProvider.RLock()
	fn := peerProvider.fn
	peerProvider.RUnlock()
	if fn == nil {
		return nil
	}
	return fn()
}

// fanoutList 汇总本机会话与各 peer 的会话（按 session_id 去重）。单个 peer 失败不影响整体。
func fanoutList(local []map[string]string, peerHosts []string, hdr http.Header, owner string) []map[string]string {
	agg := map[string]map[string]string{}
	for _, s := range local {
		agg[s["session_id"]] = s
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, host := range peerHosts {
		wg.Add(1)
		go func(host string) {
			defer wg.Done()
			for _, s := range queryPeerList(host, hdr) {
				mu.Lock()
				agg[s["session_id"]] = s
				mu.Unlock()
			}
		}(host)
	}
	wg.Wait()
	out := make([]map[string]string, 0, len(agg))
	for _, s := range agg {
		out = append(out, s)
	}
	return out
}

// queryPeerList 调用 peer 的 /mcp 执行 terminal_list（带转发标记防环），解析出会话列表。失败返回 nil。
func queryPeerList(host string, hdr http.Header) []map[string]string {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"terminal_list","arguments":{}}}`)
	req, err := http.NewRequest(http.MethodPost, "http://"+host+"/mcp", bytes.NewReader(body))
	if err != nil {
		return nil
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set(forwardedHeader, "1")
	cli := &http.Client{Timeout: 3 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var parsed struct {
		Result struct {
			StructuredContent struct {
				Sessions []map[string]string `json:"sessions"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil
	}
	return parsed.Result.StructuredContent.Sessions
}
