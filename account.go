package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const defaultEndpoint = "https://copilot.tencent.com"

var authRelPaths = []string{
	filepath.Join("CodeBuddyExtension", "Data", "Public", "auth", "workbuddy-desktop-ai.info"),
	filepath.Join("CodeBuddyExtension", "Data", "Public", "auth", "workbuddy-desktop.info"),
	filepath.Join("CodeBuddyExtension", "Data", "Public", "auth", "Tencent-Cloud.coding-copilot.info"),
}

type Auth struct {
	AccessToken      string
	RefreshToken     string
	TokenType        string
	Domain           string
	Endpoint         string
	ExpiresAt        int64
	RefreshExpiresAt int64
	LastRefreshTime  int64
}

type Session struct {
	Raw      map[string]any
	Source   string
	UID      string
	Nickname string
	Phone    string
	UIN      string
	Auth     Auth
}

func num(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case json.Number:
		n, _ := t.Int64()
		return n
	case string:
		var n int64
		fmt.Sscanf(t, "%d", &n)
		return n
	}
	return 0
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func newSession(raw map[string]any, source string) (*Session, error) {
	account, _ := raw["account"].(map[string]any)
	if account == nil {
		if arr, ok := raw["accounts"].([]any); ok && len(arr) > 0 {
			account, _ = arr[0].(map[string]any)
		}
	}
	auth, _ := raw["auth"].(map[string]any)
	if account == nil || auth == nil {
		return nil, fmt.Errorf("缺少 account 或 auth 字段")
	}
	uid := str(account["uid"])
	if uid == "" {
		return nil, fmt.Errorf("缺少 account.uid")
	}
	token := str(auth["accessToken"])
	if token == "" {
		return nil, fmt.Errorf("凭证未解密或缺少 accessToken")
	}
	s := &Session{
		Raw:      raw,
		Source:   source,
		UID:      uid,
		Nickname: str(account["nickname"]),
		Phone:    str(account["phoneNumber"]),
		UIN:      str(account["uin"]),
		Auth: Auth{
			AccessToken:      token,
			RefreshToken:     str(auth["refreshToken"]),
			TokenType:        orDefault(str(auth["tokenType"]), "Bearer"),
			Domain:           str(auth["domain"]),
			Endpoint:         str(auth["endpoint"]),
			ExpiresAt:        num(auth["expiresAt"]),
			RefreshExpiresAt: num(auth["refreshExpiresAt"]),
			LastRefreshTime:  num(auth["lastRefreshTime"]),
		},
	}
	return s, nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func (s *Session) displayName() string {
	name := s.Nickname
	if name == "" {
		name = s.UID
	}
	if s.Phone != "" {
		name += " (" + s.Phone + ")"
	}
	return name
}

func (s *Session) endpoint() string {
	return strings.TrimRight(orDefault(s.Auth.Endpoint, defaultEndpoint), "/")
}

func (s *Session) fresherThan(o *Session) bool {
	if o == nil {
		return true
	}
	if s.Auth.LastRefreshTime != o.Auth.LastRefreshTime {
		return s.Auth.LastRefreshTime > o.Auth.LastRefreshTime
	}
	return s.Auth.ExpiresAt > o.Auth.ExpiresAt
}

func (s *Session) save(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	auth, _ := s.Raw["auth"].(map[string]any)
	if auth == nil {
		auth = map[string]any{}
		s.Raw["auth"] = auth
	}
	auth["accessToken"] = s.Auth.AccessToken
	auth["refreshToken"] = s.Auth.RefreshToken
	if s.Auth.ExpiresAt > 0 {
		auth["expiresAt"] = s.Auth.ExpiresAt
	}
	if s.Auth.LastRefreshTime > 0 {
		auth["lastRefreshTime"] = s.Auth.LastRefreshTime
	}
	raw, err := json.MarshalIndent(s.Raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, s.UID+".json"), raw, 0o600)
}

func candidateRoots() []string {
	home, _ := os.UserHomeDir()
	var roots []string
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		roots = append(roots, la)
	}
	roots = append(roots, filepath.Join(home, "AppData", "Local"))
	roots = append(roots, filepath.Join(home, "Library", "Application Support"))
	xdg := os.Getenv("XDG_DATA_HOME")
	if xdg == "" {
		xdg = filepath.Join(home, ".local", "share")
	}
	roots = append(roots, xdg)
	roots = append(roots, filepath.Join(home, ".config"))
	return roots
}

func collectAuthFiles(storeDir string) []string {
	var files []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		if _, err := os.Stat(p); err == nil {
			seen[p] = true
			files = append(files, p)
		}
	}

	if entries, err := os.ReadDir(storeDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				add(filepath.Join(storeDir, e.Name()))
			}
		}
	}

	for _, root := range candidateRoots() {
		for _, rel := range authRelPaths {
			add(filepath.Join(root, rel))
		}
		glob, _ := filepath.Glob(filepath.Join(root, "CodeBuddyExtension", "Data", "Public", "auth", "*desktop*.info"))
		for _, p := range glob {
			add(p)
		}
	}
	return files
}

func discoverAccounts(storeDir, exeOverride string, uidFilter []string) ([]*Session, []string) {
	files := collectAuthFiles(storeDir)

	byUID := map[string]*Session{}
	var warnings []string
	var key []byte
	keyTried := false
	extract := func(force bool) ([]byte, error) {
		if keyTried && !force && key != nil {
			return key, nil
		}
		k, err := extractBuildKey(findWorkBuddyExe(exeOverride))
		if err != nil {
			return nil, err
		}
		key = k
		keyTried = true
		return key, nil
	}

	for _, file := range files {
		rawBytes, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		var probe map[string]any
		if err := json.Unmarshal(rawBytes, &probe); err != nil {
			warnings = append(warnings, fmt.Sprintf("跳过无法解析的凭证 %s", filepath.Base(file)))
			continue
		}
		raw := probe
		if hasEncryptedFields(probe) {
			decrypted, err := decryptFile(rawBytes, extract)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s %v", filepath.Base(file), err))
				continue
			}
			raw = decrypted
		}
		s, err := newSession(raw, file)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("跳过 %s: %v", filepath.Base(file), err))
			continue
		}
		if cur := byUID[s.UID]; s.fresherThan(cur) {
			byUID[s.UID] = s
		}
	}

	filter := map[string]bool{}
	for _, u := range uidFilter {
		filter[u] = true
	}
	var out []*Session
	for _, s := range byUID {
		if len(filter) > 0 && !filter[s.UID] {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out, warnings
}

func parseAndDecrypt(rawBytes []byte, key []byte) (map[string]any, error) {
	var raw map[string]any
	if err := json.Unmarshal(rawBytes, &raw); err != nil {
		return nil, err
	}
	if _, err := decryptFields(raw, key); err != nil {
		return nil, err
	}
	return raw, nil
}

func decryptFile(rawBytes []byte, extract func(force bool) ([]byte, error)) (map[string]any, error) {
	k, err := extract(false)
	if err != nil {
		return nil, fmt.Errorf("含加密字段但无法取密钥: %w", err)
	}
	raw, err := parseAndDecrypt(rawBytes, k)
	if err == nil {
		return raw, nil
	}
	k2, retryErr := extract(true)
	if retryErr != nil {
		return nil, fmt.Errorf("解密失败且重新取密钥失败: %v（原错误: %v）", retryErr, err)
	}
	raw2, err2 := parseAndDecrypt(rawBytes, k2)
	if err2 != nil {
		return nil, fmt.Errorf("解密失败（已重新取密钥重试一次）: %w", err2)
	}
	return raw2, nil
}
