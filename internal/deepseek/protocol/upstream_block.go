package protocol

import (
	"net/http"
	"strings"
)

// UpstreamBlockKind 标识上游（DeepSeek / CloudFront / Cloudflare）在
// 风控层对请求做出的挑战/拦截类型。这些信号来自官方网页版前端
// main.js（commitId 2335d6b）中的判定逻辑，全部通过「响应状态码 + 响应头」
// 组合识别，与账号本身的状态（封禁/禁言/token 失效）无关。
type UpstreamBlockKind int

const (
	// UpstreamBlockNone 未检测到挑战/拦截。
	UpstreamBlockNone UpstreamBlockKind = iota
	// UpstreamBlockWAFCaptcha：HTTP 405 + x-amzn-waf-action: captcha（AWS WAF 要求过验证码）。
	UpstreamBlockWAFCaptcha
	// UpstreamBlockWAFChallenge：HTTP 202 + x-amzn-waf-action: challenge（AWS WAF JS challenge）。
	UpstreamBlockWAFChallenge
	// UpstreamBlockCFChallenge：HTTP 403 或 429 + cf-mitigated: challenge（Cloudflare challenge）。
	UpstreamBlockCFChallenge
)

// String 返回人类可读的分类名，用于日志与失败消息。
func (k UpstreamBlockKind) String() string {
	switch k {
	case UpstreamBlockWAFCaptcha:
		return "waf_captcha"
	case UpstreamBlockWAFChallenge:
		return "waf_challenge"
	case UpstreamBlockCFChallenge:
		return "cf_challenge"
	default:
		return "none"
	}
}

// LogTag 返回该分类对应的结构化日志标签（统一打在同一位置，便于检索/告警）。
func (k UpstreamBlockKind) LogTag() string {
	switch k {
	case UpstreamBlockWAFCaptcha:
		return "[upstream_waf_captcha]"
	case UpstreamBlockWAFChallenge:
		return "[upstream_waf_challenge]"
	case UpstreamBlockCFChallenge:
		return "[upstream_cf_challenge]"
	default:
		return ""
	}
}

// ClassifyUpstreamBlock 按官方网页版的判定规则对一次响应做挑战/拦截分类。
// 判定优先级：WAF captcha > WAF challenge > Cloudflare challenge > none。
// 只在「状态码 + 响应头」精确组合命中时返回非 none，普通 401/403/429
// （无对应响应头）一律返回 none，不影响既有的鉴权失败处理路径。
func ClassifyUpstreamBlock(status int, h http.Header) UpstreamBlockKind {
	wafAction := strings.ToLower(strings.TrimSpace(h.Get("x-amzn-waf-action")))
	switch {
	case status == http.StatusMethodNotAllowed && wafAction == "captcha":
		return UpstreamBlockWAFCaptcha
	case status == http.StatusAccepted && wafAction == "challenge":
		return UpstreamBlockWAFChallenge
	}
	if (status == http.StatusForbidden || status == http.StatusTooManyRequests) &&
		strings.EqualFold(strings.TrimSpace(h.Get("cf-mitigated")), "challenge") {
		return UpstreamBlockCFChallenge
	}
	return UpstreamBlockNone
}
