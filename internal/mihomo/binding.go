package mihomo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"ds2api/internal/config"
)

// accountMatches 复刻 admin/shared 的账号匹配规则（邮箱精确匹配 /
// 手机号归一化匹配 / Identifier 匹配），保持绑定入口行为一致。
func accountMatches(acc config.Account, identifier string) bool {
	id := strings.TrimSpace(identifier)
	if id == "" {
		return false
	}
	if strings.TrimSpace(acc.Email) == id {
		return true
	}
	if mobileKey := config.CanonicalMobileKey(id); mobileKey != "" && mobileKey == config.CanonicalMobileKey(acc.Mobile) {
		return true
	}
	return acc.Identifier() == id
}

// validateMutation 与代理管理接口使用同一套校验，保证配置始终可加载。
func validateMutation(c *config.Config) error {
	if err := config.ValidateMihomoConfig(c.Mihomo); err != nil {
		return err
	}
	if err := config.ValidateProxyConfig(c.Proxies); err != nil {
		return err
	}
	return config.ValidateAccountProxyReferences(c.Accounts, c.Proxies)
}

// gcLocked 回收三类悬空状态（在 Store.Update 事务内运行）：
//  1. 节点已从订阅中消失的端口分配
//  2. 没有任何账号引用的托管代理（mihomo- 前缀）
//  3. 引用已删除托管代理的账号绑定，及无引用的端口分配
func gcLocked(c *config.Config) {
	for nodeKey := range c.Mihomo.PortMap {
		if _, ok := c.Mihomo.FindMihomoNode(nodeKey); !ok {
			delete(c.Mihomo.PortMap, nodeKey)
		}
	}
	used := map[string]struct{}{}
	for _, acc := range c.Accounts {
		if id := strings.TrimSpace(acc.ProxyID); config.IsMihomoManagedProxyID(id) {
			used[id] = struct{}{}
		}
	}
	kept := c.Proxies[:0]
	for _, proxy := range c.Proxies {
		id := strings.TrimSpace(proxy.ID)
		if config.IsMihomoManagedProxyID(id) {
			if _, ok := used[id]; !ok {
				continue // 无账号引用的托管代理回收
			}
		}
		kept = append(kept, proxy)
	}
	c.Proxies = kept
	existing := map[string]struct{}{}
	for _, proxy := range c.Proxies {
		existing[strings.TrimSpace(proxy.ID)] = struct{}{}
	}
	for i := range c.Accounts {
		id := strings.TrimSpace(c.Accounts[i].ProxyID)
		if config.IsMihomoManagedProxyID(id) {
			if _, ok := existing[id]; !ok {
				c.Accounts[i].ProxyID = ""
			}
		}
	}
	for nodeKey := range c.Mihomo.PortMap {
		proxyID := config.MihomoManagedProxyID(nodeKey)
		if _, ok := existing[proxyID]; !ok {
			delete(c.Mihomo.PortMap, nodeKey)
		}
	}
}

// upsertManagedProxyLocked 创建或更新节点对应的托管代理（socks5://127.0.0.1:port）。
func upsertManagedProxyLocked(c *config.Config, nodeKey, nodeName string, port int) config.Proxy {
	proxyID := config.MihomoManagedProxyID(nodeKey)
	for i, proxy := range c.Proxies {
		if strings.TrimSpace(proxy.ID) == proxyID {
			c.Proxies[i].Name = nodeName
			c.Proxies[i].Type = "socks5"
			c.Proxies[i].Host = "127.0.0.1"
			c.Proxies[i].Port = port
			return c.Proxies[i]
		}
	}
	proxy := config.Proxy{
		ID:   proxyID,
		Name: nodeName,
		Type: "socks5",
		Host: "127.0.0.1",
		Port: port,
	}
	c.Proxies = append(c.Proxies, proxy)
	return proxy
}

