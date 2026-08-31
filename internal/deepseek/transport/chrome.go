package transport

import (
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/sardanioss/httpcloak/fingerprint"
)

// Chrome 指纹的**唯一权威来源**是 protocol 包内嵌的 constants_shared.json
// （chrome.major_version），Go 与 Node/Vercel 两侧读同一文件，避免双写漂移。
// 本包只保留「无人推送时的内置回退」与「环境变量覆盖」，不重复维护版本号。
//
// 优先级：DS2API_CHROME_MAJOR_VERSION > protocol 推送的 JSON 值 > builtinDefaultChromeMajorVersion。
const builtinDefaultChromeMajorVersion = "151"

// chromeLowestPresetMajor 是预设向下探测的下限（httpcloak 保留的最老 Chrome 预设）。
const chromeLowestPresetMajor = 133

// chromeDefaultPreset 是全部探测失败时的兜底预设（当前升级目标，httpcloak v1.6.11 已含）。
const chromeDefaultPreset = "chrome-151-windows"

var (
	chromeVersionMu sync.Mutex
	// envChromeMajorVersion 非空时永远优先，运维无需重新编译即可换版。
	// 必须是合法大版本号：拼进 UA 的非法值（如 "abc"）会直接造出
	// Chrome/abc.0.0.0 这种没人用的坏指纹，比不改更危险，因此宁可忽略。
	envChromeMajorVersion  = ""
	envChromeRejectedValue = ""
	envChromeRead          = false
	// pushedChromeMajorVersion 由 protocol 在 init 时从 constants_shared.json 推送。
	pushedChromeMajorVersion = ""
)

