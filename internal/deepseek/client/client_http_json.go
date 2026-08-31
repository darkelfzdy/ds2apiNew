package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"ds2api/internal/config"
	dsprotocol "ds2api/internal/deepseek/protocol"
	trans "ds2api/internal/deepseek/transport"
)

// upstreamBlockPayloadKey 是写进 JSON 响应 map 的内部键，用于把
// 「本次响应被上游风控挑战拦截」的分类结果从 postJSONWithStatus /
// getJSONWithStatus 传给调用方（这两个函数的签名只回 map+status）。
// 键名以 __ 前缀避免与上游真实字段冲突。
const upstreamBlockPayloadKey = "__upstream_block"

// logUpstreamBlock 对一次响应做上游风控挑战分类；命中时打独立告警标签
// （[upstream_waf_captcha] / [upstream_waf_challenge] / [upstream_cf_challenge]），
// 便于把「出口 IP 被拦」与「账号异常」区分开。返回分类结果供调用方归类失败。
func logUpstreamBlock(url string, resp *http.Response, accountID string) dsprotocol.UpstreamBlockKind {
	if resp == nil {
		return dsprotocol.UpstreamBlockNone
	}
	block := dsprotocol.ClassifyUpstreamBlock(resp.StatusCode, resp.Header)
	if block == dsprotocol.UpstreamBlockNone {
		return block
	}
	config.Logger.Warn(block.LogTag()+" upstream risk-control challenge detected",
		"kind", block.String(),
		"url", url,
		"status", resp.StatusCode,
		"waf_action", resp.Header.Get("x-amzn-waf-action"),
		"cf_mitigated", resp.Header.Get("cf-mitigated"),
		"account", accountID,
	)
	return block
}

// upstreamBlockFromPayload 从 postJSONWithStatus / getJSONWithStatus 返回的
// map 里读出上游挑战分类（未命中返回 UpstreamBlockNone）。
func upstreamBlockFromPayload(resp map[string]any) dsprotocol.UpstreamBlockKind {
	if resp == nil {
		return dsprotocol.UpstreamBlockNone
	}
	s, _ := resp[upstreamBlockPayloadKey].(string)
	switch s {
	case dsprotocol.UpstreamBlockWAFCaptcha.String():
		return dsprotocol.UpstreamBlockWAFCaptcha
	case dsprotocol.UpstreamBlockWAFChallenge.String():
		return dsprotocol.UpstreamBlockWAFChallenge
	case dsprotocol.UpstreamBlockCFChallenge.String():
		return dsprotocol.UpstreamBlockCFChallenge
	default:
		return dsprotocol.UpstreamBlockNone
	}
}

func (c *Client) postJSON(ctx context.Context, doer trans.Doer, fallback trans.Doer, url string, headers map[string]string, payload any) (map[string]any, error) {
	body, status, err := c.postJSONWithStatus(ctx, doer, fallback, url, headers, payload)
	if err != nil {
		return nil, err
	}
	if status == 0 {
		return nil, errors.New("request failed")
	}
	return body, nil
}

func (c *Client) postJSONWithStatus(ctx context.Context, doer trans.Doer, fallback trans.Doer, url string, headers map[string]string, payload any) (map[string]any, int, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	headers = c.jsonHeaders(headers)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := doer.Do(req)
	if err != nil {
		config.Logger.Warn("[deepseek] fingerprint request failed, fallback to std transport", "url", url, "error", err)
		req2, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
		if reqErr != nil {
			return nil, 0, reqErr
		}
		for k, v := range headers {
			req2.Header.Set(k, v)
		}
		resp, err = fallback.Do(req2)
		if err != nil {
			return nil, 0, err
		}
	}
	defer func() { _ = resp.Body.Close() }()
	block := logUpstreamBlock(url, resp, "")
	payloadBytes, err := readResponseBody(resp)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	out := map[string]any{}
	if len(payloadBytes) > 0 {
		if err := json.Unmarshal(payloadBytes, &out); err != nil {
			config.Logger.Warn("[deepseek] json parse failed", "url", url, "status", resp.StatusCode, "content_encoding", resp.Header.Get("Content-Encoding"), "preview", preview(payloadBytes))
		}
	}
	if block != dsprotocol.UpstreamBlockNone {
		out[upstreamBlockPayloadKey] = block.String()
	}
	return out, resp.StatusCode, nil
}

func (c *Client) getJSONWithStatus(ctx context.Context, doer trans.Doer, url string, headers map[string]string) (map[string]any, int, error) {
	clients := c.requestClientsFromContext(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	for k, v := range headers {
		// A GET carries no body, so a browser never sends Content-Type on one.
		if strings.EqualFold(k, "Content-Type") {
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := doer.Do(req)
	if err != nil {
		config.Logger.Warn("[deepseek] fingerprint GET request failed, fallback to std transport", "url", url, "error", err)
		req2, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if reqErr != nil {
			return nil, 0, reqErr
		}
		for k, v := range headers {
			req2.Header.Set(k, v)
		}
		resp, err = clients.fallback.Do(req2)
		if err != nil {
			return nil, 0, err
		}
	}
	defer func() { _ = resp.Body.Close() }()
	block := logUpstreamBlock(url, resp, "")
	payloadBytes, err := readResponseBody(resp)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	out := map[string]any{}
	if len(payloadBytes) > 0 {
		if err := json.Unmarshal(payloadBytes, &out); err != nil {
			config.Logger.Warn("[deepseek] json parse failed", "url", url, "status", resp.StatusCode, "content_encoding", resp.Header.Get("Content-Encoding"), "preview", preview(payloadBytes))
		}
	}
	if block != dsprotocol.UpstreamBlockNone {
		out[upstreamBlockPayloadKey] = block.String()
	}
	return out, resp.StatusCode, nil
}
