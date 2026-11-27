// Package config loads runtime settings from the environment.
// Heroku config vars map directly onto these keys.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	BotToken           string
	AdminIDs           []int64
	BannedIDs          []int64
	ProxyListURL       string
	Proxies            []string
	DatabaseURL        string
	Concurrency        int
	Timeout            time.Duration
	MaxCookiesPerJob   int
	MaxSearchAccounts  int
	SearchResultsLimit int
	MaxSearchTermLen   int
	MaxProxiesPerUser  int
	Providers          []string
	NFLoginLinks       bool
	Cooldown           time.Duration
	ProxyRefresh       time.Duration
	LogLevel           string
}

func Load() (*Config, error) {
	cfg := &Config{
		BotToken:           strings.TrimSpace(os.Getenv("BOT_TOKEN")),
		AdminIDs:           envIDs("ADMIN_IDS"),
		BannedIDs:          envIDs("BANNED_IDS"),
		ProxyListURL:       strings.TrimSpace(os.Getenv("PROXY_LIST_URL")),
		Proxies:            envLines("PROXY_LIST"),
		DatabaseURL:        strings.TrimSpace(os.Getenv("DATABASE_URL")),
		Concurrency:        envInt("CHECK_CONCURRENCY", 20),
		Timeout:            time.Duration(envInt("CHECK_TIMEOUT", 30)) * time.Second,
		MaxCookiesPerJob:   envInt("MAX_COOKIES_PER_JOB", 0),
		MaxSearchAccounts:  envInt("MAX_SEARCH_ACCOUNTS", 0),
		SearchResultsLimit: envInt("SEARCH_RESULTS_LIMIT", 25),
		MaxSearchTermLen:   envInt("MAX_SEARCH_TERM_LENGTH", 120),
		MaxProxiesPerUser:  envInt("MAX_PROXIES_PER_USER", 500),
		Providers:          envList("PROVIDERS", []string{"microsoft", "netflix"}),
		NFLoginLinks:       envBool("NF_LOGIN_LINKS", true),
		Cooldown:           time.Duration(envInt("JOB_COOLDOWN_SECONDS", 10)) * time.Second,
		ProxyRefresh:       time.Duration(envInt("PROXY_REFRESH_MINUTES", 10)) * time.Minute,
		LogLevel:           envStr("LOG_LEVEL", "info"),
	}

	if cfg.BotToken == "" {
		return nil, fmt.Errorf("BOT_TOKEN is required")
	}

	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	if cfg.Concurrency > 100 {
		cfg.Concurrency = 100
	}
	if cfg.MaxCookiesPerJob < 0 {
		cfg.MaxCookiesPerJob = 0
	}
	if cfg.MaxSearchAccounts < 0 {
		cfg.MaxSearchAccounts = 0
	}
	if cfg.SearchResultsLimit < 1 || cfg.SearchResultsLimit > 200 {
		cfg.SearchResultsLimit = 25
	}
	if cfg.MaxSearchTermLen < 8 {
		cfg.MaxSearchTermLen = 8
	}
	if cfg.MaxProxiesPerUser < 1 {
		cfg.MaxProxiesPerUser = 500
	}
	if cfg.Timeout < 5*time.Second {
		cfg.Timeout = 5 * time.Second
	}

	return cfg, nil
}

func (c *Config) IsAdmin(id int64) bool {
	for _, a := range c.AdminIDs {
		if a == id {
			return true
		}
	}
	return false
}

func (c *Config) IsBanned(id int64) bool {
	for _, a := range c.BannedIDs {
		if a == id {
			return true
		}
	}
	return false
}

func envBool(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

// envList reads a comma or whitespace separated list.
func envList(key string, def []string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	raw = strings.ReplaceAll(raw, ";", ",")
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return def
	}
	return out
}

func envStr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envIDs(key string) []int64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	var out []int64
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	return out
}

func envLines(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	raw = strings.ReplaceAll(raw, ",", "\n")
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}
