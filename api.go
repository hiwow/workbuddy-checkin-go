package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

func doJSON(url string, headers map[string]string, payload any) (int, map[string]any, error) {
	var body []byte
	if payload == nil {
		body = []byte("{}")
	} else {
		b, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		body = b
	}
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return -1, map[string]any{"error": err.Error()}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		parsed = map[string]any{"raw": string(raw)}
	}
	return resp.StatusCode, parsed, nil
}

func baseHeaders(s *Session) map[string]string {
	tokenType := orDefault(s.Auth.TokenType, "Bearer")
	h := map[string]string{
		"Accept":        "application/json",
		"Authorization": tokenType + " " + s.Auth.AccessToken,
		"Content-Type":  "application/json",
		"X-User-Id":     s.UID,
		"User-Agent":    "WorkBuddy",
	}
	account, _ := s.Raw["account"].(map[string]any)
	if account != nil {
		if eid := str(account["enterpriseId"]); eid != "" {
			h["X-Enterprise-Id"] = eid
			h["X-Tenant-Id"] = eid
		}
	}
	if s.Auth.Domain != "" {
		h["X-Domain"] = s.Auth.Domain
	}
	return h
}

func dig(obj map[string]any, key string) any {
	if obj == nil {
		return nil
	}
	if v, ok := obj[key]; ok && v != nil {
		return v
	}
	for _, k := range []string{"data", "result", "resp", "response"} {
		if sub, ok := obj[k].(map[string]any); ok {
			if r := dig(sub, key); r != nil {
				return r
			}
		}
	}
	return nil
}

func digString(obj map[string]any, key string) string {
	return str(dig(obj, key))
}

func digBool(obj map[string]any, key string) (bool, bool) {
	switch t := dig(obj, key).(type) {
	case bool:
		return t, true
	case float64:
		return t != 0, true
	}
	return false, false
}

func digInt(obj map[string]any, key string) (int64, bool) {
	switch t := dig(obj, key).(type) {
	case float64:
		return int64(t), true
	case json.Number:
		n, err := t.Int64()
		return n, err == nil
	case string:
		var n int64
		if _, err := fmt.Sscanf(t, "%d", &n); err == nil {
			return n, true
		}
	}
	return 0, false
}

type checkinResult struct {
	OK     bool
	Credit int64
	Streak int64
	Total  int64
	Msg    string
}

func checkinStatus(s *Session) (int, map[string]any) {
	code, body, _ := doJSON(s.endpoint()+"/v2/billing/meter/checkin-activity-status", baseHeaders(s), nil)
	return code, body
}

func doCheckin(s *Session) checkinResult {
	code, body := checkinStatus(s)
	switch {
	case code == -1:
		return checkinResult{Msg: "网络不可达，稍后重试"}
	case code == 401 || code == 403:
		return checkinResult{Msg: fmt.Sprintf("登录态已失效（HTTP %d），请重新登录 WorkBuddy", code)}
	case code < 200 || code >= 300:
		return checkinResult{Msg: fmt.Sprintf("签到状态查询异常（HTTP %d）", code)}
	}
	if active, ok := digBool(body, "active"); ok && !active {
		return checkinResult{OK: true, Msg: "签到活动未开启"}
	}
	if today, ok := digBool(body, "today_checked_in"); ok && today {
		return checkinResult{OK: true, Msg: "今日已签到", Streak: intOf(body, "streak_days"), Total: intOf(body, "total_credits")}
	}

	ccode, cbody, _ := doJSON(s.endpoint()+"/v2/billing/meter/daily-checkin", baseHeaders(s), nil)
	switch {
	case ccode == -1:
		return checkinResult{Msg: "领取请求未能送达，稍后重试"}
	case ccode == 401 || ccode == 403:
		return checkinResult{Msg: fmt.Sprintf("登录态已失效（HTTP %d），请重新登录 WorkBuddy", ccode)}
	}
	if credit, ok := digInt(cbody, "credit"); ok {
		scode, sbody := checkinStatus(s)
		streak := intOf(sbody, "streak_days")
		total := intOf(sbody, "total_credits")
		if scode < 200 || scode >= 300 {
			streak = intOf(cbody, "streak_days")
		}
		return checkinResult{OK: true, Credit: credit, Streak: streak, Total: total, Msg: "签到成功"}
	}
	msg := digString(cbody, "msg")
	if msg == "" {
		msg = fmt.Sprintf("领取失败（HTTP %d）", ccode)
	}
	if isAlready(msg) {
		return checkinResult{OK: true, Msg: "今日已领取", Streak: intOf(cbody, "streak_days")}
	}
	return checkinResult{Msg: msg}
}

func isAlready(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(msg, "已签到") || strings.Contains(msg, "已领取") || strings.Contains(m, "already")
}

func intOf(body map[string]any, key string) int64 {
	v, _ := digInt(body, key)
	return v
}

func tokenExpMs(tok string) int64 {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return 0
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return 0
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return 0
	}
	exp, ok := m["exp"].(float64)
	if !ok {
		return 0
	}
	return int64(exp) * 1000
}

func refreshSession(s *Session) (bool, string) {
	if s.Auth.RefreshToken == "" {
		return false, "无 refreshToken"
	}
	exp := tokenExpMs(s.Auth.AccessToken)
	if exp > 0 && exp-time.Now().UnixMilli() > 7*86400*1000 {
		return false, ""
	}
	reason := "AT 缺失"
	if exp > 0 {
		reason = "AT 将于 " + time.UnixMilli(exp).Format("2006-01-02") + " 过期"
	} else if s.Auth.AccessToken != "" {
		reason = "AT 无法解析"
	}
	headers := map[string]string{
		"Content-Type":          "application/json",
		"Accept":                "application/json",
		"X-Refresh-Token":       s.Auth.RefreshToken,
		"X-Auth-Refresh-Source": "plugin",
	}
	code, body, _ := doJSON(defaultEndpoint+"/v2/plugin/auth/token/refresh", headers, nil)
	at := digString(body, "accessToken")
	if code >= 200 && code < 300 && at != "" {
		s.Auth.AccessToken = at
		if rt := digString(body, "refreshToken"); rt != "" {
			s.Auth.RefreshToken = rt
		}
		s.Auth.ExpiresAt = tokenExpMs(at)
		s.Auth.LastRefreshTime = time.Now().UnixMilli()
		return true, "Token 已续期（" + reason + "）"
	}
	return false, fmt.Sprintf("Token 续期失败（%s）：HTTP %d %s", reason, code, digString(body, "msg"))
}