// readEnvChromeMajorVersion 解析并校验 DS2API_CHROME_MAJOR_VERSION。
// 返回（合法值或空串，被拒绝的原始值）。
func readEnvChromeMajorVersion() (value string, rejected string) {
	raw := strings.TrimSpace(os.Getenv("DS2API_CHROME_MAJOR_VERSION"))
	if raw == "" {
		return "", ""
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < chromeLowestPresetMajor || n > 999 {
		return "", raw
	}
	return raw, ""
}

// ensureEnvChromeRead 惰性读取环境变量。必须惰性：本项目在 main() 里才
// LoadDotEnv()，包初始化阶段读不到 .env 中的变量，写死在 var 初始化里
// 会让「在 .env 里配 DS2API_CHROME_MAJOR_VERSION」静默失效。
func ensureEnvChromeRead() {
	if envChromeRead {
		return
	}
	envChromeRead = true
	envChromeMajorVersion, envChromeRejectedValue = readEnvChromeMajorVersion()
}

// RefreshChromeVersionFromEnv 在 .env 加载完成后重新读取一次环境变量，
// 返回被拒绝的非法值（合法或未设为空），供启动日志提醒运维“你以为换了、其实没换”。
func RefreshChromeVersionFromEnv() string {
	chromeVersionMu.Lock()
	defer chromeVersionMu.Unlock()
	envChromeRead = false
	ensureEnvChromeRead()
	return envChromeRejectedValue
}

// SetChromeMajorVersion 让 protocol（共享契约的持有者）把权威版本号推送给传输层。
// 只接受合法大版本号；即使存下推送值，读取时环境变量仍然优先（env > 推送 > 内置）。
func SetChromeMajorVersion(major string) {
	major = strings.TrimSpace(major)
	if major == "" {
		return
	}
	n, err := strconv.Atoi(major)
	if err != nil || n < 1 {
		return
	}
	chromeVersionMu.Lock()
	defer chromeVersionMu.Unlock()
	pushedChromeMajorVersion = major
}

// ChromeMajorVersion 返回当前生效的 Chrome 大版本（HTTP 层与 TLS 层共用）。
// 优先级：环境变量（含 .env）> protocol 推送的 JSON 值 > 内置回退。
func ChromeMajorVersion() string {
	chromeVersionMu.Lock()
	defer chromeVersionMu.Unlock()
	ensureEnvChromeRead()
	if envChromeMajorVersion != "" {
		return envChromeMajorVersion
	}
	if pushedChromeMajorVersion != "" {
		return pushedChromeMajorVersion
	}
	return builtinDefaultChromeMajorVersion
}

// ResolveChromePreset 把请求的 Chrome 大版本解析为实际使用的 httpcloak 预设名：
// 优先 chrome-<major>-windows；不存在则从 major 递减探测 ≤ 该版本的最新可用预设。
//
// 必须用 fingerprint.GetStrict（无回退）探测——Get 对未知名会静默回退到旧预设，
// 产生「声称 N、实为更旧」的严重矛盾，正是本函数要消除的那类缺陷。
func ResolveChromePreset(major string) string {
	n, err := strconv.Atoi(strings.TrimSpace(major))
	if err != nil || n < chromeLowestPresetMajor || n > 9999 {
		return chromeDefaultPreset
	}
	for v := n; v >= chromeLowestPresetMajor; v-- {
		name := "chrome-" + strconv.Itoa(v) + "-windows"
		if fingerprint.GetStrict(name) != nil {
			return name
		}
	}
	return chromeDefaultPreset
}

// ResolvedTLSPresetName 返回当前进程实际使用的 TLS/H2 预设名。
// 每次调用按当前生效版本解析（预设表查找很便宜），因此 protocol 在 init
// 推送版本号之后自动跟随，不存在「先缓存了旧值」的初始化顺序陷阱。
func ResolvedTLSPresetName() string {
	return ResolveChromePreset(ChromeMajorVersion())
}

// TLSChromeVersion 返回 TLS ClientHello 实际复现的 Chrome 大版本，
// 由解析出的预设名推导，因此永远与实际生效的预设一致。
// （旧实现是写死的常量 "133"，与实际生效的 chrome-150-windows 早已不符，
// 只剩 tests/wire-capture 在打印这个误导值。）
func TLSChromeVersion() string {
	return tlsVersionFromPreset(ResolvedTLSPresetName())
}

// tlsVersionFromPreset 从 "chrome-151-windows" 这类预设名中提取大版本号。
func tlsVersionFromPreset(preset string) string {
	p := strings.TrimPrefix(preset, "chrome-")
	if i := strings.IndexByte(p, '-'); i >= 0 {
		p = p[:i]
	}
	return p
}

// chromeHeaderOrder pins the request header order. httpcloak applies it on top
// of its browser preset so every request reaches chat.deepseek.com with a
// stable, Chrome-like header order.
//
// Names must be lowercase — httpcloak lowercases keys before looking them up in
// the order map, and headers absent from this list are appended afterwards in
// lexicographic order (still deterministic).
//
// This mirrors commonly observed Chrome fetch/XHR ordering. It is worth
// re-validating against a live capture of chat.deepseek.com if the upstream
// web client changes which headers it sets. （2026-08-31 用真实 Chrome 152 实测：
// 自定义头与 sec-ch-ua 组的相对顺序每次安装/会话随机，不是稳定指纹信号，
// 这里固定顺序只为可复现，不必追逐真实浏览器的逐次顺序。）
var chromeHeaderOrder = []string{
	"content-length",
	"sec-ch-ua",
	"sec-ch-ua-mobile",
	"sec-ch-ua-platform",
	"authorization",
	"x-client-bundle-id",
	"x-client-locale",
	"x-client-platform",
	"x-client-timezone-offset",
	"x-client-version",
	"x-ds-pow-response",
	"user-agent",
	"content-type",
	"accept",
	"origin",
	"sec-fetch-site",
	"sec-fetch-mode",
	"sec-fetch-dest",
	"referer",
	"accept-encoding",
	"accept-language",
	"cookie",
	"priority",
}

// ChromeHeaderOrder returns a copy of the pinned request header order, for
// tooling that needs to reproduce or diff the exact on-wire ordering.
func ChromeHeaderOrder() []string {
	return append([]string(nil), chromeHeaderOrder...)
}

// ChromePseudoHeaderOrder returns a copy of the pinned HTTP/2 pseudo-header
// order.
func ChromePseudoHeaderOrder() []string {
	return append([]string(nil), chromePseudoHeaderOrder...)
}

// chromePseudoHeaderOrder is Chrome's :method,:authority,:scheme,:path order.
var chromePseudoHeaderOrder = []string{":method", ":authority", ":scheme", ":path"}
