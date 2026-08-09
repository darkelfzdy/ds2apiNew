package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"ds2api/internal/config"
)

const (
	// delayTestBatchSize 是单批并发测试的节点数上限（与主流代理软件一致）。
	delayTestBatchSize = 60
	// delayTestTimeout 是单个节点的延迟测试超时。
	delayTestTimeout = 5 * time.Second
	// delayTestURL 是延迟测试目标（返回 204 的轻量端点，主流代理软件默认同款）。
	delayTestURL = "http://www.gstatic.com/generate_204"
)

// delayResult 单个节点的延迟测试结果。
type delayResult struct {
	NodeKey string `json:"node_key"`
	Name    string `json:"name"`
	Delay   int64  `json:"delay_ms,omitempty"`
	Error   string `json:"error,omitempty"`
}

// TestLatency 通过 mihomo external-controller 批量测试全部订阅节点的延迟
// （Clash 式：经节点隧道请求探测 URL 计时）。每批最多并发 delayTestBatchSize
// 个节点，全部完成后返回结果：成功项按延迟升序，失败项排在最后。
func (m *Manager) TestLatency(ctx context.Context) ([]map[string]any, error) {
	if m == nil || m.store == nil {
		return nil, errors.New("mihomo manager 未初始化")
	}
	cfg := m.store.Snapshot()
	m.mu.Lock()
	running := m.running
	apiPort := cfg.Mihomo.APIPort
	m.mu.Unlock()
	if !running || !cfg.Mihomo.Enabled {
		return nil, errors.New("代理桥未运行：请先启用代理桥并应用后再测延迟")
	}

	type nodeRef struct {
		key  string
		name string
	}
	refs := []nodeRef{}
	seen := map[string]struct{}{}
	for _, sub := range cfg.Mihomo.Subscriptions {
		for _, node := range sub.Nodes {
			key := config.MihomoNodeKey(sub.ID, node.Name)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			refs = append(refs, nodeRef{key: key, name: node.Name})
		}
	}
	if len(refs) == 0 {
		return []map[string]any{}, nil
	}

	base := fmt.Sprintf("http://127.0.0.1:%d", apiPort)
	client := &http.Client{Timeout: delayTestTimeout + 2*time.Second}
	probe := func(ctx context.Context, name string) (int64, error) {
		endpoint := fmt.Sprintf(
			"%s/proxies/%s/delay?timeout=%d&url=%s",
			base,
			url.PathEscape(name),
			delayTestTimeout.Milliseconds(),
			url.QueryEscape(delayTestURL),
		)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return 0, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		if closeErr := resp.Body.Close(); closeErr != nil {
			config.Logger.Warn("[mihomo] close delay probe body failed", "error", closeErr)
		}
		if readErr != nil {
			return 0, readErr
		}
		if resp.StatusCode != http.StatusOK {
			return 0, fmt.Errorf("mihomo 返回 HTTP %d", resp.StatusCode)
		}
		var out struct {
			Delay int64 `json:"delay"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return 0, fmt.Errorf("解析延迟结果失败: %w", err)
		}
		return out.Delay, nil
	}

	results := make([]delayResult, 0, len(refs))
	for start := 0; start < len(refs); start += delayTestBatchSize {
		end := start + delayTestBatchSize
		if end > len(refs) {
			end = len(refs)
		}
		batch := refs[start:end]
		ch := make(chan delayResult, len(batch))
		var wg sync.WaitGroup
		for _, ref := range batch {
			wg.Add(1)
			go func(ref nodeRef) {
				defer wg.Done()
				res := delayResult{NodeKey: ref.key, Name: ref.name}
				delay, err := probe(ctx, ref.name)
				if err != nil {
					res.Error = err.Error()
				} else {
					res.Delay = delay
				}
				ch <- res
			}(ref)
		}
		wg.Wait()
		close(ch)
		for res := range ch {
			results = append(results, res)
		}
	}

	sort.SliceStable(results, func(i, j int) bool {
		okI, okJ := results[i].Error == "", results[j].Error == ""
		if okI != okJ {
			return okI // 成功项在前，失败项垫底
		}
		if okI {
			return results[i].Delay < results[j].Delay
		}
		return results[i].Name < results[j].Name
	})
	out := make([]map[string]any, 0, len(results))
	for _, r := range results {
		item := map[string]any{"node_key": r.NodeKey, "name": r.Name}
		if r.Error != "" {
			item["error"] = r.Error
		} else {
			item["delay_ms"] = r.Delay
		}
		out = append(out, item)
	}
	return out, nil
}
