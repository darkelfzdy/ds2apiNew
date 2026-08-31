package protocol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	trans "ds2api/internal/deepseek/transport"
)

// TestChromeContractMatchesSharedJSON 断言运行时生效的 Chrome 大版本
// 与 constants_shared.json 里的权威值一致（跨语言单一来源的 Go 侧锚点）。
func TestChromeContractMatchesSharedJSON(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(".", "constants_shared.json"))
	if err != nil {
		t.Fatalf("read shared constants: %v", err)
	}
	var cfg sharedConstants
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal shared constants: %v", err)
	}
	if cfg.Chrome.MajorVersion == "" {
		t.Fatal("chrome.major_version missing from constants_shared.json")
	}
	if env := strings.TrimSpace(os.Getenv("DS2API_CHROME_MAJOR_VERSION")); env != "" {
		t.Skipf("DS2API_CHROME_MAJOR_VERSION=%s overrides the JSON contract", env)
	}
	if got := trans.ChromeMajorVersion(); got != cfg.Chrome.MajorVersion {
		t.Fatalf("trans.ChromeMajorVersion() = %q, want JSON %q", got, cfg.Chrome.MajorVersion)
	}
	for major, brand := range cfg.Chrome.GreaseBrands {
		if got := chromeGreaseBrands[major]; got != brand {
			t.Fatalf("grease brand for %s = %q, want JSON %q", major, got, brand)
		}
	}
}

func TestWebHeadersUseCurrentChromeVersion(t *testing.T) {
	major := trans.ChromeMajorVersion()
	h := webBrowserHeaders()
	ua := h["User-Agent"]
	if !strings.Contains(ua, "Chrome/"+major+".0.0.0") {
		t.Fatalf("User-Agent %q must advertise Chrome/%s", ua, major)
	}
	if !strings.Contains(ua, "Mozilla/5.0") || !strings.Contains(ua, "Safari/537.36") {
		t.Fatalf("unexpected User-Agent shape: %q", ua)
	}
	scu := h["sec-ch-ua"]
	if !strings.Contains(scu, "\"Chromium\";v=\""+major+"\"") || !strings.Contains(scu, "\"Google Chrome\";v=\""+major+"\"") {
		t.Fatalf("sec-ch-ua %q must carry Chromium/Google Chrome v=%s", scu, major)
	}
	if !strings.Contains(scu, ChromeGreaseBrand()) {
		t.Fatalf("sec-ch-ua %q must start with the GREASE brand %q", scu, ChromeGreaseBrand())
	}
	// UA 与 sec-ch-ua 必须报同一个大版本，否则两个头自相矛盾。
	if !strings.Contains(scu, ";v=\""+major+"\"") {
		t.Fatalf("sec-ch-ua %q disagrees with UA major %s", scu, major)
	}
}

func TestChromeGreaseBrandKnownAndFallback(t *testing.T) {
	if got := chromeGreaseBrands["151"]; got != "\"Not=A?Brand\";v=\"99\"" {
		t.Fatalf("151 GREASE = %q", got)
	}
	// ChromeMajorVersion 只接受合法版本号（env 与推送都校验），
	// 因此 ChromeGreaseBrand 的解析失败分支是纯防御；直接验证它不返回空值。
	if got := ChromeGreaseBrand(); got == "" {
		t.Fatal("ChromeGreaseBrand() returned empty")
	}
	// 防御分支本身可单独验证：回退大版本必须能算出可用串。
	if _, ok := ComputeChromeGreaseBrand(chromeGreaseFallbackMajor); !ok {
		t.Fatalf("fallback major %q is not computable", chromeGreaseFallbackMajor)
	}
}

// TestComputeChromeGreaseBrandMatchesPinnedHistory 是本次升级的核心回归位：
// 算法（Chromium 源码公式）必须逐字复现已实测/已交叉确认的历史钉值。
// 两边不一致说明要么公式移植错了，要么钉值错了。
func TestComputeChromeGreaseBrandMatchesPinnedHistory(t *testing.T) {
	for major, pinned := range chromeGreaseBrands {
		got, ok := ComputeChromeGreaseBrand(major)
		if !ok {
			t.Fatalf("ComputeChromeGreaseBrand(%q) reported unusable", major)
		}
		if got != pinned {
			t.Fatalf("major=%s: computed %q != pinned %q", major, got, pinned)
		}
	}
}

// TestComputeChromeGreaseBrandFutureVersions 固定算法对尚未发布版本的输出，
// 确保把 chrome.major_version 提到新版本时不需要人工补 GREASE 表。
func TestComputeChromeGreaseBrandFutureVersions(t *testing.T) {
	cases := map[string]string{
		// 153: 153%11=10 -> "_", 154%11=0 -> " ", 153%3=0 -> "8"
		"153": "\"Not_A Brand\";v=\"8\"",
		// 154: 154%11=0 -> " ", 155%11=1 -> "(", 154%3=1 -> "99"
		"154": "\"Not A(Brand\";v=\"99\"",
		// 155: 155%11=1 -> "(", 156%11=2 -> ":", 155%3=2 -> "24"
		"155": "\"Not(A:Brand\";v=\"24\"",
		// 149: 149%11=6 -> ")", 150%11=7 -> ";", 149%3=2 -> "24"
		"149": "\"Not)A;Brand\";v=\"24\"",
	}
	for major, want := range cases {
		got, ok := ComputeChromeGreaseBrand(major)
		if !ok {
			t.Fatalf("major=%s reported unusable", major)
		}
		if got != want {
			t.Fatalf("major=%s: got %q, want %q", major, got, want)
		}
	}
	for _, bad := range []string{"", "abc", "0", "-1"} {
		if _, ok := ComputeChromeGreaseBrand(bad); ok {
			t.Fatalf("major=%q must be rejected", bad)
		}
	}
}

// TestChromeGreaseBrandComputesUnknownMajor 验证升版不依赖手维护表。
func TestChromeGreaseBrandComputesUnknownMajor(t *testing.T) {
	orig := trans.ChromeMajorVersion()
	defer trans.SetChromeMajorVersion(orig)
	if _, exists := chromeGreaseBrands["153"]; exists {
		t.Skip("153 is pinned in the contract; nothing to compute")
	}
	trans.SetChromeMajorVersion("153")
	want, _ := ComputeChromeGreaseBrand("153")
	if got := ChromeGreaseBrand(); got != want {
		t.Fatalf("ChromeGreaseBrand() for unpinned 153 = %q, want computed %q", got, want)
	}
	// 算出来的串必须带上报的自身大版本，与 sec-ch-ua 其余部分不自相矛盾。
	scu := chromeSecChUA()
	if !strings.Contains(scu, "\"Chromium\";v=\"153\"") || !strings.Contains(scu, want) {
		t.Fatalf("sec-ch-ua for 153 = %q", scu)
	}
}

func TestBaseHeadersWebPlatformConsistent(t *testing.T) {
	h := BaseHeaders
	major := trans.ChromeMajorVersion()
	if !strings.Contains(h["User-Agent"], "Chrome/"+major) {
		t.Fatalf("BaseHeaders UA = %q, want Chrome/%s", h["User-Agent"], major)
	}
	if h["x-client-version"] != ClientVersion {
		t.Fatalf("x-client-version = %q, want ClientVersion %q", h["x-client-version"], ClientVersion)
	}
}
