package protocol

import (
	"net/http"
	"testing"
)

func hdr(pairs ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		h.Set(pairs[i], pairs[i+1])
	}
	return h
}

func TestClassifyUpstreamBlockWAFCaptcha(t *testing.T) {
	got := ClassifyUpstreamBlock(http.StatusMethodNotAllowed, hdr("X-Amzn-Waf-Action", "captcha"))
	if got != UpstreamBlockWAFCaptcha {
		t.Fatalf("got %v, want UpstreamBlockWAFCaptcha", got)
	}
	if got.String() != "waf_captcha" || got.LogTag() != "[upstream_waf_captcha]" {
		t.Fatalf("String/LogTag = %q/%q", got.String(), got.LogTag())
	}
}

func TestClassifyUpstreamBlockWAFChallenge(t *testing.T) {
	got := ClassifyUpstreamBlock(http.StatusAccepted, hdr("x-amzn-waf-action", "challenge"))
	if got != UpstreamBlockWAFChallenge {
		t.Fatalf("got %v, want UpstreamBlockWAFChallenge", got)
	}
	if got.LogTag() != "[upstream_waf_challenge]" {
		t.Fatalf("LogTag = %q", got.LogTag())
	}
}

func TestClassifyUpstreamBlockCFChallenge(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests} {
		got := ClassifyUpstreamBlock(status, hdr("Cf-Mitigated", "challenge"))
		if got != UpstreamBlockCFChallenge {
			t.Fatalf("status=%d got %v, want UpstreamBlockCFChallenge", status, got)
		}
		if got.LogTag() != "[upstream_cf_challenge]" {
			t.Fatalf("LogTag = %q", got.LogTag())
		}
	}
}

func TestClassifyUpstreamBlockNegative(t *testing.T) {
	cases := []struct {
		name   string
		status int
		h      http.Header
	}{
		{"plain 401", http.StatusUnauthorized, hdr()},
		{"plain 403 no header", http.StatusForbidden, hdr()},
		{"plain 429 no header", http.StatusTooManyRequests, hdr()},
		{"403 wrong cf value", http.StatusForbidden, hdr("cf-mitigated", "managed")},
		{"405 wrong waf action", http.StatusMethodNotAllowed, hdr("x-amzn-waf-action", "challenge")},
		{"202 no header", http.StatusAccepted, hdr()},
		{"200 ok", http.StatusOK, hdr("x-amzn-waf-action", "captcha")},
		{"405 captcha wrong status combo", http.StatusOK, hdr("x-amzn-waf-action", "captcha")},
	}
	for _, tc := range cases {
		if got := ClassifyUpstreamBlock(tc.status, tc.h); got != UpstreamBlockNone {
			t.Fatalf("%s: got %v, want UpstreamBlockNone", tc.name, got)
		}
	}
	if UpstreamBlockNone.LogTag() != "" || UpstreamBlockNone.String() != "none" {
		t.Fatalf("none String/LogTag = %q/%q", UpstreamBlockNone.String(), UpstreamBlockNone.LogTag())
	}
}
