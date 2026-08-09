package mihomo

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"ds2api/internal/config"
)

func testConfigWithBinding() config.Config {
	nodeKey := config.MihomoNodeKey("sub-1", "香港 01")
	proxyID := config.MihomoManagedProxyID(nodeKey)
	return config.Config{
		Accounts: []config.Account{
			{Email: "a@test.com", ProxyID: proxyID},
			{Email: "b@test.com"},
		},
		Proxies: []config.Proxy{
			{ID: proxyID, Name: "香港 01", Type: "socks5", Host: "127.0.0.1", Port: 10801},
		},
		Mihomo: config.MihomoConfig{
			Enabled:  true,
			BasePort: config.DefaultMihomoBasePort,
			APIPort:  config.DefaultMihomoAPIPort,
			Subscriptions: []config.MihomoSubscription{
				{
					ID:  "sub-1",
					URL: "https://example.com/sub",
					Nodes: []config.MihomoNode{
						{Name: "香港 01", Type: "ss", Raw: map[string]any{
							"name": "香港 01", "type": "ss", "server": "hk.example.com",
							"port": 8388, "cipher": "aes-128-gcm", "password": "pw",
						}},
						{Name: "日本 01", Type: "vmess", Raw: map[string]any{
							"name": "日本 01", "type": "vmess", "server": "jp.example.com",
							"port": 443, "uuid": "u-u-i-d",
						}},
					},
				},
			},
			PortMap: map[string]int{nodeKey: 10801},
		},
	}
}

func TestBuildRuntimeYAMLWithBinding(t *testing.T) {
	cfg := testConfigWithBinding()
	out, bindings, err := BuildRuntimeYAML(cfg)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if len(bindings) != 1 || bindings[0].Port != 10801 || bindings[0].NodeName != "香港 01" {
		t.Fatalf("unexpected bindings: %+v", bindings)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("generated yaml invalid: %v\n%s", err, out)
	}
	if got := doc["external-controller"]; got != "127.0.0.1:19090" {
		t.Fatalf("external-controller mismatch: %v", got)
	}
	proxies, ok := doc["proxies"].([]any)
	if !ok || len(proxies) != 2 {
		t.Fatalf("expected 2 proxies, got %v", doc["proxies"])
	}
	listeners, ok := doc["listeners"].([]any)
	if !ok || len(listeners) != 1 {
		t.Fatalf("expected 1 listener, got %v", doc["listeners"])
	}
	listener := listeners[0].(map[string]any)
	if listener["port"] != 10801 || listener["proxy"] != "香港 01" || listener["type"] != "socks" || listener["listen"] != "127.0.0.1" {
		t.Fatalf("listener mismatch: %v", listener)
	}
}

func TestBuildRuntimeYAMLNoBindings(t *testing.T) {
	cfg := testConfigWithBinding()
	// 去掉账号引用后，不应再生成 listener。
	cfg.Accounts[0].ProxyID = ""
	out, bindings, err := BuildRuntimeYAML(cfg)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if len(bindings) != 0 {
		t.Fatalf("expected no bindings, got %+v", bindings)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("generated yaml invalid: %v\n%s", err, out)
	}
	if listeners, ok := doc["listeners"].([]any); !ok || len(listeners) != 0 {
		t.Fatalf("expected empty listeners, got %v", doc["listeners"])
	}
	// 节点列表仍完整输出（与绑定无关）。
	if proxies, ok := doc["proxies"].([]any); !ok || len(proxies) != 2 {
		t.Fatalf("expected 2 proxies, got %v", doc["proxies"])
	}
}

func TestCollectActiveBindingsSkipsStaleState(t *testing.T) {
	cfg := testConfigWithBinding()

	// 托管代理被手工删除：不再生成 listener。
	withoutProxy := cfg.Clone()
	withoutProxy.Proxies = nil
	if bindings := collectActiveBindings(withoutProxy); len(bindings) != 0 {
		t.Fatalf("expected no bindings when managed proxy missing, got %+v", bindings)
	}

	// 节点从订阅中消失：不再生成 listener。
	withoutNode := cfg.Clone()
	withoutNode.Mihomo.Subscriptions[0].Nodes = withoutNode.Mihomo.Subscriptions[0].Nodes[1:]
	if bindings := collectActiveBindings(withoutNode); len(bindings) != 0 {
		t.Fatalf("expected no bindings when node missing, got %+v", bindings)
	}

	// sanity：原始配置仍能产出绑定。
	if bindings := collectActiveBindings(cfg); len(bindings) != 1 {
		t.Fatalf("expected 1 binding, got %+v", bindings)
	}
}

func TestAllocateMihomoPortSkipsUsed(t *testing.T) {
	m := config.MihomoConfig{BasePort: config.DefaultMihomoBasePort}
	p1 := m.AllocateMihomoPort("sub-1::A")
	p2 := m.AllocateMihomoPort("sub-1::B")
	if p1 != config.DefaultMihomoBasePort || p2 != config.DefaultMihomoBasePort+1 {
		t.Fatalf("unexpected allocation: %d, %d", p1, p2)
	}
	if again := m.AllocateMihomoPort("sub-1::A"); again != p1 {
		t.Fatalf("allocation not stable: %d vs %d", again, p1)
	}
	if !strings.HasPrefix(config.MihomoManagedProxyID("sub-1::A"), config.MihomoManagedProxyPrefix) {
		t.Fatal("managed proxy id prefix mismatch")
	}
}
