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
	// 未知大版本必须回退到最新已知（chromeGreaseFallbackMajor），不能发空值。
	orig := trans.ChromeMajorVersion()
	defer trans.SetChromeMajorVersion(orig)
	trans.SetChromeMajorVersion("199")
	want := chromeGreaseBrands[chromeGreaseFallbackMajor]
	if got := ChromeGreaseBrand(); got != want || got == "" {
		t.Fatalf("unknown major: ChromeGreaseBrand() = %q, want fallback %q", got, want)
	}
	// 回退后 sec-ch-ua 仍带上报的自身版本号，两个头不自相矛盾。
	scu := chromeSecChUA()
	if !strings.Contains(scu, "\"Chromium\";v=\"199\"") || !strings.Contains(scu, want) {
		t.Fatalf("sec-ch-ua after fallback = %q", scu)
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
