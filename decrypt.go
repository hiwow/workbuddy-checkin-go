package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var (
	formatID = map[string]string{
		"file":   "WBEF1",
		"field":  "WBEV1",
		"record": "WBER1",
		"stream": "WBES1",
	}
	framingCode = map[string]byte{
		"file":   1,
		"field":  2,
		"record": 3,
		"stream": 4,
	}
)

const keyExtractJS = `const fs = require('fs');
const payload = JSON.parse(process._linkedBinding('electron_browser_workbuddy_storage').loggerGet());
fs.writeFileSync(process.argv[2], String(payload.atRestSecretKey), 'utf8');
`

func findWorkBuddyExe(override string) string {
	if override != "" {
		if _, err := os.Stat(override); err == nil {
			return override
		}
	}
	if env := os.Getenv("WORKBUDDY_EXE"); env != "" {
		if _, err := os.Stat(env); err == nil {
			return env
		}
	}
	home, _ := os.UserHomeDir()
	cands := []string{}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		cands = append(cands, filepath.Join(local, "Programs", "WorkBuddy", "WorkBuddy.exe"))
	}
	cands = append(cands,
		filepath.Join(home, "AppData", "Local", "Programs", "WorkBuddy", "WorkBuddy.exe"),
		`C:\Program Files\WorkBuddy\WorkBuddy.exe`,
		`C:\Program Files (x86)\WorkBuddy\WorkBuddy.exe`,
	)
	for _, c := range cands {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func extractBuildKey(exe string) ([]byte, error) {
	if exe == "" {
		return nil, fmt.Errorf("未找到 WorkBuddy.exe，无法解密新版加密凭证")
	}
	dir, err := os.MkdirTemp("", "wb-key-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	jsPath := filepath.Join(dir, "key.js")
	outPath := filepath.Join(dir, "key.txt")
	if err := os.WriteFile(jsPath, []byte(keyExtractJS), 0o600); err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, jsPath, outPath)
	cmd.Env = append(os.Environ(), "ELECTRON_RUN_AS_NODE=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("调用 WorkBuddy.exe 取密钥失败: %s", firstLine(msg))
	}
	secret, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("未取得 atRestSecretKey")
	}
	s := strings.TrimSpace(string(secret))
	if s == "" {
		return nil, fmt.Errorf("atRestSecretKey 为空")
	}
	sum := sha256.Sum256([]byte(s))
	return sum[:], nil
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func isTruthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t == 1
	case string:
		return t == "1"
	case json.Number:
		return t.String() == "1"
	}
	return false
}

func hasEncryptedFields(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		if raw, ok := t["$wbEncrypted"]; ok && isTruthy(raw) {
			return true
		}
		for _, c := range t {
			if hasEncryptedFields(c) {
				return true
			}
		}
	case []any:
		for _, c := range t {
			if hasEncryptedFields(c) {
				return true
			}
		}
	}
	return false
}

func decryptFields(v any, key []byte) (int, error) {
	n := 0
	switch t := v.(type) {
	case map[string]any:
		for k, c := range t {
			if m, ok := c.(map[string]any); ok {
				if raw, ok := m["$wbEncrypted"]; ok && isTruthy(raw) {
					env, _ := m["envelope"].(string)
					pt, err := decryptEnvelope(key, env)
					if err != nil {
						return n, err
					}
					t[k] = pt
					n++
					continue
				}
			}
			sub, err := decryptFields(c, key)
			n += sub
			if err != nil {
				return n, err
			}
		}
	case []any:
		for i, c := range t {
			if m, ok := c.(map[string]any); ok {
				if raw, ok := m["$wbEncrypted"]; ok && isTruthy(raw) {
					env, _ := m["envelope"].(string)
					pt, err := decryptEnvelope(key, env)
					if err != nil {
						return n, err
					}
					t[i] = pt
					n++
					continue
				}
			}
			sub, err := decryptFields(c, key)
			n += sub
			if err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

type envelope struct {
	Suite      int    `json:"suite"`
	KeyID      string `json:"keyId"`
	Nonce      string `json:"nonce"`
	AuthTag    string `json:"authTag"`
	Ciphertext string `json:"ciphertext"`
}

func decryptEnvelope(key []byte, envelopeB64 string) (any, error) {
	rawEnv, err := base64.StdEncoding.DecodeString(envelopeB64)
	if err != nil {
		return nil, fmt.Errorf("envelope base64 解码失败: %w", err)
	}
	var e envelope
	if err := json.Unmarshal(rawEnv, &e); err != nil {
		return nil, fmt.Errorf("envelope JSON 解析失败: %w", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(e.Nonce)
	if err != nil {
		return nil, fmt.Errorf("nonce 解码失败: %w", err)
	}
	tag, err := base64.StdEncoding.DecodeString(e.AuthTag)
	if err != nil {
		return nil, fmt.Errorf("authTag 解码失败: %w", err)
	}
	ct, err := base64.StdEncoding.DecodeString(e.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("ciphertext 解码失败: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	aad := buildAAD(e.KeyID, e.Suite, "field")
	plain, err := gcm.Open(nil, nonce, append(ct, tag...), aad)
	if err != nil {
		return nil, fmt.Errorf("AES-256-GCM 解密失败（构建密钥可能已随版本轮换）: %w", err)
	}
	s := string(plain)
	if len(s) > 0 && (s[0] == '{' || s[0] == '[') {
		var j any
		if json.Unmarshal(plain, &j) == nil {
			return j, nil
		}
	}
	return s, nil
}

func buildAAD(keyID string, suite int, framing string) []byte {
	var b bytes.Buffer
	b.WriteString("WB-AAD\x00")
	b.WriteByte(0x01)
	b.Write(lp(formatID[framing]))
	b.Write(lp("sym-v1"))
	b.Write(u32(uint32(suite)))
	b.Write(lp(keyID))
	b.WriteByte(framingCode[framing])
	b.WriteByte(0x00)
	b.WriteByte(0x00)
	return b.Bytes()
}

func lp(s string) []byte {
	raw := []byte(s)
	out := make([]byte, 0, 4+len(raw))
	out = append(out, u32(uint32(len(raw)))...)
	out = append(out, raw...)
	return out
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}
