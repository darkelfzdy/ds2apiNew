// Package updatecheck 定期检查上游仓库（ouqiting/ds2api）是否有新版本发布，
// 并在发现新版本时通过 bark（https://api.day.app）推送通知到手机。
//
// 通过环境变量启用与配置：
//   - DS2API_UPDATE_NOTIFY_BARK：bark 推送地址前缀（如 https://api.day.app/<key>）或完整 URL。
//     未设置则完全禁用本功能，不发起任何网络请求。
//   - DS2API_UPDATE_CHECK_INTERVAL：检查间隔，默认 6h（支持 30m/2h 等 Go duration 格式）。
//   - DS2API_UPDATE_NOTIFY_ONLY_MAJOR：仅当 major 版本变化时才推送（可选）。
package updatecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ds2api/internal/config"
	"ds2api/internal/version"
)

// Bark API 地址前缀。完整推送地址为 barkPrefix + "/" + 标题 + "/" + 正文。
const defaultBarkPrefix = "https://api.day.app"

// latestReleaseAPI 查询上游 GitHub 最新 release 的 API 地址（测试可注入）。
var latestReleaseAPI = "https://api.github.com/repos/ouqiting/ds2api/releases/latest"

const (
	envBarkURL    = "DS2API_UPDATE_NOTIFY_BARK"
	envInterval   = "DS2API_UPDATE_CHECK_INTERVAL"
	envOnlyMajor  = "DS2API_UPDATE_NOTIFY_ONLY_MAJOR"
	defaultCheck  = 6 * time.Hour
	stateFileName = "updatecheck.json"
)

