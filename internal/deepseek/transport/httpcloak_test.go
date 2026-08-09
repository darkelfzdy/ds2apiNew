package transport

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMergeHeaderOrderPreservesDeepSeekHeaderPosition(t *testing.T) {
	base := []string{"content-length", "authorization", "accept", "user-agent", "cookie"}
	got := mergeHeaderOrder(base, []string{"x-ds-pow-response", "authorization", "accept"})
	want := []string{"x-ds-pow-response", "authorization", "accept", "content-length", "user-agent", "cookie"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d]=%q, want %q; full=%v", i, got[i], want[i], got)
		}
	}
}

func TestMergeHeaderOrderDoesNotDuplicateCustomHeader(t *testing.T) {
	got := mergeHeaderOrder([]string{"accept", "x-ds-pow-response"}, []string{"x-ds-pow-response"})
	want := []string{"x-ds-pow-response", "accept"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestHTTPCloakClientKeepsDeepSeekHeaderOrder(t *testing.T) {
	client := newHTTPCloakClient(time.Minute, "")
	order := client.GetHeaderOrder()
	if len(order) == 0 {
		t.Fatal("expected a non-empty header order")
	}
	seen := map[string]bool{}
	for _, key := range order {
		if seen[key] {
			t.Fatalf("duplicate header %q in %v", key, order)
		}
		seen[key] = true
	}
	if !seen["x-ds-pow-response"] {
		t.Fatalf("DeepSeek header missing from %v", order)
	}
}

func TestToHTTPCloakRequestCopiesHeadersAndBody(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://chat.deepseek.com/api", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Add("X-Ds-Pow-Response", "pow")
	converted := toHTTPCloakRequest(req)
	if converted.Method != http.MethodPost || converted.URL != req.URL.String() {
		t.Fatalf("request metadata was not preserved: %+v", converted)
	}
	if got := converted.Headers["X-Ds-Pow-Response"]; len(got) != 1 || got[0] != "pow" {
		t.Fatalf("headers were not copied: %v", converted.Headers)
	}
	if converted.Body == nil {
		t.Fatal("request body was not copied")
	}
}