// BindAccount 把账号绑定到节点（nodeKey 为空字符串表示解绑）。
// 事务内完成端口分配、托管代理 upsert、账号 ProxyID 更新与 GC，
// 成功后重置账号池并重启 mihomo 使监听端口生效。
func (m *Manager) BindAccount(_ context.Context, identifier, nodeKey string) error {
	if m == nil || m.store == nil {
		return errors.New("mihomo manager 未初始化")
	}
	identifier = strings.TrimSpace(identifier)
	nodeKey = strings.TrimSpace(nodeKey)
	err := m.store.Update(func(c *config.Config) error {
		idx := -1
		for i, acc := range c.Accounts {
			if accountMatches(acc, identifier) {
				idx = i
				break
			}
		}
		if idx < 0 {
			return errors.New("账号不存在")
		}
		if nodeKey == "" {
			c.Accounts[idx].ProxyID = ""
			gcLocked(c)
			return validateMutation(c)
		}
		node, ok := c.Mihomo.FindMihomoNode(nodeKey)
		if !ok {
			return errors.New("节点不存在（订阅可能已被删除），请先刷新节点列表")
		}
		port := c.Mihomo.AllocateMihomoPort(nodeKey)
		proxy := upsertManagedProxyLocked(c, nodeKey, node.Name, port)
		c.Accounts[idx].ProxyID = proxy.ID
		gcLocked(c)
		return validateMutation(c)
	})
	if err != nil {
		return err
	}
	m.resetPool()
	return m.Apply(context.Background())
}

// AddSubscription 抓取并保存一个新订阅，返回订阅 ID。
func (m *Manager) AddSubscription(ctx context.Context, name, rawURL string) (config.MihomoSubscription, error) {
	if m == nil || m.store == nil {
		return config.MihomoSubscription{}, errors.New("mihomo manager 未初始化")
	}
	name = strings.TrimSpace(name)
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return config.MihomoSubscription{}, errors.New("订阅链接不能为空")
	}
	// 网络抓取在 Store 锁外执行，避免阻塞配置读写。
	nodes, err := FetchSubscription(ctx, rawURL)
	if err != nil {
		return config.MihomoSubscription{}, err
	}
	sub := config.MihomoSubscription{
		ID:        "sub-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12],
		Name:      name,
		URL:       rawURL,
		UpdatedAt: time.Now().Unix(),
		Nodes:     nodes,
	}
	if sub.Name == "" {
		sub.Name = rawURL
	}
	if err := m.store.Update(func(c *config.Config) error {
		c.Mihomo.Subscriptions = append(c.Mihomo.Subscriptions, sub)
		return validateMutation(c)
	}); err != nil {
		return config.MihomoSubscription{}, err
	}
	return sub, m.Apply(context.Background())
}

// RefreshSubscription 重新抓取订阅节点；节点集合变化后执行 GC
// （消失节点的绑定会被解除并回收端口）。
func (m *Manager) RefreshSubscription(ctx context.Context, subID string) (int, error) {
	if m == nil || m.store == nil {
		return 0, errors.New("mihomo manager 未初始化")
	}
	subID = strings.TrimSpace(subID)
	snap := m.store.Snapshot()
	var rawURL string
	for _, sub := range snap.Mihomo.Subscriptions {
		if sub.ID == subID {
			rawURL = sub.URL
			break
		}
	}
	if rawURL == "" {
		return 0, errors.New("订阅不存在")
	}
	nodes, err := FetchSubscription(ctx, rawURL)
	if err != nil {
		return 0, err
	}
	if err := m.store.Update(func(c *config.Config) error {
		for i := range c.Mihomo.Subscriptions {
			if c.Mihomo.Subscriptions[i].ID != subID {
				continue
			}
			c.Mihomo.Subscriptions[i].Nodes = nodes
			c.Mihomo.Subscriptions[i].UpdatedAt = time.Now().Unix()
			gcLocked(c)
			return validateMutation(c)
		}
		return errors.New("订阅不存在")
	}); err != nil {
		return 0, err
	}
	m.resetPool()
	return len(nodes), m.Apply(context.Background())
}

