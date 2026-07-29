// Package outputref 把超限结果的固定日志范围编码成不透明、防篡改的 token，
// 取代会话内引用注册表。使用进程级随机 HMAC 密钥：token 有效期等于进程生命周期
// （会话为内存态，重启后会话先失效，故不追求跨重启可解析）。
package outputref

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
)

// Scope 是一次超限结果绑定的固定信息。字段全部导出以便 JSON 编解码与相等比较。
type Scope struct {
	From    int64  `json:"f"`
	To      int64  `json:"t"`
	Input   string `json:"i,omitempty"` // 命令回显清洗语义
	Observe bool   `json:"o,omitempty"` // 人工接管命令还原语义
}

var key = mustRandKey()

func mustRandKey() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("outputref: rand key: " + err.Error())
	}
	return b
}

var enc = base64.RawURLEncoding

// Sign 生成 "<payload>.<mac>"，payload 与 mac 均 base64url 编码。
func Sign(s Scope) string {
	payload, _ := json.Marshal(s)
	mac := sign(payload)
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(mac)
}

// Parse 验签并解码；失败返回错误，绝不回退到默认范围。
func Parse(token string) (Scope, error) {
	var s Scope
	dot := -1
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			dot = i
			break
		}
	}
	if dot <= 0 || dot == len(token)-1 {
		return s, errors.New("outputref: malformed token")
	}
	payload, err := enc.DecodeString(token[:dot])
	if err != nil {
		return s, errors.New("outputref: bad payload encoding")
	}
	mac, err := enc.DecodeString(token[dot+1:])
	if err != nil {
		return s, errors.New("outputref: bad mac encoding")
	}
	if !hmac.Equal(mac, sign(payload)) {
		return s, errors.New("outputref: signature mismatch")
	}
	if err := json.Unmarshal(payload, &s); err != nil {
		return s, errors.New("outputref: bad payload")
	}
	return s, nil
}

func sign(payload []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(payload)
	return m.Sum(nil)
}
