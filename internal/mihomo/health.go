package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"ds2api/internal/config"
)

// 健康状态常量。unknown 表示尚未测速，视为“可用但不保证”。
const (
	NodeHealthUnknown = "unknown"
	NodeHealthOK      = "ok"
	NodeHealthFail    = "fail"
)

// NodeHealth 是单个节点最近一次测速的结果。FailStreak 记录连续失败次数，
// 供自动故障转移的阈值判断，避免单次抖动就把账号切走。
type NodeHealth struct {
	Status     string `json:"status"`
	LatencyMS  int    `json:"latency_ms"`
	Error      string `json:"error,omitempty"`
	TestedAt   int64  `json:"tested_at"`
	FailStreak int    `json:"-"`
}

// nodeRef 引用一个订阅节点。Key 是 "subID::nodeName"（既是 nodeKey，
// 也是 mihomo 运行时 proxy 的限定名），Name 是展示用的节点名。
type nodeRef struct {
	Key  string
	Name string
}

// 测速相关默认值，均可用环境变量覆盖。
const (
	defaultHealthTimeoutMS = 5000
	defaultFailThreshold   = 2
	defaultHealthTestURL   = "https://www.google.com/generate_204"
	healthTestConcurrency  = 8
)

func healthTimeoutMS() int {
	ms := envInt("DS2API_MIHOMO_HEALTH_TIMEOUT", defaultHealthTimeoutMS)
	if ms < 1000 {
		ms = 1000
	}
	return ms
}

func failThreshold() int {
	n := envInt("DS2API_MIHOMO_FAIL_THRESHOLD", defaultFailThreshold)
	if n < 1 {
		n = 1
	}
	return n
}

func healthTestURL() string {
	if v := strings.TrimSpace(os.Getenv("DS2API_MIHOMO_TEST_URL")); v != "" {
		return v
	}
	return defaultHealthTestURL
}

func envInt(key string, def int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// SetNodeHealth 覆盖节点健康状态（内部测速与测试注入共用）。
// 写回订阅节点字段（随订阅持久化）并同步运行时连续失败计数。
func (m *Manager) SetNodeHealth(nodeKey string, h NodeHealth) {
	if m == nil || m.store == nil || strings.TrimSpace(nodeKey) == "" {
		return
	}
	if err := m.store.Update(func(c *config.Config) error {
		setNodeHealthLocked(c, nodeKey, h)
		return nil
	}); err != nil {
		config.Logger.Warn("[mihomo] set node health failed", "node_key", nodeKey, "error", err)
		return
	}
	m.setFailStreak(nodeKey, h.FailStreak)
}

// setNodeHealthLocked 在 config 里定位节点并写回测速结果（Store 锁内调用）。
func setNodeHealthLocked(c *config.Config, nodeKey string, h NodeHealth) {
	subID, nodeName := config.SplitMihomoNodeKey(nodeKey)
	if subID == "" || nodeName == "" {
		return
	}
	for i := range c.Mihomo.Subscriptions {
		sub := &c.Mihomo.Subscriptions[i]
		if sub.ID != subID {
			continue
		}
		for j := range sub.Nodes {
			node := &sub.Nodes[j]
			if node.Name != nodeName {
				continue
			}
			node.Status = h.Status
			node.LatencyMS = h.LatencyMS
			node.Error = h.Error
			node.TestedAt = h.TestedAt
			return
		}
	}
}

// nodeHealthOf 读取节点自带的持久化测速结果。
func nodeHealthOf(n config.MihomoNode) NodeHealth {
	return NodeHealth{
		Status:    n.Status,
		LatencyMS: n.LatencyMS,
		Error:     n.Error,
		TestedAt:  n.TestedAt,
	}
}

// setFailStreak / failStreakMap 维护连续失败次数（纯运行时）。
func (m *Manager) setFailStreak(nodeKey string, streak int) {
	m.healthMu.Lock()
	if streak <= 0 {
		delete(m.failStreak, nodeKey)
	} else {
		if m.failStreak == nil {
			m.failStreak = map[string]int{}
		}
		m.failStreak[nodeKey] = streak
	}
	m.healthMu.Unlock()
}

func (m *Manager) failStreakMap() map[string]int {
	m.healthMu.Lock()
	defer m.healthMu.Unlock()
	out := make(map[string]int, len(m.failStreak))
	for k, v := range m.failStreak {
		out[k] = v
	}
	return out
}

// NodeHealthMap 返回当前健康状态快照（节点键 -> 健康信息），
// 数据直接来源于订阅节点上持久化的测速字段 + 运行时连续失败计数。
func (m *Manager) NodeHealthMap() map[string]NodeHealth {
	if m == nil || m.store == nil {
		return map[string]NodeHealth{}
	}
	streaks := m.failStreakMap()
	out := map[string]NodeHealth{}
	for _, sub := range m.store.Snapshot().Mihomo.Subscriptions {
		for _, node := range sub.Nodes {
			key := config.MihomoNodeKey(sub.ID, node.Name)
			h := nodeHealthOf(node)
			h.FailStreak = streaks[key]
			out[key] = h
		}
	}
	return out
}

// healthSummary 统计全部节点的健康分布，供状态接口展示。
func (m *Manager) healthSummary(nodeKeys []string) map[string]any {
	health := m.NodeHealthMap()
	okCount, failCount := 0, 0
	var lastTest int64
	for _, key := range nodeKeys {
		h, known := health[key]
		if !known {
			continue
		}
		if h.TestedAt > lastTest {
			lastTest = h.TestedAt
		}
		switch h.Status {
		case NodeHealthOK:
			okCount++
		case NodeHealthFail:
			failCount++
		}
	}
	return map[string]any{
		"available": okCount,
		"dead":      failCount,
		"unknown":   len(nodeKeys) - okCount - failCount,
		"last_test": lastTest,
	}
}

// nodeTestResult 是单节点测速的中间结果。
type nodeTestResult struct {
	NodeKey string
	Name    string
	Health  NodeHealth
}

// testNodeDelays 并发（上限 healthTestConcurrency）调用 mihomo 控制 API
// `GET /proxies/{name}/delay?url=...&timeout=...` 测每个节点的出口延迟。
// proxy 名使用 nodeRef.Key（即 "subID::nodeName" 限定名）。
func (m *Manager) testNodeDelays(ctx context.Context, apiPort int, nodes []nodeRef) []nodeTestResult {
	timeoutMS := healthTimeoutMS()
	testURL := healthTestURL()
	results := make([]nodeTestResult, len(nodes))

	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < healthTestConcurrency && w < len(nodes); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				node := nodes[i]
				latency, err := probeNodeDelay(ctx, apiPort, node.Key, testURL, timeoutMS)
				h := NodeHealth{TestedAt: time.Now().Unix()}
				if err != nil {
					h.Status = NodeHealthFail
					h.Error = err.Error()
				} else {
					h.Status = NodeHealthOK
					h.LatencyMS = latency
				}
				results[i] = nodeTestResult{NodeKey: node.Key, Name: node.Name, Health: h}
			}
		}()
	}
	for i := range nodes {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return results
}

