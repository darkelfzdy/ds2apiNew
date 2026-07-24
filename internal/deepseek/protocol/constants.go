package protocol

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	DeepSeekHost                    = "chat.deepseek.com"
	DeepSeekLoginURL                = "https://chat.deepseek.com/api/v0/users/login"
	DeepSeekCreateSessionURL        = "https://chat.deepseek.com/api/v0/chat_session/create"
	DeepSeekCreatePowURL            = "https://chat.deepseek.com/api/v0/chat/create_pow_challenge"
	DeepSeekCompletionURL           = "https://chat.deepseek.com/api/v0/chat/completion"
	DeepSeekContinueURL             = "https://chat.deepseek.com/api/v0/chat/continue"
	DeepSeekStopStreamURL           = "https://chat.deepseek.com/api/v0/chat/stop_stream"
	DeepSeekUploadFileURL           = "https://chat.deepseek.com/api/v0/file/upload_file"
	DeepSeekFetchFilesURL           = "https://chat.deepseek.com/api/v0/file/fetch_files"
	DeepSeekFetchSessionURL         = "https://chat.deepseek.com/api/v0/chat_session/fetch_page"
	DeepSeekDeleteSessionURL        = "https://chat.deepseek.com/api/v0/chat_session/delete"
	DeepSeekDeleteAllSessionsURL    = "https://chat.deepseek.com/api/v0/chat_session/delete_all"
	DeepSeekUpdateSettingsURL       = "https://chat.deepseek.com/api/v0/users/update_settings"
	DeepSeekClientSettingsURL       = "https://chat.deepseek.com/api/v0/client/settings"
	DeepSeekClientSettingsReportURL = "https://chat.deepseek.com/api/v0/client/settings/report"
	DeepSeekCompletionTargetPath    = "/api/v0/chat/completion"
	DeepSeekUploadTargetPath        = "/api/v0/file/upload_file"
)

// chromeMajorVersion 与 transport 层 utls.HelloChrome_Auto 保持一致，
// 使 TLS 指纹与 HTTP 层的 Chrome User-Agent 属于同一代浏览器。
const chromeMajorVersion = "128"

var chromeUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + chromeMajorVersion + ".0.0.0 Safari/537.36"
var chromeSecChUA = "\"Not.A/Brand\";v=\"8\", \"Chromium\";v=\"" + chromeMajorVersion + "\", \"Google Chrome\";v=\"" + chromeMajorVersion + "\""

var defaultStaticBaseHeaders = map[string]string{
	"Host":         "chat.deepseek.com",
	"Accept":       "application/json",
	"Content-Type": "application/json",
}

// webBrowserHeaders 是 platform=web 时 DeepSeek 网页版浏览器应有的头。
// 与 utls.HelloChrome_Auto 配合，使 TLS 指纹与 HTTP 头自洽。
var webBrowserHeaders = map[string]string{
	"User-Agent":         chromeUserAgent,
	"sec-ch-ua":          chromeSecChUA,
	"sec-ch-ua-mobile":   "?0",
	"sec-ch-ua-platform": "\"Windows\"",
	"Origin":             "https://chat.deepseek.com",
	"Referer":            "https://chat.deepseek.com/",
	"sec-fetch-site":     "same-origin",
	"sec-fetch-mode":     "cors",
	"sec-fetch-dest":     "empty",
}

// localeTimezoneOffsets 按 locale 给出 x-client-timezone-offset 的秒偏移，
// 避免所有账号都硬编码成 28800（东八区）。
var localeTimezoneOffsets = map[string]string{
	"zh_CN": "28800",
	"zh_TW": "28800",
	"en_US": "-420",
	"en_GB": "3600",
	"ja_JP": "32400",
	"ko_KR": "32400",
	"de_DE": "7200",
	"fr_FR": "7200",
	"ru_RU": "18000",
	"es_ES": "7200",
}

// localeAcceptLanguages 按 locale 给出 Accept-Language 值。
var localeAcceptLanguages = map[string]string{
	"zh_CN": "zh-CN,zh;q=0.9",
	"zh_TW": "zh-TW,zh;q=0.9",
	"en_US": "en-US,en;q=0.9",
	"en_GB": "en-GB,en;q=0.9",
	"ja_JP": "ja-JP,ja;q=0.9",
	"ko_KR": "ko-KR,ko;q=0.9",
	"de_DE": "de-DE,de;q=0.9",
	"fr_FR": "fr-FR,fr;q=0.9",
	"ru_RU": "ru-RU,ru;q=0.9",
	"es_ES": "es-ES,es;q=0.9",
}

var defaultSkipContainsPatterns = []string{
	"quasi_status",
	"elapsed_secs",
	"token_usage",
	"pending_fragment",
	"conversation_mode",
	"fragments/-1/status",
	"fragments/-2/status",
	"fragments/-3/status",
}

var defaultSkipExactPaths = []string{
	"response/search_status",
}

var ClientVersion string
var BaseHeaders = map[string]string{}
var SkipContainsPatterns = cloneStringSlice(defaultSkipContainsPatterns)
var SkipExactPathSet = toStringSet(defaultSkipExactPaths)

type clientConstants struct {
	Name            string `json:"name"`
	Platform        string `json:"platform"`
	Version         string `json:"version"`
	AndroidAPILevel string `json:"android_api_level"`
	Locale          string `json:"locale"`
}

type sharedConstants struct {
	Client              clientConstants   `json:"client"`
	BaseHeaders         map[string]string `json:"base_headers"`
	SkipContainsPattern []string          `json:"skip_contains_patterns"`
	SkipExactPaths      []string          `json:"skip_exact_paths"`
}

