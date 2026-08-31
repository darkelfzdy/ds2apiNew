package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"ds2api/internal/account"
	"ds2api/internal/config"
)

type ctxKey string

const authCtxKey ctxKey = "auth_context"

const toolsDisabledCtxKey ctxKey = "tools_disabled"

var (
	ErrUnauthorized = errors.New("unauthorized: missing auth token")
	ErrNoAccount    = errors.New("no accounts configured or all accounts are busy")
)

type RequestAuth struct {
	UseConfigToken bool
	DeepSeekToken  string
	CallerID       string
	AccountID      string
	TargetAccount  string
	Account        config.Account
	TriedAccounts  map[string]bool
	ToolsEnabled   bool
	resolver       *Resolver
}

type LoginFunc func(ctx context.Context, acc config.Account) (string, error)
type PostLoginFunc func(ctx context.Context, a *RequestAuth)

type Resolver struct {
	Store     *config.Store
	Pool      *account.Pool
	Login     LoginFunc
	PostLogin PostLoginFunc

	mu               sync.Mutex
	tokenRefreshedAt map[string]time.Time
}

func NewResolver(store *config.Store, pool *account.Pool, login LoginFunc) *Resolver {
	return &Resolver{
		Store:            store,
		Pool:             pool,
		Login:            login,
		tokenRefreshedAt: map[string]time.Time{},
	}
}

