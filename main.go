package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

const usage = `workbuddy-checkin (Go)

用法:
  wbcheckin [选项]

选项:
  --dry-run            只列出发现的账号，不调用签到接口
  --force              忽略当日缓存，强制重新签到
  --uid <uid>          只处理指定账号，可多次指定
  --json               以 JSON 输出结果
  --no-refresh         禁用 accessToken 自动续期
  --auths <dir>        解密后凭证目录（默认 ./auths）
  --cache <file>       幂等缓存文件（默认 ./checkin-cache.json）
  --workbuddy-exe <p>  指定 WorkBuddy.exe 路径（解密用）
  -h, --help           显示帮助

退出码: 0=全部成功/已签到 1=有失败 2=未发现账号或参数错误
`

type uidFlags []string

func (u *uidFlags) String() string { return strings.Join(*u, ",") }
func (u *uidFlags) Set(v string) error {
	*u = append(*u, v)
	return nil
}

func main() {
	os.Exit(run())
}

func run() int {
	fs := flag.NewFlagSet("wbcheckin", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var uids uidFlags
	dryRun := fs.Bool("dry-run", false, "")
	force := fs.Bool("force", false, "")
	jsonOut := fs.Bool("json", false, "")
	noRefresh := fs.Bool("no-refresh", false, "")
	auths := fs.String("auths", "auths", "")
	cachePath := fs.String("cache", "checkin-cache.json", "")
	exe := fs.String("workbuddy-exe", "", "")
	help := fs.Bool("help", false, "")
	fs.Var(&uids, "uid", "")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }

	if err := fs.Parse(os.Args[1:]); err != nil {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	if *help {
		fmt.Print(usage)
		return 0
	}

	accounts, warnings := discoverAccounts(*auths, *exe, uids)
	if !*jsonOut {
		for _, w := range warnings {
			fmt.Printf("[warn] %s\n", w)
		}
	}
	if len(accounts) == 0 {
		fmt.Fprintf(os.Stderr, "未找到任何账号：%s 及本机 WorkBuddy 登录目录下均无可用凭证。\n", *auths)
		fmt.Fprintln(os.Stderr, "请先登录 WorkBuddy 桌面端，或把已解密凭证放入 auths/ 目录。")
		return 2
	}

	if *dryRun {
		type row struct {
			UID       string `json:"uid"`
			Nickname  string `json:"nickname"`
			Phone     string `json:"phone"`
			Domain    string `json:"domain"`
			ExpiresAt string `json:"tokenExpiresAt"`
			Source    string `json:"source"`
		}
		rows := []row{}
		for _, a := range accounts {
			exp := "unknown"
			if a.Auth.ExpiresAt > 0 {
				exp = time.UnixMilli(a.Auth.ExpiresAt).Format(time.RFC3339)
			}
			rows = append(rows, row{a.UID, a.Nickname, a.Phone, orDefault(a.Auth.Domain, "(默认域名)"), exp, a.Source})
		}
		if *jsonOut {
			b, _ := json.MarshalIndent(rows, "", "  ")
			fmt.Println(string(b))
		} else {
			for _, r := range rows {
				name := orDefault(r.Nickname, r.UID)
				if r.Phone != "" {
					name += " (" + r.Phone + ")"
				}
				fmt.Printf("账号 %s  domain=%s  token到期=%s\n  source=%s\n", name, r.Domain, r.ExpiresAt, r.Source)
			}
		}
		return 0
	}

	today := time.Now().Format("2006-01-02")
	cache := loadCache(*cachePath)
	failed := 0

	type outRow struct {
		UID     string `json:"uid"`
		Name    string `json:"name"`
		OK      bool   `json:"ok"`
		Skipped bool   `json:"skipped"`
		Credit  int64  `json:"credit,omitempty"`
		Streak  int64  `json:"streak_days,omitempty"`
		Message string `json:"message"`
	}
	results := []outRow{}

	for _, a := range accounts {
		name := a.displayName()
		refreshed := false
		refreshMsg := ""

		if !*noRefresh {
			refreshed, refreshMsg = refreshSession(a)
			if refreshed {
				if err := a.save(*auths); err != nil {
					warnings = append(warnings, fmt.Sprintf("凭证回写失败（%s）: %v", name, err))
				}
				if !*jsonOut {
					fmt.Printf("[auth] %s  %s\n", name, refreshMsg)
				}
			} else if refreshMsg != "" && !*jsonOut {
				fmt.Printf("[warn] %s  %s\n", name, refreshMsg)
			}
		}

		if cache.hit(a.UID, today) && !*force {
			results = append(results, outRow{UID: a.UID, Name: name, OK: true, Skipped: true, Message: "今日已签到（缓存命中）"})
			if !*jsonOut {
				fmt.Printf("[ OK ] %s  今日已签到过（缓存命中）\n", name)
			}
			continue
		}

		r := doCheckin(a)
		entry := &cacheEntry{Date: today, OK: r.OK, Credit: r.Credit, Streak: r.Streak, Message: r.Msg, At: time.Now().UnixMilli()}
		cache.Entries[a.UID] = entry
		if err := cache.save(*cachePath); err != nil {
			warnings = append(warnings, fmt.Sprintf("缓存写入失败: %v", err))
		}

		results = append(results, outRow{UID: a.UID, Name: name, OK: r.OK, Credit: r.Credit, Streak: r.Streak, Message: r.Msg})
		if r.OK {
			note := r.Msg
			if r.Credit > 0 {
				note = fmt.Sprintf("签到成功，+%d 积分", r.Credit)
				if r.Streak > 0 {
					note += fmt.Sprintf("（连签 %d 天）", r.Streak)
				}
			}
			if !*jsonOut {
				fmt.Printf("[ OK ] %s  %s\n", name, note)
			}
		} else {
			failed++
			if !*jsonOut {
				fmt.Printf("[FAIL] %s  %s\n", name, r.Msg)
			}
		}
	}

	if *jsonOut {
		b, _ := json.MarshalIndent(map[string]any{
			"date": today, "total": len(results), "failed": failed, "results": results,
		}, "", "  ")
		fmt.Println(string(b))
	} else {
		fmt.Printf("共 %d 个账号，失败 %d 个\n", len(results), failed)
		for _, w := range warnings {
			fmt.Printf("[warn] %s\n", w)
		}
	}

	if failed > 0 {
		return 1
	}
	return 0
}