//go:embed constants_shared.json
var sharedConstantsJSON []byte

func init() {
	cfg := sharedConstants{}
	if err := json.Unmarshal(sharedConstantsJSON, &cfg); err != nil {
		panic(fmt.Errorf("load DeepSeek shared constants: %w", err))
	}
	sharedClient = normalizeClientConstants(cfg.Client)
	sharedBaseHeaderOverrides = cfg.BaseHeaders
	applySharedConstants(cfg)
}

func applySharedConstants(cfg sharedConstants) {
	client := normalizeClientConstants(cfg.Client)
	ClientVersion = client.Version
	BaseHeaders = BuildBaseHeaders(client, cfg.BaseHeaders)
	SkipContainsPatterns = cloneStringSlice(defaultSkipContainsPatterns)
	if len(cfg.SkipContainsPattern) > 0 {
		SkipContainsPatterns = cloneStringSlice(cfg.SkipContainsPattern)
	}
	SkipExactPathSet = toStringSet(defaultSkipExactPaths)
	if len(cfg.SkipExactPaths) > 0 {
		SkipExactPathSet = toStringSet(cfg.SkipExactPaths)
	}
}

func normalizeClientConstants(in clientConstants) clientConstants {
	if in.Name == "" {
		in.Name = "DeepSeek"
	}
	if in.Platform == "" {
		in.Platform = "web"
	}
	if in.Locale == "" {
		in.Locale = "zh_CN"
	}
	return in
}

// BuildBaseHeaders 根据 client 配置和 locale 构建请求头。
// 当 platform=web 时会补齐浏览器头，使与 Chrome TLS 指纹一致；
// x-client-timezone-offset 按 locale 动态设置。
func BuildBaseHeaders(client clientConstants, overrides map[string]string) map[string]string {
	out := cloneStringMap(defaultStaticBaseHeaders)
	for k, v := range overrides {
		if k == "" || v == "" {
			continue
		}
		out[k] = v
	}

	locale := strings.TrimSpace(client.Locale)
	if locale == "" {
		locale = "zh_CN"
	}
	out["x-client-timezone-offset"] = TimezoneOffsetFor(locale)

	if IsWebPlatform(client.Platform) {
		for k, v := range webBrowserHeaders {
			out[k] = v
		}
		out["Accept-Language"] = AcceptLanguageFor(locale)
	} else if client.Name != "" && client.Version != "" {
		// App / 非 web 平台保持 App 风格 UA。
		out["User-Agent"] = client.Name + "/" + client.Version
	}

	if client.Platform != "" {
		out["x-client-platform"] = client.Platform
	}
	if client.Version != "" {
		out["x-client-version"] = client.Version
	}
	if client.Locale != "" {
		out["x-client-locale"] = client.Locale
	}
	return out
}

// BaseHeadersFor 使用共享 client 配置，并按 locale 覆盖时区和语言头。
func BaseHeadersFor(locale string) map[string]string {
	client := normalizeClientConstants(sharedClient)
	client.Locale = strings.TrimSpace(locale)
	if client.Locale == "" {
		client.Locale = "zh_CN"
	}
	return BuildBaseHeaders(client, sharedBaseHeaderOverrides)
}

// LoginHeaders 返回登录/刷新 token 使用的保守头。
// 登录接口对浏览器头较敏感，使用不带 Chrome UA 和 sec-* 的原始 App 风格头可避免异常响应。
func LoginHeaders(locale string) map[string]string {
	client := normalizeClientConstants(sharedClient)
	client.Locale = strings.TrimSpace(locale)
	if client.Locale == "" {
		client.Locale = "zh_CN"
	}
	out := cloneStringMap(defaultStaticBaseHeaders)
	for k, v := range sharedBaseHeaderOverrides {
		if k == "" || v == "" {
			continue
		}
		out[k] = v
	}
	out["x-client-timezone-offset"] = TimezoneOffsetFor(client.Locale)
	if client.Name != "" && client.Version != "" {
		out["User-Agent"] = client.Name + "/" + client.Version
	}
	if client.Platform != "" {
		out["x-client-platform"] = client.Platform
	}
	if client.Version != "" {
		out["x-client-version"] = client.Version
	}
	if client.Locale != "" {
		out["x-client-locale"] = client.Locale
	}
	return out
}

// sharedClient 和 sharedBaseHeaderOverrides 保存 init 时解析的原始值。
var sharedClient clientConstants
var sharedBaseHeaderOverrides map[string]string

// IsWebPlatform 判断 platform 是否代表网页版。
func IsWebPlatform(platform string) bool {
	return strings.EqualFold(strings.TrimSpace(platform), "web")
}

// TimezoneOffsetFor 返回 locale 对应的时区偏移（秒），未知时回退到东八区。
func TimezoneOffsetFor(locale string) string {
	if offset, ok := localeTimezoneOffsets[strings.TrimSpace(locale)]; ok {
		return offset
	}
	return "28800"
}

// AcceptLanguageFor 返回 locale 对应的 Accept-Language，未知时回退到中文。
func AcceptLanguageFor(locale string) string {
	if lang, ok := localeAcceptLanguages[strings.TrimSpace(locale)]; ok {
		return lang
	}
	return "zh-CN,zh;q=0.9"
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneStringSlice(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func toStringSet(in []string) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for _, v := range in {
		if v == "" {
			continue
		}
		out[v] = struct{}{}
	}
	return out
}

const (
	KeepAliveTimeout  = 5
	StreamIdleTimeout = 300
	MaxKeepaliveCount = 40
)
