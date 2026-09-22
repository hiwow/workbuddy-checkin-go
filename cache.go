package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type cacheEntry struct {
	Date    string `json:"date"`
	OK      bool   `json:"ok"`
	Credit  int64  `json:"credit"`
	Streak  int64  `json:"streak_days"`
	Message string `json:"message"`
	At      int64  `json:"at"`
}

type cacheFile struct {
	Entries map[string]*cacheEntry `json:"entries"`
}

func loadCache(path string) *cacheFile {
	c := &cacheFile{Entries: map[string]*cacheEntry{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	if err := json.Unmarshal(raw, c); err != nil || c.Entries == nil {
		c.Entries = map[string]*cacheEntry{}
	}
	return c
}

func (c *cacheFile) save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

func (c *cacheFile) hit(uid, today string) bool {
	e, ok := c.Entries[uid]
	return ok && e != nil && e.Date == today && e.OK
}
