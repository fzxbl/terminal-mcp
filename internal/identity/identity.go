package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
)

// sep 是拼接各 header 值的分隔符（不可打印，避免值内碰撞）。
const sep = "\x1f"

// Signer 按固定 header 集合从请求头算出客户端归属签名。
type Signer struct {
	headers   []string
	mode      string // raw | sha256
	onMissing string // reject | allow_empty
}

// New 构造 Signer。参数应已由 config 归一（非法值在 config.applyDefaults 兜底）。
func New(headers []string, mode, onMissing string) *Signer {
	return &Signer{headers: headers, mode: mode, onMissing: onMissing}
}

// Signature 计算归属签名。ok=false 表示按 onMissing=reject 策略缺头、应拒绝该调用。
func (s *Signer) Signature(h http.Header) (sig string, ok bool) {
	parts := make([]string, 0, len(s.headers))
	for _, name := range s.headers {
		v := h.Get(name)
		if v == "" && s.onMissing == "reject" {
			return "", false
		}
		parts = append(parts, v)
	}
	raw := strings.Join(parts, sep)
	if s.mode == "sha256" {
		sum := sha256.Sum256([]byte(raw))
		return hex.EncodeToString(sum[:]), true
	}
	return raw, true
}