// applyHealthResults 把测速结果写回订阅节点字段（随订阅持久化），
// 并累加/清零连续失败计数（供 watcher 的故障转移阈值使用）。
func (m *Manager) applyHealthResults(results []nodeTestResult) {
	if m == nil || m.store == nil || len(results) == 0 {
		return
	}
	if err := m.store.Update(func(c *config.Config) error {
		for _, r := range results {
			setNodeHealthLocked(c, r.NodeKey, r.Health)
		}
		return nil
	}); err != nil {
		config.Logger.Warn("[mihomo] apply health results failed", "error", err)
	}
	m.healthMu.Lock()
	if m.failStreak == nil {
		m.failStreak = map[string]int{}
	}
	for _, r := range results {
		if r.Health.Status == NodeHealthFail {
			m.failStreak[r.NodeKey]++
		} else {
			delete(m.failStreak, r.NodeKey)
		}
	}
	m.healthMu.Unlock()
}

// probeNodeDelay 调用 mihomo 控制 API 测试单个代理节点延迟（毫秒）。
func probeNodeDelay(ctx context.Context, apiPort int, proxyName, testURL string, timeoutMS int) (int, error) {
	endpoint := fmt.Sprintf(
		"http://127.0.0.1:%d/proxies/%s/delay?url=%s&timeout=%d",
		apiPort, url.PathEscape(proxyName), url.QueryEscape(testURL), timeoutMS,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	client := &http.Client{Timeout: time.Duration(timeoutMS+3000) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			config.Logger.Warn("[mihomo] close delay probe body failed", "error", closeErr)
		}
	}()
	var payload struct {
		Delay   int    `json:"delay"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, fmt.Errorf("解析测速响应失败: %w", err)
	}
	if resp.StatusCode >= 400 || payload.Delay <= 0 {
		msg := strings.TrimSpace(payload.Message)
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return 0, errors.New(msg)
	}
	return payload.Delay, nil
}

// runningWithAPI 检查进程状态标记与 external-controller 端口是否可连。
func (m *Manager) runningWithAPI() bool {
	m.mu.Lock()
	running := m.running
	m.mu.Unlock()
	if !running || m.store == nil {
		return false
	}
	return m.apiReachable()
}

// apiReachable 检查 external-controller 端口当前是否可连（不关心进程状态标记）。
func (m *Manager) apiReachable() bool {
	if m.store == nil {
		return false
	}
	return portListening(m.store.Snapshot().Mihomo.APIPort)
}