type latestReleasePayload struct {
	TagName     string `json:"tag_name"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	Body        string `json:"body"`
}

// barkConfig 由环境变量解析出的推送配置。barkURL 为空表示功能禁用。
type barkConfig struct {
	barkURL   string
	interval  time.Duration
	onlyMajor bool
}

// State 持久化已推送过的版本，避免同一版本重复轰炸。
type State struct {
	mu            sync.Mutex
	path          string
	notifiedTags  map[string]string // tag -> 推送时间 RFC3339
	lastCheckedAt string
}

// Checker 负责周期性检查上游版本并推送 bark 通知。
type Checker struct {
	cfg   barkConfig
	state *State
	http  *http.Client
}

// barkURLFromEnv 解析 bark 推送地址：允许只填 key（自动拼前缀），
// 也允许填完整前缀（https://api.day.app/<key>）或带末尾斜杠。
func barkURLFromEnv() string {
	raw := strings.TrimSpace(os.Getenv(envBarkURL))
	if raw == "" {
		return ""
	}
	// 只填 key 的情况：直接是 bark key 或带 key/ 后缀
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return strings.TrimRight(raw, "/")
	}
	// 视为 key：/key 形式
	return defaultBarkPrefix + "/" + strings.Trim(strings.TrimLeft(raw, "/"), "/")
}

// Enabled 返回 bark 推送是否启用。
func Enabled() bool {
	return barkURLFromEnv() != ""
}

// NewChecker 构造 Checker。barkURL 为空时 Checker 立即返回（禁用态）。
func NewChecker() *Checker {
	burl := barkURLFromEnv()
	interval := defaultCheck
	if raw := strings.TrimSpace(os.Getenv(envInterval)); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			interval = d
		} else {
			config.Logger.Warn("[updatecheck] invalid interval, using default", "raw", raw, "default", defaultCheck)
		}
	}
	onlyMajor := strings.EqualFold(strings.TrimSpace(os.Getenv(envOnlyMajor)), "true") ||
		strings.EqualFold(strings.TrimSpace(os.Getenv(envOnlyMajor)), "1")
	statePath := config.ResolvePath("DS2API_UPDATE_STATE_PATH", filepath.Join("data", stateFileName))
	return &Checker{
		cfg:   barkConfig{barkURL: burl, interval: interval, onlyMajor: onlyMajor},
		state: loadState(statePath),
		http:  &http.Client{Timeout: 8 * time.Second},
	}
}

// Start 启动后台定时任务。ctx 取消时退出。禁用态直接返回。
func (c *Checker) Start(ctx context.Context) {
	if c == nil || c.cfg.barkURL == "" {
		config.Logger.Info("[updatecheck] disabled (DS2API_UPDATE_NOTIFY_BARK not set)")
		return
	}
	config.Logger.Info("[updatecheck] enabled", "interval", c.cfg.interval.String(), "url", maskBarkKey(c.cfg.barkURL))
	// 启动后立即检查一轮，之后按间隔循环。
	c.checkOnce(ctx)
	ticker := time.NewTicker(c.cfg.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			config.Logger.Info("[updatecheck] stopped")
			return
		case <-ticker.C:
			c.checkOnce(ctx)
		}
	}
}

// checkOnce 查询一次上游最新版本，有更新则推送。
func (c *Checker) checkOnce(ctx context.Context) {
	payload, err := c.fetchLatest(ctx)
	if err != nil {
		config.Logger.Warn("[updatecheck] fetch latest release failed", "error", err)
		return
	}
	tag := strings.TrimSpace(payload.TagName)
	if tag == "" {
		config.Logger.Warn("[updatecheck] latest release missing tag")
		return
	}

	current, _ := version.Current()
	latest := strings.TrimPrefix(tag, "v")
	hasUpdate := version.Compare(current, latest) < 0
	if !hasUpdate {
		config.Logger.Debug("[updatecheck] no update", "current", current, "latest", tag)
		c.state.markChecked(tag)
		return
	}
	if c.cfg.onlyMajor && sameMajor(current, latest) {
		config.Logger.Debug("[updatecheck] same major, skip (only-major mode)", "current", current, "latest", tag)
		c.state.markChecked(tag)
		return
	}
	if c.state.alreadyNotified(tag) {
		config.Logger.Debug("[updatecheck] already notified", "tag", tag)
		return
	}

	title := fmt.Sprintf("DS2API 上游有新版本 %s", tag)
	body := buildNotifyBody(*payload)
	config.Logger.Info("[updatecheck] upstream update detected", "current", current, "latest", tag)
	if err := c.pushBark(title, body); err != nil {
		config.Logger.Error("[updatecheck] bark push failed", "error", err)
		return
	}
	c.state.markNotified(tag)
	config.Logger.Info("[updatecheck] bark notified", "tag", tag)
}

// fetchLatest 查询上游 GitHub 最新 release。
func (c *Checker) fetchLatest(ctx context.Context) (*latestReleasePayload, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestReleaseAPI, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ds2api-updatecheck")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("github api status: %s", resp.Status)
	}
	var data latestReleasePayload
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return &data, nil
}

// pushBark 发送 bark 推送。url 形如 https://api.day.app/<key>/<title>/<body>。
func (c *Checker) pushBark(title, body string) error {
	endpoint := c.cfg.barkURL + "/" + url.PathEscape(title) + "/" + url.PathEscape(body)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ds2api-updatecheck")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("bark status: %s", resp.Status)
	}
	return nil
}

// buildNotifyBody 构造推送正文：版本说明摘要（截断）。
func buildNotifyBody(payload latestReleasePayload) string {
	var b strings.Builder
	if payload.PublishedAt != "" {
		if t, err := time.Parse(time.RFC3339, payload.PublishedAt); err == nil {
			b.WriteString("发布于 ")
			b.WriteString(t.Format("2006-01-02"))
		}
	}
	if payload.HTMLURL != "" {
		if b.Len() > 0 {
			b.WriteString(" · ")
		}
		b.WriteString(payload.HTMLURL)
	}
	body := strings.TrimSpace(payload.Body)
	if body != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		// 截断过长的 release notes，保留前 500 字符
		lines := strings.Split(body, "\n")
		for _, ln := range lines {
			ln = strings.TrimSpace(ln)
			if ln == "" || strings.HasPrefix(ln, "#") || strings.HasPrefix(ln, ">") {
				continue
			}
			if b.Len() > 500 {
				b.WriteString("…")
				break
			}
			b.WriteString(ln)
			b.WriteString("\n")
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "上游仓库发布了新版本"
	}
	return out
}

// sameMajor 判断两个版本号 major 是否相同（用于 only-major 模式）。
func sameMajor(a, b string) bool {
	am := strings.SplitN(a, ".", 2)
	bm := strings.SplitN(b, ".", 2)
	return len(am) > 0 && len(bm) > 0 && am[0] == bm[0]
}

// maskBarkKey 日志脱敏：只保留域名，隐藏 key。
func maskBarkKey(u string) string {
	if i := strings.Index(u, "day.app/"); i >= 0 {
		return u[:i+len("day.app/")] + "****"
	}
	return "****"
}

// --- State 持久化 ---

func loadState(path string) *State {
	s := &State{path: path, notifiedTags: map[string]string{}}
	b, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var raw struct {
		NotifiedTags  map[string]string `json:"notified_tags"`
		LastCheckedAt string            `json:"last_checked_at"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		config.Logger.Warn("[updatecheck] state file corrupt, starting fresh", "path", path, "error", err)
		return s
	}
	if raw.NotifiedTags != nil {
		s.notifiedTags = raw.NotifiedTags
	}
	s.lastCheckedAt = raw.LastCheckedAt
	return s
}

func (s *State) alreadyNotified(tag string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.notifiedTags[tag]
	return ok
}

func (s *State) markNotified(tag string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifiedTags[tag] = time.Now().UTC().Format(time.RFC3339)
	s.persistLocked()
}

func (s *State) markChecked(tag string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastCheckedAt = time.Now().UTC().Format(time.RFC3339)
	s.persistLocked()
}

func (s *State) persistLocked() {
	if s.path == "" {
		return
	}
	raw := struct {
		NotifiedTags  map[string]string `json:"notified_tags"`
		LastCheckedAt string            `json:"last_checked_at"`
	}{
		NotifiedTags:  s.notifiedTags,
		LastCheckedAt: s.lastCheckedAt,
	}
	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return
	}
	if dir := filepath.Dir(s.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return
		}
	}
	if err := os.WriteFile(s.path, b, 0o600); err != nil {
		config.Logger.Warn("[updatecheck] persist state failed", "path", s.path, "error", err)
	}
}
