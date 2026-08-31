package completionruntime

import (
	"context"
	"net/http"
	"testing"

	"ds2api/internal/auth"
	dsclient "ds2api/internal/deepseek/client"
	"ds2api/internal/promptcompat"
)

func blockedErr(op string) error {
	return &dsclient.RequestFailure{Op: op, Kind: dsclient.FailureUpstreamBlocked, Message: "upstream risk-control challenge: cf_challenge"}
}

func TestBlockedUpstreamErrorMapsToBadGateway(t *testing.T) {
	out := blockedUpstreamError(blockedErr("completion"))
	if out == nil {
		t.Fatal("expected OutputError for blocked failure")
	}
	if out.Status != http.StatusBadGateway {
		t.Fatalf("status=%d want=%d", out.Status, http.StatusBadGateway)
	}
	if out.Code != "upstream_blocked" {
		t.Fatalf("code=%q want upstream_blocked", out.Code)
	}
}

func TestBlockedUpstreamErrorNilForOtherKinds(t *testing.T) {
	kinds := []dsclient.FailureKind{
		dsclient.FailureMuted,
		dsclient.FailureManagedUnauthorized,
		dsclient.FailureDirectUnauthorized,
		dsclient.FailureCaptchaRequired,
		dsclient.FailureUnknown,
	}
	for _, kind := range kinds {
		err := &dsclient.RequestFailure{Op: "op", Kind: kind, Message: "m"}
		if got := blockedUpstreamError(err); got != nil {
			t.Fatalf("kind=%v must not map to blocked error, got %#v", kind, got)
		}
	}
	if got := blockedUpstreamError(nil); got != nil {
		t.Fatalf("nil must map to nil, got %#v", got)
	}
	if got := blockedUpstreamError(http.ErrServerClosed); got != nil {
		t.Fatalf("plain error must map to nil, got %#v", got)
	}
}

func TestAuthOutputErrorPrefersBlockedOver401(t *testing.T) {
	// 被 WAF/CF 拦时不能再报 401「请重新登录」：那会把运维引向完全错误的方向。
	blocked := authOutputError(&auth.RequestAuth{UseConfigToken: true}, blockedErr("create session"))
	if blocked == nil || blocked.Status != http.StatusBadGateway || blocked.Code != "upstream_blocked" {
		t.Fatalf("blocked create-session must map to 502, got %#v", blocked)
	}
	// 非拦截保持既有 401 语义。
	managed := authOutputError(&auth.RequestAuth{UseConfigToken: true}, &dsclient.RequestFailure{Kind: dsclient.FailureManagedUnauthorized})
	if managed == nil || managed.Status != http.StatusUnauthorized {
		t.Fatalf("managed unauthorized must stay 401, got %#v", managed)
	}
}

func TestAuthOutputErrorDirectTokenUnchanged(t *testing.T) {
	out := authOutputError(&auth.RequestAuth{UseConfigToken: false}, context.Canceled)
	if out == nil || out.Status != http.StatusUnauthorized || out.Message != DirectTokenErrorMessage {
		t.Fatalf("direct token path changed: %#v", out)
	}
}

// blockedPowCaller 让 GetPow 返回上游拦截错误，其余方法沿用 fakeDeepSeekCaller。
type blockedPowCaller struct {
	*fakeDeepSeekCaller
}

func (b *blockedPowCaller) GetPow(context.Context, *auth.RequestAuth, int) (string, error) {
	return "", blockedErr("get pow")
}

func TestFireSegmentPayloadsReportsBlockedAsBadGateway(t *testing.T) {
	ds := &blockedPowCaller{fakeDeepSeekCaller: &fakeDeepSeekCaller{}}
	stdReq := promptcompat.StandardRequest{ResolvedModel: "deepseek-v4-pro"}
	_, _, outErr := fireSegmentPayloads(context.Background(), ds, &auth.RequestAuth{}, stdReq, "session-1", []string{"a", "b"}, 3)
	if outErr == nil {
		t.Fatal("expected error from blocked PoW")
	}
	if outErr.Status != http.StatusBadGateway || outErr.Code != "upstream_blocked" {
		t.Fatalf("blocked PoW must surface as 502/upstream_blocked, got %#v", outErr)
	}
}
