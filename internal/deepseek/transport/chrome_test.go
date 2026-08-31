package transport

import (
	"strings"
	"testing"
)

func TestResolveChromePresetExactMatch(t *testing.T) {
	if got := ResolveChromePreset("151"); got != "chrome-151-windows" {
		t.Fatalf("ResolveChromePreset(151) = %q, want chrome-151-windows", got)
	}
	if got := ResolveChromePreset("150"); got != "chrome-150-windows" {
		t.Fatalf("ResolveChromePreset(150) = %q, want chrome-150-windows", got)
	}
}

func TestResolveChromePresetFallsDownToLatestAvailable(t *testing.T) {
	// 152 has no preset in httpcloak v1.6.11; must resolve to chrome-151-windows
	// (newest available <= 152), never to a contradictory older/newer one.
	if got := ResolveChromePreset("152"); got != "chrome-151-windows" {
		t.Fatalf("ResolveChromePreset(152) = %q, want chrome-151-windows", got)
	}
}

func TestReadEnvChromeMajorVersionValidation(t *testing.T) {
	cases := []struct {
		raw      string
		want     string
		rejected string
	}{
		{"", "", ""},
		{"151", "151", ""},
		{" 152 ", "152", ""},
		{"abc", "", "abc"},
		{"15x", "", "15x"},
		{"1", "", "1"},
		{"132", "", "132"},
		{"1000", "", "1000"},
	}
	for _, tc := range cases {
		t.Setenv("DS2API_CHROME_MAJOR_VERSION", tc.raw)
		got, rejected := readEnvChromeMajorVersion()
		if got != tc.want || rejected != tc.rejected {
			t.Fatalf("raw=%q got=%q rejected=%q, want got=%q rejected=%q", tc.raw, got, rejected, tc.want, tc.rejected)
		}
	}
}

func TestSetChromeMajorVersionRejectsGarbage(t *testing.T) {
	defer func(prev string) { pushedChromeMajorVersion = prev }(pushedChromeMajorVersion)
	pushedChromeMajorVersion = ""
	SetChromeMajorVersion("abc")
	if pushedChromeMajorVersion != "" {
		t.Fatalf("garbage push must be ignored, got %q", pushedChromeMajorVersion)
	}
	SetChromeMajorVersion("150")
	if pushedChromeMajorVersion != "150" {
		t.Fatalf("valid push not stored, got %q", pushedChromeMajorVersion)
	}
}

func TestResolveChromePresetInvalidMajorUsesDefault(t *testing.T) {
	// Invalid/too-low major must not panic and must yield a usable preset.
	for _, major := range []string{"", "abc", "1", "132"} {
		if got := ResolveChromePreset(major); got != chromeDefaultPreset {
			t.Fatalf("ResolveChromePreset(%q) = %q, want %q", major, got, chromeDefaultPreset)
		}
	}
}

func TestChromeMajorVersionPrecedence(t *testing.T) {
	// 环境变量 > protocol 推送值 > 内置回退。环境变量惰性读取（.env 在 main 里才加载），
	// 先触发一次读取再判断是否被设置。
	_ = ChromeMajorVersion()
	if envChromeMajorVersion == "" {
		defer func(prev string) { pushedChromeMajorVersion = prev }(pushedChromeMajorVersion)
		pushedChromeMajorVersion = ""
		if got := ChromeMajorVersion(); got != builtinDefaultChromeMajorVersion {
			t.Fatalf("no env/no push -> %q, want builtin %q", got, builtinDefaultChromeMajorVersion)
		}
		SetChromeMajorVersion("150")
		if got := ChromeMajorVersion(); got != "150" {
			t.Fatalf("pushed 150 -> %q", got)
		}
		if got := ResolvedTLSPresetName(); got != "chrome-150-windows" {
			t.Fatalf("ResolvedTLSPresetName after push = %q, want chrome-150-windows", got)
		}
		if got := TLSChromeVersion(); got != "150" {
			t.Fatalf("TLSChromeVersion after push = %q, want 150", got)
		}
		// 空推送值不得清空当前版本。
		SetChromeMajorVersion("  ")
		if got := ChromeMajorVersion(); got != "150" {
			t.Fatalf("empty push must not clear version, got %q", got)
		}
	}
}

func TestTLSVersionTracksResolvedPreset(t *testing.T) {
	// TLS 版本必须永远等于实际解析出的预设里的版本号（旧实现写死 133 的缺陷回归位）。
	preset := ResolvedTLSPresetName()
	if got, want := TLSChromeVersion(), tlsVersionFromPreset(preset); got != want {
		t.Fatalf("TLSChromeVersion() = %q, want %q (preset %q)", got, want, preset)
	}
	if !strings.HasPrefix(preset, "chrome-") || !strings.HasSuffix(preset, "-windows") {
		t.Fatalf("unexpected preset name %q", preset)
	}
}

func TestTLSVersionFromPreset(t *testing.T) {
	cases := map[string]string{
		"chrome-151-windows": "151",
		"chrome-150":         "150",
		"chrome-133":         "133",
	}
	for preset, want := range cases {
		if got := tlsVersionFromPreset(preset); got != want {
			t.Fatalf("tlsVersionFromPreset(%q) = %q, want %q", preset, got, want)
		}
	}
}