// DeleteSubscription 删除订阅并解除其节点的全部绑定。
func (m *Manager) DeleteSubscription(_ context.Context, subID string) error {
	if m == nil || m.store == nil {
		return errors.New("mihomo manager 未初始化")
	}
	subID = strings.TrimSpace(subID)
	err := m.store.Update(func(c *config.Config) error {
		idx := -1
		for i, sub := range c.Mihomo.Subscriptions {
			if sub.ID == subID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return errors.New("订阅不存在")
		}
		c.Mihomo.Subscriptions = append(c.Mihomo.Subscriptions[:idx], c.Mihomo.Subscriptions[idx+1:]...)
		// 解除引用该订阅节点的账号绑定（节点已随订阅消失）。
		prefix := subID + "::"
		deadProxies := map[string]struct{}{}
		for nodeKey := range c.Mihomo.PortMap {
			if strings.HasPrefix(nodeKey, prefix) {
				deadProxies[config.MihomoManagedProxyID(nodeKey)] = struct{}{}
			}
		}
		for i := range c.Accounts {
			if _, dead := deadProxies[strings.TrimSpace(c.Accounts[i].ProxyID)]; dead {
				c.Accounts[i].ProxyID = ""
			}
		}
		gcLocked(c)
		return validateMutation(c)
	})
	if err != nil {
		return err
	}
	m.resetPool()
	return m.Apply(context.Background())
}

// UpdateSettings 更新桥开关/二进制路径/端口设置并立即应用。
func (m *Manager) UpdateSettings(_ context.Context, enabled bool, binaryPath string, basePort, apiPort int) error {
	if m == nil || m.store == nil {
		return errors.New("mihomo manager 未初始化")
	}
	err := m.store.Update(func(c *config.Config) error {
		c.Mihomo.Enabled = enabled
		c.Mihomo.BinaryPath = strings.TrimSpace(binaryPath)
		if basePort > 0 {
			if basePort != c.Mihomo.BasePort && len(c.Mihomo.PortMap) > 0 {
				return fmt.Errorf("已存在端口分配（%d 个），更换 base_port 前请先解除所有账号绑定", len(c.Mihomo.PortMap))
			}
			c.Mihomo.BasePort = basePort
		}
		if apiPort > 0 {
			c.Mihomo.APIPort = apiPort
		}
		c.Mihomo = config.NormalizeMihomoConfig(c.Mihomo)
		return validateMutation(c)
	})
	if err != nil {
		return err
	}
	return m.Apply(context.Background())
}

// ListNodes 汇总全部订阅节点及其绑定/端口状态，供管理界面展示。
func (m *Manager) ListNodes() []map[string]any {
	if m == nil || m.store == nil {
		return nil
	}
	snap := m.store.Snapshot()
	// accounts 同时携带 identifier（解绑操作的入参）与 label（展示），
	// 避免前端从展示文本里反解析账号标识。
	accountsByProxy := map[string][]map[string]string{}
	for _, acc := range snap.Accounts {
		id := strings.TrimSpace(acc.ProxyID)
		if config.IsMihomoManagedProxyID(id) {
			identifier := acc.Identifier()
			label := identifier
			if strings.TrimSpace(acc.Name) != "" {
				label = strings.TrimSpace(acc.Name) + " (" + identifier + ")"
			}
			accountsByProxy[id] = append(accountsByProxy[id], map[string]string{
				"identifier": identifier,
				"label":      label,
			})
		}
	}
	out := []map[string]any{}
	for _, sub := range snap.Mihomo.Subscriptions {
		for _, node := range sub.Nodes {
			nodeKey := config.MihomoNodeKey(sub.ID, node.Name)
			proxyID := config.MihomoManagedProxyID(nodeKey)
			port := snap.Mihomo.PortMap[nodeKey]
			server := ""
			if v, ok := node.Raw["server"].(string); ok {
				server = v
			}
			out = append(out, map[string]any{
				"node_key":        nodeKey,
				"name":            node.Name,
				"type":            node.Type,
				"server":          server,
				"subscription":    sub.Name,
				"subscription_id": sub.ID,
				"local_port":      port,
				"proxy_id":        proxyID,
				"accounts":        accountsByProxy[proxyID],
			})
		}
	}
	return out
}

func (m *Manager) resetPool() {
	if m.pool != nil {
		m.pool.Reset()
	}
}
