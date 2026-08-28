package mihomo

import (
	"context"
	"strings"
	"testing"

	"ds2api/internal/config"
)

// TestUpdateSettingsNodeExclude 覆盖管理台设置接口的 node_exclude 语义：
// nil = 未提供保持原值；非 nil = 整体替换并立即重滤节点池。
func TestUpdateSettingsNodeExclude(t *testing.T) {
	store := newAutoBindTestStore(t)
	mgr := NewManager(store, nil)
	ctx := context.Background()
	usKey := config.MihomoNodeKey("sub-1", "美国 01")

	listNodesHas := func(key string) bool {
		for _, item := range mgr.ListNodes() {
			if item["node_key"] == key {
				return true
			}
		}
		return false
	}

	// 初始三个节点均可见。
	if !listNodesHas(usKey) {
		t.Fatalf("expected US node present before exclusion")
	}

	// 1) 不携带 node_exclude（nil）：既有值保持不变。
	if err := mgr.UpdateSettings(ctx, false, "", 0, 0, false, []string{"美国"}); err != nil {
		t.Fatalf("set exclude: %v", err)
	}
	if err := mgr.UpdateSettings(ctx, false, "", 0, 0, false, nil); err != nil {
		t.Fatalf("update without exclude: %v", err)
	}
	if got := store.Snapshot().Mihomo.NodeExclude; len(got) != 1 || got[0] != "美国" {
		t.Fatalf("expected exclude preserved on nil, got %v", got)
	}

	// 2) 更新关键字后订阅缓存立即重滤，被排除节点退出节点池。
	if err := mgr.UpdateSettings(ctx, false, "", 0, 0, false, []string{" 美国 ", ""}); err != nil {
		t.Fatalf("update exclude: %v", err)
	}
	snap := store.Snapshot()
	if got := snap.Mihomo.NodeExclude; len(got) != 1 || got[0] != "美国" {
		t.Fatalf("expected normalized exclude [美国], got %v", got)
	}
	for _, sub := range snap.Mihomo.Subscriptions {
		for _, node := range sub.Nodes {
			if strings.Contains(node.Name, "美国") {
				t.Fatalf("excluded node %q remained in cached subscription", node.Name)
			}
		}
	}
	if listNodesHas(usKey) {
		t.Fatalf("excluded node still listed by ListNodes")
	}
	if got := mgr.Status()["node_exclude"]; len(got.([]string)) != 1 {
		t.Fatalf("expected status node_exclude with 1 entry, got %v", got)
	}

	// 3) 显式传空数组清空：排除列表立即清空；但此前被过滤掉的节点已从
	// 订阅缓存移除（落库前过滤语义），需下次刷新订阅才会回到节点池。
	if err := mgr.UpdateSettings(ctx, false, "", 0, 0, false, []string{}); err != nil {
		t.Fatalf("clear exclude: %v", err)
	}
	if got := store.Snapshot().Mihomo.NodeExclude; len(got) != 0 {
		t.Fatalf("expected cleared exclude, got %v", got)
	}
	if got := mgr.Status()["node_exclude"].([]string); len(got) != 0 {
		t.Fatalf("expected empty status node_exclude, got %v", got)
	}
	if listNodesHas(usKey) {
		t.Fatalf("filtered-out node cannot return before subscription refresh")
	}
}
