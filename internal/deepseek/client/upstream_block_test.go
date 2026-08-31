package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"ds2api/internal/auth"
	dsprotocol "ds2api/internal/deepseek/protocol"
)

// blockedDoer 返回带指定状态码与响应头的固定响应，模拟上游风控挑战。
func blockedDoer(status int, headers map[string]string, body string) doerFunc {
	return func(req *http.Request) (*http.Response, error) {
		h := http.Header{}
		for k, v := range headers {
			h.Set(k, v)
		}
		return &http.Response{
			StatusCode: status,
			Header:     h,
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}
}

func TestPostJSONWithStatusMarksUpstreamBlock(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		headers map[string]string
		want    dsprotocol.UpstreamBlockKind
	}{
		{"waf captcha", http.StatusMethodNotAllowed, map[string]string{"x-amzn-waf-action": "captcha"}, dsprotocol.UpstreamBlockWAFCaptcha},
		{"waf challenge", http.StatusAccepted, map[string]string{"x-amzn-waf-action": "challenge"}, dsprotocol.UpstreamBlockWAFChallenge},
		{"cf challenge 403", http.StatusForbidden, map[string]string{"cf-mitigated": "challenge"}, dsprotocol.UpstreamBlockCFChallenge},
		{"cf challenge 429", http.StatusTooManyRequests, map[string]string{"cf-mitigated": "challenge"}, dsprotocol.UpstreamBlockCFChallenge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &Client{}
			resp, status, err := client.postJSONWithStatus(
				context.Background(),
				blockedDoer(tc.status, tc.headers, `{"code":1,"msg":"blocked"}`),
				nil,
				"https://example.com/api",
				map[string]string{"x-test": "1"},
				map[string]any{"foo": "bar"},
			)
			if err != nil {
				t.Fatalf("postJSONWithStatus error: %v", err)
			}
			if status != tc.status {
				t.Fatalf("status=%d want=%d", status, tc.status)
			}
			if got := upstreamBlockFromPayload(resp); got != tc.want {
				t.Fatalf("upstreamBlockFromPayload = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPostJSONWithStatusPlain401NotMarkedBlocked(t *testing.T) {
	client := &Client{}
	resp, status, err := client.postJSONWithStatus(
		context.Background(),
		blockedDoer(http.StatusUnauthorized, nil, `{"code":40001,"msg":"unauthorized"}`),
		nil,
		"https://example.com/api",
		map[string]string{"x-test": "1"},
		map[string]any{"foo": "bar"},
	)
	if err != nil {
		t.Fatalf("postJSONWithStatus error: %v", err)
	}
	if status != http.StatusUnauthorized {
		t.Fatalf("status=%d want=%d", status, http.StatusUnauthorized)
	}
	if got := upstreamBlockFromPayload(resp); got != dsprotocol.UpstreamBlockNone {
		t.Fatalf("upstreamBlockFromPayload = %v, want UpstreamBlockNone for plain 401", got)
	}
	if _, present := resp[upstreamBlockPayloadKey]; present {
		t.Fatal("plain 401 must not carry the upstream-block payload key")
	}
}

func TestPostJSONWithStatusPlain403NoHeaderNotMarkedBlocked(t *testing.T) {
	client := &Client{}
	resp, _, err := client.postJSONWithStatus(
		context.Background(),
		blockedDoer(http.StatusForbidden, nil, `{"code":1,"msg":"forbidden"}`),
		nil,
		"https://example.com/api",
		map[string]string{"x-test": "1"},
		map[string]any{"foo": "bar"},
	)
	if err != nil {
		t.Fatalf("postJSONWithStatus error: %v", err)
	}
	if got := upstreamBlockFromPayload(resp); got != dsprotocol.UpstreamBlockNone {
		t.Fatalf("upstreamBlockFromPayload = %v, want UpstreamBlockNone for plain 403", got)
	}
}

func TestCallCompletionSurfacesUpstreamBlockAsFailure(t *testing.T) {
	client := &Client{
		stream: blockedDoer(http.StatusMethodNotAllowed, map[string]string{"x-amzn-waf-action": "captcha"}, `{"blocked":true}`),
	}
	_, err := client.CallCompletion(
		context.Background(),
		&auth.RequestAuth{DeepSeekToken: "token", AccountID: "acc-1"},
		map[string]any{"prompt": "hello"},
		"pow",
		3,
	)
	if err == nil {
		t.Fatal("expected completion error for WAF captcha response")
	}
	if !IsUpstreamBlockedError(err) {
		t.Fatalf("expected FailureUpstreamBlocked, got %#v", err)
	}
	var failure *RequestFailure
	if !errors.As(err, &failure) || failure.Kind != FailureUpstreamBlocked {
		t.Fatalf("unexpected failure shape: %#v", err)
	}
}

func TestCallCompletionKeepsPlainNon200AsResponse(t *testing.T) {
	// 普通非 200（无挑战响应头）行为不变：仍把响应交给下游处理，不提前报错。
	client := &Client{
		stream: blockedDoer(http.StatusInternalServerError, nil, `{"code":1}`),
	}
	resp, err := client.CallCompletion(
		context.Background(),
		&auth.RequestAuth{DeepSeekToken: "token", AccountID: "acc-1"},
		map[string]any{"prompt": "hello"},
		"pow",
		3,
	)
	if err != nil {
		t.Fatalf("plain non-200 must not error here, got %v", err)
	}
	if resp == nil || resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 response passed through, got %#v", resp)
	}
}

func TestUpstreamBlockFromPayloadRoundTrip(t *testing.T) {
	if got := upstreamBlockFromPayload(nil); got != dsprotocol.UpstreamBlockNone {
		t.Fatalf("nil payload = %v, want none", got)
	}
	for _, k := range []dsprotocol.UpstreamBlockKind{dsprotocol.UpstreamBlockWAFCaptcha, dsprotocol.UpstreamBlockWAFChallenge, dsprotocol.UpstreamBlockCFChallenge} {
		if got := upstreamBlockFromPayload(map[string]any{upstreamBlockPayloadKey: k.String()}); got != k {
			t.Fatalf("round trip %v -> %v", k, got)
		}
	}
}
