package history

import (
	"net/http"
	"testing"

	dsclient "ds2api/internal/deepseek/client"
)

func TestMapErrorUpstreamBlockedIsBadGateway(t *testing.T) {
	err := &dsclient.RequestFailure{Op: "create session", Kind: dsclient.FailureUpstreamBlocked, Message: "upstream risk-control challenge: cf_challenge"}
	status, msg := MapError(err)
	if status != http.StatusBadGateway {
		t.Fatalf("status=%d want=%d", status, http.StatusBadGateway)
	}
	if msg != err.Error() {
		t.Fatalf("msg=%q want=%q", msg, err.Error())
	}
}

func TestMapErrorUnauthorizedUnchanged(t *testing.T) {
	cases := map[dsclient.FailureKind]int{
		dsclient.FailureManagedUnauthorized: http.StatusUnauthorized,
		dsclient.FailureDirectUnauthorized:  http.StatusUnauthorized,
		dsclient.FailureMuted:               http.StatusInternalServerError,
		dsclient.FailureCaptchaRequired:     http.StatusInternalServerError,
	}
	for kind, want := range cases {
		err := &dsclient.RequestFailure{Op: "op", Kind: kind, Message: "m"}
		if got, _ := MapError(err); got != want {
			t.Fatalf("kind=%v status=%d want=%d", kind, got, want)
		}
	}
}
