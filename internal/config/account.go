package config

import (
	"strings"
	"time"
)

const (
	// PoolTypeDefault 允许无工具调用和含工具调用的请求使用此账号。
	PoolTypeDefault = "default"
	// PoolTypeNoTools 仅允许无工具调用的请求使用此账号。
	// tools_enabled=true 的 API 密钥不会调度到此账号。
	PoolTypeNoTools = "no_tools"
	// PoolTypeToolsOnly 仅允许含工具调用的请求使用此账号。
	// tools_enabled=false 的 API 密钥不会调度到此账号。
	PoolTypeToolsOnly = "tools_only"
)

func (a Account) Identifier() string {
	if strings.TrimSpace(a.Email) != "" {
		return strings.TrimSpace(a.Email)
	}
	if mobile := NormalizeMobileForStorage(a.Mobile); mobile != "" {
		return mobile
	}
	return ""
}

// IsEnabled reports whether the account is eligible for scheduling.
// Disabled accounts are skipped by the pool.
func (a Account) IsEnabled() bool {
	return !a.Disabled
}

// IsMuted reports whether the account is currently muted (banned from chatting).
// Returns true only when MutedUntil is set and has not yet expired.
func (a Account) IsMuted() bool {
	if a.MutedUntil <= 0 {
		return false
	}
	return a.MutedUntil > float64(time.Now().Unix())
}

// IsCoolingDown reports whether the account is in a local risk-control cooldown
// after a captcha challenge. Like IsMuted this expires on its own, so the pool
// picks the account back up without any sweeper.
func (a Account) IsCoolingDown() bool {
	if a.CooldownUntil <= 0 {
		return false
	}
	return a.CooldownUntil > float64(time.Now().Unix())
}

// IsSchedulable reports whether the pool may hand this account to a request.
func (a Account) IsSchedulable() bool {
	return a.IsEnabled() && !a.IsMuted() && !a.IsCoolingDown()
}

// NormalizePoolType 规范化账号号池类型，空值视为 default。
func NormalizePoolType(poolType string) string {
	switch strings.ToLower(strings.TrimSpace(poolType)) {
	case PoolTypeNoTools:
		return PoolTypeNoTools
	case PoolTypeToolsOnly:
		return PoolTypeToolsOnly
	default:
		return PoolTypeDefault
	}
}

// MatchesPoolType 判断账号是否可被指定工具开关的请求调用。
//   - no_tools 账号仅匹配 toolsEnabled=false 的请求
//   - tools_only 账号仅匹配 toolsEnabled=true 的请求
//   - default / 未知 账号总是匹配
func (a Account) MatchesPoolType(toolsEnabled bool) bool {
	switch NormalizePoolType(a.PoolType) {
	case PoolTypeNoTools:
		return !toolsEnabled
	case PoolTypeToolsOnly:
		return toolsEnabled
	default:
		return true
	}
}