func (r *Resolver) Determine(req *http.Request) (*RequestAuth, error) {
	callerKey := extractCallerToken(req)
	if callerKey == "" {
		return nil, ErrUnauthorized
	}
	callerID := callerTokenID(callerKey)
	ctx := req.Context()
	if !r.Store.HasAPIKey(callerKey) {
		return &RequestAuth{
			UseConfigToken: false,
			DeepSeekToken:  callerKey,
			CallerID:       callerID,
			resolver:       r,
			TriedAccounts:  map[string]bool{},
		}, nil
	}
	target := strings.TrimSpace(req.Header.Get("X-Ds2-Target-Account"))
	toolsEnabled := r.Store.APIKeyToolsEnabled(callerKey)
	a, err := r.acquireManagedRequestAuth(ctx, callerID, target, toolsEnabled)
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (r *Resolver) acquireManagedRequestAuth(ctx context.Context, callerID, target string, toolsEnabled bool) (*RequestAuth, error) {
	tried := map[string]bool{}
	var lastEnsureErr error
	filter := account.AccountFilter(func(acc config.Account) bool {
		return acc.MatchesPoolType(toolsEnabled)
	})
	for {
		if target == "" && len(tried) >= len(r.Store.Accounts()) {
			if lastEnsureErr != nil {
				return nil, lastEnsureErr
			}
			return nil, ErrNoAccount
		}
		acc, ok := r.Pool.AcquireWait(ctx, target, tried, filter)
		if !ok {
			if lastEnsureErr != nil {
				return nil, lastEnsureErr
			}
			return nil, ErrNoAccount
		}

		a := &RequestAuth{
			UseConfigToken: true,
			CallerID:       callerID,
			AccountID:      acc.Identifier(),
			TargetAccount:  target,
			Account:        acc,
			TriedAccounts:  tried,
			ToolsEnabled:   toolsEnabled,
			resolver:       r,
		}

		if err := r.ensureManagedToken(ctx, a); err != nil {
			lastEnsureErr = err
			tried[a.AccountID] = true
			r.Pool.Release(a.AccountID)
			if target != "" {
				return nil, err
			}
			continue
		}
		return a, nil
	}
}

// DetermineCaller resolves caller identity without acquiring any pooled account.
// Use this for local-cache lookup routes that only need tenant isolation.
func (r *Resolver) DetermineCaller(req *http.Request) (*RequestAuth, error) {
	callerKey := extractCallerToken(req)
	if callerKey == "" {
		return nil, ErrUnauthorized
	}
	callerID := callerTokenID(callerKey)
	a := &RequestAuth{
		UseConfigToken: false,
		CallerID:       callerID,
		resolver:       r,
		TriedAccounts:  map[string]bool{},
	}
	if r == nil || r.Store == nil || !r.Store.HasAPIKey(callerKey) {
		a.DeepSeekToken = callerKey
	}
	return a, nil
}

func WithAuth(ctx context.Context, a *RequestAuth) context.Context {
	return context.WithValue(ctx, authCtxKey, a)
}

func FromContext(ctx context.Context) (*RequestAuth, bool) {
	v := ctx.Value(authCtxKey)
	a, ok := v.(*RequestAuth)
	return a, ok
}

// ToolsEnabledForRequest reports whether tool-call prompt injection should
// happen for the caller behind req. Managed API keys with tools_enabled=false
// disable injection; direct-token callers and missing/unknown keys keep the
// default (enabled) behavior.
func (r *Resolver) ToolsEnabledForRequest(req *http.Request) bool {
	callerKey := extractCallerToken(req)
	if callerKey == "" {
		return true
	}
	if r == nil || r.Store == nil || !r.Store.HasAPIKey(callerKey) {
		return true
	}
	return r.Store.APIKeyToolsEnabled(callerKey)
}

// WithToolsDisabled stores a flag in the context so downstream consumers
// (e.g. current-input-file tool transcript generation) can skip tool handling.
func WithToolsDisabled(ctx context.Context) context.Context {
	return context.WithValue(ctx, toolsDisabledCtxKey, true)
}

// ToolsDisabledFromContext returns true when the caller's API key has
// tools_enabled=false and the request context was marked accordingly.
func ToolsDisabledFromContext(ctx context.Context) bool {
	v := ctx.Value(toolsDisabledCtxKey)
	b, _ := v.(bool)
	return b
}

func (r *Resolver) loginAndPersist(ctx context.Context, a *RequestAuth) error {
	token, err := r.Login(ctx, a.Account)
	if err != nil {
		return err
	}
	a.Account.Token = token
	a.DeepSeekToken = token
	r.markTokenRefreshedNow(a.AccountID)
	if err := r.Store.UpdateAccountToken(a.AccountID, token); err != nil {
		return err
	}
	if r.PostLogin != nil {
		r.PostLogin(ctx, a)
	}
	return nil
}

func (r *Resolver) RefreshToken(ctx context.Context, a *RequestAuth) bool {
	if !a.UseConfigToken || a.AccountID == "" {
		return false
	}
	// 旧实现一进函数就把 token 清空再去登录，等于“先删再试”。
	// 但登录用的是账号密码，不依赖旧 token，提前清空对登录本身没有任何帮助；
	// 而一旦登录因出口被拦/网络抖动失败，我们就在“对旧 token 一无所知”的情况下
	// 把它扔了：下次请求必须重新走登录，而登录恰是风控最敏感的一步。
	// （token 不写盘，且 token 为空时 ensureManagedToken 会在下次请求自动重登，
	// 所以这不是把账号打死，而是多付一次重登代价与额外的风控暴露。）
	previousToken := strings.TrimSpace(a.Account.Token)
	if err := r.loginAndPersist(ctx, a); err != nil {
		config.Logger.Error("[refresh_token] failed", "account", a.AccountID, "error", err)
		if transientRefreshFailure(err) {
			// 这次失败没带来任何关于旧 token 的新信息，保留它。
			config.Logger.Warn("[refresh_token] transient failure, kept existing token",
				"account", a.AccountID, "kept", strings.TrimSpace(previousToken) != "")
			a.Account.Token = previousToken
			return false
		}
		// 登录确实被上游拒绝：旧 token 已无价值，按原行为清掉，
		// 避免后续请求拿着它反复撞 401。
		r.MarkTokenInvalid(a)
		return false
	}
	return true
}

// upstreamBlockedMarker 由 client.RequestFailure 结构性实现（避免 auth 反向 import client）。
type upstreamBlockedMarker interface {
	UpstreamBlocked() bool
}

// transientRefreshFailure 判断一次登录失败是否“只说明链路不通、不说明凭据失效”。
// 这类失败下保留旧 token 才是对的：我们并没有从上游得到任何关于它的结论。
func transientRefreshFailure(err error) bool {
	if err == nil {
		return false
	}
	var marker upstreamBlockedMarker
	if errors.As(err, &marker) && marker.UpstreamBlocked() {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// 没有 HTTP 响应可谈：连不上、TLS 握手失败、连接被重置、EOF。
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return true
	}
	return false
}

func (r *Resolver) MarkTokenInvalid(a *RequestAuth) {
	if !a.UseConfigToken || a.AccountID == "" {
		return
	}
	a.Account.Token = ""
	a.DeepSeekToken = ""
	r.clearTokenRefreshMark(a.AccountID)
	_ = r.Store.UpdateAccountToken(a.AccountID, "")
}

// SetAccountMutedUntil 持久化账号禁言到期时间。
// 与 DisableAccount 不同，这只是临时禁用，到期后号池会自动恢复调度。
// 当弹性号池开启时，封号后立即在同一个事务内触发 ReconcileElasticPool
// 补位：被封账号让出名额，按原始顺序从后面的休眠账号中启用一个补上。
func (r *Resolver) SetAccountMutedUntil(a *RequestAuth, muteUntil float64) {
	if !a.UseConfigToken || a.AccountID == "" || muteUntil <= 0 {
		return
	}
	identifier := a.AccountID
	if err := r.Store.Update(func(c *config.Config) error {
		for i := range c.Accounts {
			if c.Accounts[i].Identifier() != identifier {
				continue
			}
			c.Accounts[i].MutedUntil = muteUntil
			break
		}
		if c.ElasticPool.Enabled {
			account.ReconcileElasticPool(c)
		}
		return nil
	}); err != nil {
		config.Logger.Error("[muted_account] failed to persist muted_until", "account", identifier, "error", err)
		return
	}
	a.Account.MutedUntil = muteUntil
	if r.Pool != nil {
		r.Pool.Reset()
	}
	config.Logger.Info("[muted_account] account muted until", "account", identifier, "mute_until", muteUntil)
}

// SetAccountBanned 将账号标记为被上游停用（USER_IS_BANNED）。
// 手动启用或启用全部账号不会解除该状态；只有成功刷新 token 后才会在 Login 中清除。
func (r *Resolver) SetAccountBanned(a *RequestAuth, reason string) {
	if !a.UseConfigToken || a.AccountID == "" {
		return
	}
	if strings.TrimSpace(reason) == "" {
		reason = "账户已被停用"
	}
	identifier := a.AccountID
	if err := r.Store.Update(func(c *config.Config) error {
		for i := range c.Accounts {
			if c.Accounts[i].Identifier() != identifier {
				continue
			}
			c.Accounts[i].Banned = true
			c.Accounts[i].Disabled = true
			c.Accounts[i].DisabledReason = reason
			break
		}
		if c.ElasticPool.Enabled {
			account.ReconcileElasticPool(c)
		}
		return nil
	}); err != nil {
		config.Logger.Error("[banned_account] failed to persist banned state", "account", identifier, "error", err)
		return
	}
	a.Account.Banned = true
	a.Account.Disabled = true
	a.Account.DisabledReason = reason
	if r.Pool != nil {
		r.Pool.Reset()
	}
	config.Logger.Warn("[banned_account] account disabled after USER_IS_BANNED", "account", identifier, "reason", reason)
}

func (r *Resolver) SwitchAccount(ctx context.Context, a *RequestAuth) bool {
	if !a.UseConfigToken {
		return false
	}
	if strings.TrimSpace(a.TargetAccount) != "" {
		return false
	}
	if a.TriedAccounts == nil {
		a.TriedAccounts = map[string]bool{}
	}
	if a.AccountID != "" {
		a.TriedAccounts[a.AccountID] = true
		r.Pool.Release(a.AccountID)
	}
	filter := account.AccountFilter(func(acc config.Account) bool {
		return acc.MatchesPoolType(a.ToolsEnabled)
	})
	for {
		acc, ok := r.Pool.Acquire("", a.TriedAccounts, filter)
		if !ok {
			return false
		}
		a.Account = acc
		a.AccountID = acc.Identifier()
		if err := r.ensureManagedToken(ctx, a); err != nil {
			a.TriedAccounts[a.AccountID] = true
			r.Pool.Release(a.AccountID)
			continue
		}
		return true
	}
}

func (a *RequestAuth) SwitchAccount(ctx context.Context) bool {
	if a == nil || a.resolver == nil {
		return false
	}
	return a.resolver.SwitchAccount(ctx, a)
}

func (r *Resolver) Release(a *RequestAuth) {
	if a == nil || !a.UseConfigToken || a.AccountID == "" {
		return
	}
	r.Pool.Release(a.AccountID)
}

func extractCallerToken(req *http.Request) string {
	authHeader := strings.TrimSpace(req.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		token := strings.TrimSpace(authHeader[7:])
		if token != "" {
			return token
		}
	}
	if key := strings.TrimSpace(req.Header.Get("x-api-key")); key != "" {
		return key
	}
	// Gemini/Google clients commonly send API key via x-goog-api-key.
	if key := strings.TrimSpace(req.Header.Get("x-goog-api-key")); key != "" {
		return key
	}
	// Gemini AI Studio compatibility: allow query key fallback only when no
	// header-based credential is present.
	if key := strings.TrimSpace(req.URL.Query().Get("key")); key != "" {
		return key
	}
	return strings.TrimSpace(req.URL.Query().Get("api_key"))
}

func callerTokenID(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return "caller:" + hex.EncodeToString(sum[:8])
}

func (r *Resolver) ensureManagedToken(ctx context.Context, a *RequestAuth) error {
	if strings.TrimSpace(a.Account.Token) == "" {
		return r.loginAndPersist(ctx, a)
	}
	if r.shouldForceRefresh(a.AccountID) {
		if err := r.loginAndPersist(ctx, a); err != nil {
			return err
		}
		return nil
	}
	a.DeepSeekToken = a.Account.Token
	return nil
}

func (r *Resolver) shouldForceRefresh(accountID string) bool {
	if r == nil || r.Store == nil {
		return false
	}
	if strings.TrimSpace(accountID) == "" {
		return false
	}
	intervalHours := r.Store.RuntimeTokenRefreshIntervalHours()
	if intervalHours <= 0 {
		return false
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	last, ok := r.tokenRefreshedAt[accountID]
	if !ok || last.IsZero() {
		r.tokenRefreshedAt[accountID] = now
		return false
	}
	return now.Sub(last) >= time.Duration(intervalHours)*time.Hour
}

func (r *Resolver) markTokenRefreshedNow(accountID string) {
	if strings.TrimSpace(accountID) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokenRefreshedAt[accountID] = time.Now()
}

func (r *Resolver) clearTokenRefreshMark(accountID string) {
	if strings.TrimSpace(accountID) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tokenRefreshedAt, accountID)
}
