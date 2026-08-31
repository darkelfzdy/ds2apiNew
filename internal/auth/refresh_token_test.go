package auth

import (
	"context"
	"errors"
	"net"
	"net/url"
	"testing"

	"ds2api/internal/account"
	"ds2api/internal/config"
)

// blockedFailure 结构性实现 upstreamBlockedMarker，模拟 client.RequestFailure
// 在 Kind=upstream_blocked 时的形态（测试里不 import client，避免拉进整个传输层）。
type blockedFailure struct{ msg string }

func (b *blockedFailure) Error() string         { return b.msg }
func (b *blockedFailure) UpstreamBlocked() bool { return true }

type plainFailure struct{ msg string }

func (p *plainFailure) Error() string { return p.msg }

func newRefreshResolver(t *testing.T, login LoginFunc) *Resolver {
	t.Helper()
	t.Setenv("DS2API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[{"email":"acc@example.com","password":"pwd"}]
	}`)
	store := config.LoadStore()
	// 注意：config 加载时会 ClearAccountTokens（token 不存盘，只存内存），
	// 所以必须用 UpdateAccountToken 在内存里播种一个“当前可用”的 token。
	if err := store.UpdateAccountToken("acc@example.com", "account-token"); err != nil {
		t.Fatalf("seed token failed: %v", err)
	}
	pool := account.NewPool(store)
	return NewResolver(store, pool, login)
}

func managedAuth(t *testing.T, r *Resolver) *RequestAuth {
	t.Helper()
	a := &RequestAuth{
		UseConfigToken: true,
		AccountID:      "acc@example.com",
		DeepSeekToken:  "account-token",
		Account:        config.Account{Email: "acc@example.com", Password: "pwd", Token: "account-token"},
	}
	return a
}

func persistedToken(t *testing.T, r *Resolver) string {
	t.Helper()
	for _, acc := range r.Store.Accounts() {
		if acc.Email == "acc@example.com" {
			return acc.Token
		}
	}
	t.Fatal("account not found in store")
	return ""
}

func TestRefreshTokenSeededStateIsSane(t *testing.T) {
	// 防误测：如果没播种成功，下面的“保留”用例会变成空转。
	r := newRefreshResolver(t, func(context.Context, config.Account) (string, error) {
		return "unused", nil
	})
	if got := persistedToken(t, r); got != "account-token" {
		t.Fatalf("test fixture must start with a token, got %q", got)
	}
}

func TestTransientRefreshFailureClassification(t *testing.T) {
	transient := []error{
		&blockedFailure{"upstream risk-control challenge: cf_challenge"},
		context.DeadlineExceeded,
		context.Canceled,
		&url.Error{Op: "Post", URL: "https://chat.deepseek.com", Err: errors.New("dial tcp: no route to host")},
		&net.OpError{Op: "dial", Err: errors.New("connection refused")},
	}
	for _, err := range transient {
		if !transientRefreshFailure(err) {
			t.Fatalf("expected transient for %T: %v", err, err)
		}
	}

	// 登录确实被上游拒绝：这些说明凭据/账号本身有问题，旧 token 该清。
	rejected := []error{
		errors.New("login failed: USER_IS_BANNED"),
		errors.New("login failed: wrong password"),
		&plainFailure{"login failed: biz error"},
		errors.New("missing login token"),
		nil,
	}
	for _, err := range rejected {
		if transientRefreshFailure(err) {
			t.Fatalf("expected non-transient for %v", err)
		}
	}
}

func TestRefreshTokenKeepsTokenOnTransientFailure(t *testing.T) {
	// 出口被拦（AWS WAF / Cloudflare challenge）：登录请求根本没到 DeepSeek，
	// 我们对旧 token 一无所知，不能把它抹掉。
	r := newRefreshResolver(t, func(context.Context, config.Account) (string, error) {
		return "", &blockedFailure{"upstream risk-control challenge: waf_captcha"}
	})
	a := managedAuth(t, r)

	if r.RefreshToken(context.Background(), a) {
		t.Fatal("expected refresh to fail")
	}
	if got := persistedToken(t, r); got != "account-token" {
		t.Fatalf("persisted token must survive a transient failure, got %q", got)
	}
	if a.Account.Token != "account-token" {
		t.Fatalf("in-memory account token must be restored, got %q", a.Account.Token)
	}
}

func TestRefreshTokenKeepsTokenOnNetworkFailure(t *testing.T) {
	r := newRefreshResolver(t, func(context.Context, config.Account) (string, error) {
		return "", &url.Error{Op: "Post", URL: "https://chat.deepseek.com/api/v0/users/login", Err: errors.New("dial tcp: i/o timeout")}
	})
	a := managedAuth(t, r)
	if r.RefreshToken(context.Background(), a) {
		t.Fatal("expected refresh to fail")
	}
	if got := persistedToken(t, r); got != "account-token" {
		t.Fatalf("network failure must not wipe the token, got %q", got)
	}
}

func TestRefreshTokenClearsTokenWhenLoginRejected(t *testing.T) {
	// 登录被上游明确拒绝：旧 token 已无价值，保持既有清空行为。
	r := newRefreshResolver(t, func(context.Context, config.Account) (string, error) {
		return "", errors.New("login failed: USER_IS_BANNED")
	})
	a := managedAuth(t, r)
	if r.RefreshToken(context.Background(), a) {
		t.Fatal("expected refresh to fail")
	}
	if got := persistedToken(t, r); got != "" {
		t.Fatalf("rejected login must clear the token, got %q", got)
	}
	if a.Account.Token != "" || a.DeepSeekToken != "" {
		t.Fatalf("account must be marked invalid in memory, got %q / %q", a.Account.Token, a.DeepSeekToken)
	}
}

func TestRefreshTokenSuccessPersistsNewToken(t *testing.T) {
	r := newRefreshResolver(t, func(context.Context, config.Account) (string, error) {
		return "fresh-token", nil
	})
	a := managedAuth(t, r)
	if !r.RefreshToken(context.Background(), a) {
		t.Fatal("expected refresh to succeed")
	}
	if got := persistedToken(t, r); got != "fresh-token" {
		t.Fatalf("expected fresh-token persisted, got %q", got)
	}
	if a.DeepSeekToken != "fresh-token" {
		t.Fatalf("expected in-memory token updated, got %q", a.DeepSeekToken)
	}
}
