package client

import (
	"errors"
	"fmt"
)

type FailureKind string

const (
	FailureUnknown             FailureKind = ""
	FailureDirectUnauthorized  FailureKind = "direct_unauthorized"
	FailureManagedUnauthorized FailureKind = "managed_unauthorized"
	FailureCaptchaRequired     FailureKind = "captcha_required"
	FailureMuted               FailureKind = "account_muted"
	// FailureUpstreamBlocked 表示上游风控层（AWS WAF / Cloudflare challenge）
	// 拦截了请求——是「出口 IP / 指纹被拦」，与账号本身的状态无关，
	// 不应触发 token 刷新，也不应归类为账号封禁。
	FailureUpstreamBlocked FailureKind = "upstream_blocked"
)

type RequestFailure struct {
	Op      string
	Kind    FailureKind
	Message string
}

func (e *RequestFailure) Error() string {
	if e == nil {
		return ""
	}
	switch {
	case e.Op != "" && e.Message != "":
		return fmt.Sprintf("%s: %s", e.Op, e.Message)
	case e.Op != "":
		return e.Op + " failed"
	case e.Message != "":
		return e.Message
	default:
		return "request failed"
	}
}

func IsManagedUnauthorizedError(err error) bool {
	var failure *RequestFailure
	return errors.As(err, &failure) && failure.Kind == FailureManagedUnauthorized
}

func IsDirectUnauthorizedError(err error) bool {
	var failure *RequestFailure
	return errors.As(err, &failure) && failure.Kind == FailureDirectUnauthorized
}

func IsMutedError(err error) bool {
	var failure *RequestFailure
	return errors.As(err, &failure) && failure.Kind == FailureMuted
}

func IsUpstreamBlockedError(err error) bool {
	var failure *RequestFailure
	return errors.As(err, &failure) && failure.Kind == FailureUpstreamBlocked
}
