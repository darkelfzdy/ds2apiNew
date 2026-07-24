package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSharedConstantsLoaded(t *testing.T) {
	cfg := sharedConstants{}
	if err := json.Unmarshal(sharedConstantsJSON, &cfg); err != nil {
		t.Fatalf("failed to parse shared constants: %v", err)
	}
	client := normalizeClientConstants(cfg.Client)
	if ClientVersion != client.Version {
		t.Fatalf("unexpected client version=%q", ClientVersion)
	}
	if _, ok := BaseHeaders["accept-charset"]; ok {
		t.Fatal("unexpected accept-charset header present")
	}
	if BaseHeaders["x-client-platform"] != "web" {
		t.Fatalf("unexpected base header x-client-platform=%q", BaseHeaders["x-client-platform"])
	}
	if BaseHeaders["x-client-version"] != ClientVersion {
		t.Fatalf("unexpected base header x-client-version=%q", BaseHeaders["x-client-version"])
	}
	if BaseHeaders["Content-Type"] != "application/json" {
		t.Fatalf("unexpected base header Content-Type=%q", BaseHeaders["Content-Type"])
	}
	if _, ok := BaseHeaders["x-client-bundle-id"]; ok {
		t.Fatal("unexpected x-client-bundle-id header present")
	}
	// platform=web 时 BaseHeaders 应出现完整浏览器头。
	for _, h := range []string{"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform", "sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest", "Referer", "Origin", "Accept-Language"} {
		if _, ok := BaseHeaders[h]; !ok {
			t.Fatalf("expected browser header missing: %s", h)
		}
	}
	if !strings.Contains(BaseHeaders["User-Agent"], "Chrome") {
		t.Fatalf("expected Chrome User-Agent for web platform, got %q", BaseHeaders["User-Agent"])
	}
	if BaseHeaders["x-client-timezone-offset"] != "28800" {
		t.Fatalf("unexpected timezone offset for zh_CN: %q", BaseHeaders["x-client-timezone-offset"])
	}
	if len(SkipContainsPatterns) == 0 {
		t.Fatal("expected skip contains patterns to be loaded")
	}
	if _, ok := SkipExactPathSet["response/search_status"]; !ok {
		t.Fatal("expected response/search_status in exact skip path set")
	}
}

func TestClientHeadersDerivedFromSharedVersion(t *testing.T) {
	client := normalizeClientConstants(clientConstants{
		Name:            "DeepSeek",
		Platform:        "web",
		Version:         "9.8.7",
		AndroidAPILevel: "35",
		Locale:          "zh_CN",
	})
	headers := BuildBaseHeaders(client, map[string]string{
		"x-client-version": "stale",
	})
	if !strings.Contains(headers["User-Agent"], "Chrome") {
		t.Fatalf("expected Chrome User-Agent for web platform, got %q", headers["User-Agent"])
	}
	if headers["x-client-version"] != "9.8.7" {
		t.Fatalf("unexpected derived client version=%q", headers["x-client-version"])
	}
	if headers["Accept-Language"] != "zh-CN,zh;q=0.9" {
		t.Fatalf("unexpected accept-language=%q", headers["Accept-Language"])
	}
}

func TestAppPlatformUsesAppUserAgent(t *testing.T) {
	client := normalizeClientConstants(clientConstants{
		Name:            "DeepSeek",
		Platform:        "android",
		Version:         "2.2.2",
		AndroidAPILevel: "35",
		Locale:          "zh_CN",
	})
	headers := BuildBaseHeaders(client, nil)
	if headers["User-Agent"] != "DeepSeek/2.2.2" {
		t.Fatalf("expected App User-Agent, got %q", headers["User-Agent"])
	}
	if _, ok := headers["sec-ch-ua"]; ok {
		t.Fatal("unexpected browser header on android platform")
	}
}

func TestTimezoneAndLanguageDerivedFromLocale(t *testing.T) {
	cases := []struct {
		locale   string
		tz       string
		language string
	}{
		{"zh_CN", "28800", "zh-CN,zh;q=0.9"},
		{"en_US", "-420", "en-US,en;q=0.9"},
		{"ja_JP", "32400", "ja-JP,ja;q=0.9"},
	}
	for _, tc := range cases {
		tz := TimezoneOffsetFor(tc.locale)
		if tz != tc.tz {
			t.Fatalf("locale %s: expected timezone %s, got %s", tc.locale, tc.tz, tz)
		}
		lang := AcceptLanguageFor(tc.locale)
		if lang != tc.language {
			t.Fatalf("locale %s: expected language %s, got %s", tc.locale, tc.language, lang)
		}
		headers := BuildBaseHeaders(clientConstants{Name: "DeepSeek", Platform: "web", Version: "2.2.2", Locale: tc.locale}, nil)
		if headers["x-client-timezone-offset"] != tc.tz {
			t.Fatalf("locale %s: expected header timezone %s, got %s", tc.locale, tc.tz, headers["x-client-timezone-offset"])
		}
		if headers["Accept-Language"] != tc.language {
			t.Fatalf("locale %s: expected header language %s, got %s", tc.locale, tc.language, headers["Accept-Language"])
		}
	}
}

func TestLoginHeadersUseConservativeAppStyle(t *testing.T) {
	headers := LoginHeaders("zh_CN")
	if headers["User-Agent"] != "DeepSeek/"+ClientVersion {
		t.Fatalf("expected App User-Agent for login, got %q", headers["User-Agent"])
	}
	for _, h := range []string{"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform", "sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest", "Referer", "Origin", "Accept-Language"} {
		if _, ok := headers[h]; ok {
			t.Fatalf("unexpected browser header in login headers: %s", h)
		}
	}
	if headers["x-client-timezone-offset"] != "28800" {
		t.Fatalf("unexpected login timezone offset: %q", headers["x-client-timezone-offset"])
	}
}
