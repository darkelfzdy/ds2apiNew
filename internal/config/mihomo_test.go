package config

import (
	"reflect"
	"testing"
)

func TestFilterExcludedNodes(t *testing.T) {
	nodes := []MihomoNode{
		{Name: "🇭🇰香港专线01|BGP|住宅IP"},
		{Name: "🇺🇸美国01|流媒体|0.1x"},
		{Name: "🇺🇸美国高速01|CTCU|0.1x"},
		{Name: "🇬🇧英国伦敦01|CTCU|0.1x"},
		{Name: "🇯🇵日本专线01|BGP|流媒体"},
	}

	t.Run("无排除列表时原样返回", func(t *testing.T) {
		got := FilterExcludedNodes(nodes, nil)
		if !reflect.DeepEqual(got, nodes) {
			t.Fatalf("expected same slice, got %v", got)
		}
		if len(got) != 5 {
			t.Fatalf("expected 5 nodes, got %d", len(got))
		}
	})

	t.Run("按关键字剔除美国/英国节点", func(t *testing.T) {
		got := FilterExcludedNodes(nodes, []string{"🇺🇸", "🇬🇧"})
		if len(got) != 2 {
			t.Fatalf("expected 2 kept, got %d: %v", len(got), got)
		}
		for _, n := range got {
			if n.Name == "🇺🇸美国01|流媒体|0.1x" || n.Name == "🇺🇸美国高速01|CTCU|0.1x" ||
				n.Name == "🇬🇧英国伦敦01|CTCU|0.1x" {
				t.Fatalf("excluded node leaked: %s", n.Name)
			}
		}
	})

	t.Run("不修改输入切片底层数组", func(t *testing.T) {
		src := append([]MihomoNode(nil), nodes...)
		_ = FilterExcludedNodes(src, []string{"🇺🇸"})
		if !reflect.DeepEqual(src, nodes) {
			t.Fatalf("input slice mutated")
		}
	})

	t.Run("空关键字被忽略", func(t *testing.T) {
		got := FilterExcludedNodes(nodes, []string{"", "🇯🇵"})
		if len(got) != 4 {
			t.Fatalf("expected 4 kept (empty kw ignored), got %d: %v", len(got), got)
		}
	})

	t.Run("全部被排除时返回空", func(t *testing.T) {
		got := FilterExcludedNodes(nodes, []string{"|"})
		if len(got) != 0 {
			t.Fatalf("expected empty, got %d", len(got))
		}
	})
}

func TestMihomoNodeExcludeRoundTrip(t *testing.T) {
	// MarshalJSON 必须带上 node_exclude，否则配置无法持久化。
	m := MihomoConfig{
		Enabled:     true,
		NodeExclude: []string{"🇺🇸", "🇬🇧"},
	}
	b, err := m.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	for _, kw := range []string{"node_exclude", "🇺🇸", "🇬🇧"} {
		if !containsStr(got, kw) {
			t.Fatalf("marshal output missing %q: %s", kw, got)
		}
	}
}

func TestNormalizeMihomoConfigTrimsNodeExclude(t *testing.T) {
	m := NormalizeMihomoConfig(MihomoConfig{NodeExclude: []string{" 🇺🇸 ", "", "🇬🇧"}})
	if !reflect.DeepEqual(m.NodeExclude, []string{"🇺🇸", "🇬🇧"}) {
		t.Fatalf("expected trimmed non-empty, got %v", m.NodeExclude)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
