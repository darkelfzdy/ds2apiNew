package updatecheck

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestBarkURLFromEnv(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want string
	}{
		{"unset disables", "", ""},
		{"bare key", "abc123", "https://api.day.app/abc123"},
		{"key with slash", "abc123/", "https://api.day.app/abc123"},
		{"full url", "https://api.day.app/abc123", "https://api.day.app/abc123"},
		{"full url trailing slash", "https://api.day.app/abc123/", "https://api.day.app/abc123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := os.Getenv(envBarkURL)
			_ = os.Setenv(envBarkURL, tc.env)
			defer func() { _ = os.Setenv(envBarkURL, old) }()
			if got := barkURLFromEnv(); got != tc.want {
				t.Fatalf("barkURLFromEnv() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildNotifyBody(t *testing.T) {
	payload := latestReleasePayload{
		TagName:     "v3.7.0",
		HTMLURL:     "https://github.com/ouqiting/ds2api/releases/tag/v3.7.0",
		PublishedAt: "2026-08-12T00:00:00Z",
		Body:        "# Changelog\n\n- 新增 Mihomo 代理桥\n- 修复 bug\n",
	}
	got := buildNotifyBody(payload)
	if got == "" {
		t.Fatal("buildNotifyBody returned empty")
	}
	if got != "发布于 2026-08-12 · https://github.com/ouqiting/ds2api/releases/tag/v3.7.0\n- 新增 Mihomo 代理桥\n- 修复 bug" {
		t.Fatalf("unexpected body: %q", got)
	}
}

func TestSameMajor(t *testing.T) {
	if !sameMajor("3.7.0", "3.8.1") {
		t.Error("3.7.0 and 3.8.1 should share major 3")
	}
	if sameMajor("3.7.0", "4.0.0") {
		t.Error("3.x and 4.x should differ")
	}
}

func TestStatePersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "updatecheck.json")
	s := loadState(path)
	s.markNotified("v3.7.0")
	s.markChecked("v3.7.0")

	s2 := loadState(path)
	if !s2.alreadyNotified("v3.7.0") {
		t.Fatal("v3.7.0 should be persisted as notified")
	}
	if s2.alreadyNotified("v3.8.0") {
		t.Fatal("v3.8.0 should not be notified")
	}
	if s2.lastCheckedAt == "" {
		t.Fatal("lastCheckedAt should be persisted")
	}
}

func TestCheckerFetchLatestAndPush(t *testing.T) {
	var pushHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/latest":
			_ = json.NewEncoder(w).Encode(latestReleasePayload{
				TagName: "v9.9.9",
				HTMLURL: "https://example.com/release",
				Body:    "major update",
			})
		default:
			pushHits++
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	// 隔离状态文件，避免污染项目 data/ 目录
	oldState := os.Getenv("DS2API_UPDATE_STATE_PATH")
	_ = os.Setenv("DS2API_UPDATE_STATE_PATH", filepath.Join(t.TempDir(), "updatecheck.json"))
	defer func() { _ = os.Setenv("DS2API_UPDATE_STATE_PATH", oldState) }()

	c := NewChecker()
	c.cfg.barkURL = srv.URL + "/key"
	c.http = srv.Client()

	// 用测试 server 替换 GitHub API 地址
	origAPI := latestReleaseAPI
	latestReleaseAPI = srv.URL + "/repos/latest"
	defer func() { latestReleaseAPI = origAPI }()

	c.checkOnce(t.Context())
	if pushHits != 1 {
		t.Fatalf("expected 1 bark push, got %d", pushHits)
	}
	// 第二次应去重，不再推送
	c.checkOnce(t.Context())
	if pushHits != 1 {
		t.Fatalf("expected no duplicate push, got %d total", pushHits)
	}
}

func TestCheckerDisabledNoNetwork(t *testing.T) {
	c := NewChecker()
	// 未配置 bark：Start 直接返回，checkOnce 也应安全跳过
	c.Start(t.Context())
}
