package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMihomoConfigMarshalOmitsSubscriptions(t *testing.T) {
	cfg := Config{
		Mihomo: MihomoConfig{
			Enabled:    true,
			BinaryPath: "/usr/bin/mihomo",
			BasePort:   DefaultMihomoBasePort,
			APIPort:    DefaultMihomoAPIPort,
			Subscriptions: []MihomoSubscription{
				{ID: "sub-1", Name: "机场", URL: "https://example.com/sub", Nodes: []MihomoNode{{Name: "节点", Type: "ss"}}},
			},
			PortMap: map[string]int{"sub-1::节点": 10801},
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	raw := string(b)
	if strings.Contains(raw, "subscriptions") || strings.Contains(raw, "port_map") {
		t.Fatalf("expected subscriptions/port_map omitted from config.json serialization, got: %s", raw)
	}
	if !strings.Contains(raw, `"enabled":true`) {
		t.Fatalf("expected enabled=true kept in serialization, got: %s", raw)
	}

	// Unmarshal 仍兼容旧式 subscriptions/port_map。
	var decoded Config
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(decoded.Mihomo.Subscriptions) != 0 {
		t.Fatalf("expected no subscriptions after roundtrip (omitted), got %+v", decoded.Mihomo.Subscriptions)
	}

	legacy := `{"mihomo":{"enabled":true,"subscriptions":[{"id":"sub-1","url":"https://example.com/sub"}],"port_map":{"sub-1::x":10801}}}`
	var legacyCfg Config
	if err := json.Unmarshal([]byte(legacy), &legacyCfg); err != nil {
		t.Fatalf("unmarshal legacy error: %v", err)
	}
	if len(legacyCfg.Mihomo.Subscriptions) != 1 || legacyCfg.Mihomo.Subscriptions[0].ID != "sub-1" {
		t.Fatalf("legacy subscriptions not read: %+v", legacyCfg.Mihomo.Subscriptions)
	}
	if legacyCfg.Mihomo.PortMap["sub-1::x"] != 10801 {
		t.Fatalf("legacy port_map not read: %+v", legacyCfg.Mihomo.PortMap)
	}
}

func TestMihomoSubscriptionsPersistToSeparateFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	t.Setenv("DS2API_CONFIG_JSON", "")
	t.Setenv("DS2API_CONFIG_PATH", path)

	store := LoadStore()
	if err := store.Update(func(c *Config) error {
		c.Mihomo.Subscriptions = []MihomoSubscription{
			{ID: "sub-1", Name: "机场", URL: "https://example.com/sub", Nodes: []MihomoNode{{Name: "节点", Type: "ss"}}},
		}
		c.Mihomo.PortMap = map[string]int{"sub-1::节点": DefaultMihomoBasePort}
		return nil
	}); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	// config.json 不应包含订阅。
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(content), "subscriptions") {
		t.Fatalf("config.json should not contain subscriptions, got: %s", content)
	}

	// 独立文件应包含订阅与端口映射。
	subsPath := filepath.Join(dir, "mihomo_subscriptions.json")
	subsContent, err := os.ReadFile(subsPath)
	if err != nil {
		t.Fatalf("read subscriptions file: %v", err)
	}
	if !strings.Contains(string(subsContent), "sub-1") || !strings.Contains(string(subsContent), "port_map") {
		t.Fatalf("subscriptions file content mismatch: %s", subsContent)
	}

	// 重新加载后订阅仍可用。
	reloaded := LoadStore()
	snap := reloaded.Snapshot()
	if len(snap.Mihomo.Subscriptions) != 1 || snap.Mihomo.Subscriptions[0].ID != "sub-1" {
		t.Fatalf("subscriptions lost after reload: %+v", snap.Mihomo.Subscriptions)
	}
	if snap.Mihomo.PortMap["sub-1::节点"] != DefaultMihomoBasePort {
		t.Fatalf("port_map lost after reload: %+v", snap.Mihomo.PortMap)
	}
}

func TestMihomoSubscriptionsMigratedFromLegacyConfigJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	legacy := `{
		"keys": ["k1"],
		"mihomo": {
			"enabled": false,
			"subscriptions": [{"id": "sub-legacy", "name": "旧机场", "url": "https://example.com/sub", "nodes": [{"name": "香港", "type": "ss"}]}],
			"port_map": {"sub-legacy::香港": 10801}
		}
	}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}
	t.Setenv("DS2API_CONFIG_JSON", "")
	t.Setenv("DS2API_CONFIG_PATH", path)

	store := LoadStore()
	snap := store.Snapshot()
	if len(snap.Mihomo.Subscriptions) != 1 || snap.Mihomo.Subscriptions[0].ID != "sub-legacy" {
		t.Fatalf("legacy subscriptions not loaded: %+v", snap.Mihomo.Subscriptions)
	}

	// 触发一次保存（模拟增删订阅后的迁移写入）。
	if err := store.Update(func(c *Config) error {
		c.Mihomo.Subscriptions[0].Name = "机场（已迁移）"
		return nil
	}); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(content), "subscriptions") {
		t.Fatalf("legacy subscriptions should be migrated out of config.json, got: %s", content)
	}
	if _, err := os.Stat(filepath.Join(dir, "mihomo_subscriptions.json")); err != nil {
		t.Fatalf("migrated subscriptions file missing: %v", err)
	}

	reloaded := LoadStore()
	if got := reloaded.Snapshot().Mihomo.Subscriptions[0].Name; got != "机场（已迁移）" {
		t.Fatalf("migrated name not preserved, got %q", got)
	}
}

func TestMihomoSubscriptionsFileRemovedWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	t.Setenv("DS2API_CONFIG_JSON", "")
	t.Setenv("DS2API_CONFIG_PATH", path)

	store := LoadStore()
	if err := store.Update(func(c *Config) error {
		c.Mihomo.Subscriptions = []MihomoSubscription{{ID: "sub-1", URL: "https://example.com/sub"}}
		return nil
	}); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	subsPath := filepath.Join(dir, "mihomo_subscriptions.json")
	if _, err := os.Stat(subsPath); err != nil {
		t.Fatalf("subscriptions file should exist: %v", err)
	}

	if err := store.Update(func(c *Config) error {
		c.Mihomo.Subscriptions = nil
		c.Mihomo.PortMap = nil
		return nil
	}); err != nil {
		t.Fatalf("clear failed: %v", err)
	}
	if _, err := os.Stat(subsPath); !os.IsNotExist(err) {
		t.Fatalf("subscriptions file should be removed when empty, err=%v", err)
	}
}
