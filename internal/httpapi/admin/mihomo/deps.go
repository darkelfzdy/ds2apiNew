// Package mihomo 提供 Mihomo 代理桥的管理接口：状态查看、开关与端口
// 设置、机场订阅管理、节点列表，以及“账号 ↔ 节点”绑定。
package mihomo

import (
	"context"
	"fmt"

	"ds2api/internal/config"
	adminshared "ds2api/internal/httpapi/admin/shared"
)

// Bridge 抽象 mihomo.Manager，便于测试替换。
type Bridge interface {
	Supported() bool
	Status() map[string]any
	Apply(ctx context.Context) error
	BindAccount(ctx context.Context, identifier, nodeKey string) error
	AddSubscription(ctx context.Context, name, rawURL string) (config.MihomoSubscription, error)
	RefreshSubscription(ctx context.Context, subID string) (int, error)
	DeleteSubscription(ctx context.Context, subID string) error
	UpdateSettings(ctx context.Context, enabled bool, binaryPath string, basePort, apiPort int, autoBind bool, nodeExclude []string) error
	ListNodes() []map[string]any
	TestLatency(ctx context.Context) ([]map[string]any, error)
	AssignAccounts(ctx context.Context, nodeKeys []string) (int, error)
	DownloadInfo() map[string]any
	StartBinaryDownload(ctx context.Context) error
}

type Handler struct {
	Store  adminshared.ConfigStore
	Pool   adminshared.PoolController
	Bridge Bridge
}

var writeJSON = adminshared.WriteJSON

func fieldString(m map[string]any, key string) string {
	return adminshared.FieldString(m, key)
}

func fieldInt(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return 0
	}
}

func fieldBool(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}

// fieldStringSlice 读取字符串数组字段：键缺失返回 nil（表示“未提供”，
// 调用方应保持原值）；键存在但为空数组/null 返回空切片（表示“清空”）。
func fieldStringSlice(m map[string]any, key string) []string {
	items, ok := m[key].([]any)
	if !ok {
		if _, present := m[key]; present {
			return []string{}
		}
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, isStr := item.(string); isStr {
			out = append(out, s)
			continue
		}
		out = append(out, fmt.Sprintf("%v", item))
	}
	return out
}
