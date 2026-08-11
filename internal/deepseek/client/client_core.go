package client

import (
	"context"
	"net/http"
	"sync"
	"time"

	"ds2api/internal/auth"
	"ds2api/internal/config"
	trans "ds2api/internal/deepseek/transport"
	"ds2api/internal/devcapture"
	"ds2api/internal/util"
)

// intFrom is a package-internal alias for the shared util version.
var intFrom = util.IntFrom

type Client struct {
	Store      *config.Store
	Auth       *auth.Resolver
	capture    *devcapture.Store
	regular    trans.Doer
	stream     trans.Doer
	fallback   *http.Client
	fallbackS  *http.Client
	maxRetries int

	powCache *powChallengeCache
	cookies  *cookieJar

	proxyClientsMu sync.RWMutex
	proxyClients   map[string]cachedProxyClients
}

func NewClient(store *config.Store, resolver *auth.Resolver) *Client {
	client := &Client{
		Store:        store,
		Auth:         resolver,
		capture:      devcapture.Global(),
		regular:      trans.New(60 * time.Second),
		stream:       trans.New(0),
		fallback:     &http.Client{Timeout: 60 * time.Second},
		fallbackS:    &http.Client{Timeout: 0},
		maxRetries:   3,
		proxyClients: map[string]cachedProxyClients{},
		powCache:     newPowChallengeCache(),
		cookies:      newCookieJar(),
	}
	if resolver != nil {
		resolver.PostLogin = func(ctx context.Context, a *auth.RequestAuth) {
			client.reportClientSettingsAfterLogin(ctx, a, "")
		}
	}
	return client
}

// PreloadPow 保留兼容接口，纯 Go 实现无需预加载。
func (c *Client) PreloadPow(_ context.Context) error {
	return nil
}

// Close releases pooled upstream connections owned by this client.
func (c *Client) Close() {
	if c == nil {
		return
	}
	closeRequestClients(requestClients{
		regular:   c.regular,
		stream:    c.stream,
		fallback:  c.fallback,
		fallbackS: c.fallbackS,
	})
	c.proxyClientsMu.Lock()
	cached := make([]requestClients, 0, len(c.proxyClients))
	for key, entry := range c.proxyClients {
		cached = append(cached, entry.clients)
		delete(c.proxyClients, key)
	}
	c.proxyClientsMu.Unlock()
	for _, bundle := range cached {
		closeRequestClients(bundle)
	}
}
