package transport

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	fhttp2 "github.com/bogdanfinn/fhttp/http2"
	"github.com/bogdanfinn/fhttp/http2/hpack"
)

const h2ClientPreface = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"

// TestChromeProfileSelfConsistent guards the invariant that the advertised
// Chrome version and the uTLS ClientHello are the same generation. A UA
// claiming one Chrome while the TLS handshake fingerprints as another is a
// trivially detectable contradiction.
func TestChromeProfileSelfConsistent(t *testing.T) {
	if got := clientHelloID.Client; got != "Chrome" {
		t.Fatalf("clientHelloID.Client = %q, want Chrome", got)
	}
	// The ClientHello must be exactly the version we claim it is. This is the
	// invariant that broke before: a bare utls.HelloChrome_Auto silently
	// tracked the dependency and drifted away from the declared constant.
	if clientHelloID.Version != TLSChromeVersion {
		t.Fatalf("uTLS ClientHello is Chrome %s but TLSChromeVersion says %s",
			clientHelloID.Version, TLSChromeVersion)
	}

	httpVer, err := strconv.Atoi(ChromeMajorVersion)
	if err != nil {
		t.Fatalf("ChromeMajorVersion %q is not numeric: %v", ChromeMajorVersion, err)
	}
	tlsVer, err := strconv.Atoi(TLSChromeVersion)
	if err != nil {
		t.Fatalf("TLSChromeVersion %q is not numeric: %v", TLSChromeVersion, err)
	}
	// The HTTP layer may run ahead of the TLS layer (uTLS lags real Chrome),
	// but never behind: claiming an older browser than the handshake we send
	// would be a contradiction with no upside.
	if httpVer < tlsVer {
		t.Fatalf("ChromeMajorVersion %d is older than TLSChromeVersion %d", httpVer, tlsVer)
	}
	if httpVer-tlsVer > 25 {
		t.Errorf("User-Agent claims Chrome %d while the ClientHello reproduces Chrome %d; "+
			"the gap is wide enough that a JA3/JA4-to-version cross-check would notice. "+
			"Check whether uTLS now ships a newer Chrome fingerprint.", httpVer, tlsVer)
	}
}

type capturedPreface struct {
	settings   []fhttp2.Setting
	windowIncr uint32
	headers    []hpack.HeaderField
	err        error
}

// serveOnce accepts one connection, reads the HTTP/2 client preface, SETTINGS,
// WINDOW_UPDATE and the first HEADERS frame, then reports what it saw.
func serveOnce(ln net.Listener, out chan<- capturedPreface) {
	var got capturedPreface
	defer func() { out <- got }()

	conn, err := ln.Accept()
	if err != nil {
		got.err = err
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	br := bufio.NewReader(conn)
	preface := make([]byte, len(h2ClientPreface))
	if _, got.err = io.ReadFull(br, preface); got.err != nil {
		return
	}
	if string(preface) != h2ClientPreface {
		got.err = errString("bad client preface: " + string(preface))
		return
	}

	fr := fhttp2.NewFramer(conn, br)
	fr.ReadMetaHeaders = hpack.NewDecoder(65536, nil)

	// Advertise server SETTINGS so the client is willing to open a stream.
	if got.err = fr.WriteSettings(); got.err != nil {
		return
	}

	for {
		f, err := fr.ReadFrame()
		if err != nil {
			got.err = err
			return
		}
		switch frame := f.(type) {
		case *fhttp2.SettingsFrame:
			if frame.IsAck() {
				continue
			}
			for i := 0; i < frame.NumSettings(); i++ {
				got.settings = append(got.settings, frame.Setting(i))
			}
		case *fhttp2.WindowUpdateFrame:
			if frame.StreamID == 0 {
				got.windowIncr = frame.Increment
			}
		case *fhttp2.MetaHeadersFrame:
			got.headers = frame.Fields
			return
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// captureChromeHandshake drives one real request through the Chrome-profiled
// HTTP/2 transport over a loopback TCP connection and returns the frames the
// client actually put on the wire.
func captureChromeHandshake(t *testing.T) capturedPreface {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	results := make(chan capturedPreface, 1)
	go serveOnce(ln, results)

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	cc, err := newChromeH2Transport().NewClientConn(conn)
	if err != nil {
		t.Fatalf("NewClientConn: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost,
		"https://chat.deepseek.com/api/v0/chat/completion", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "test-agent")
	req.Header.Set("authorization", "Bearer token")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("x-ds-pow-response", "cG93")
	req.Header.Set("Origin", "https://chat.deepseek.com")

	// The fake server never answers, so RoundTrip is expected to fail; we only
	// care about the bytes it wrote before giving up.
	go func() {
		resp, rtErr := cc.RoundTrip(toFHTTPRequest(req))
		if rtErr == nil && resp != nil {
			_ = resp.Body.Close()
		}
	}()

	select {
	case got := <-results:
		if got.err != nil && got.headers == nil {
			t.Fatalf("server side: %v", got.err)
		}
		return got
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for client frames")
		return capturedPreface{}
	}
}

// TestChromeHTTP2Settings asserts the exact SETTINGS payload and order, plus
// the connection-level WINDOW_UPDATE. Together with the pseudo-header order
// these form the HTTP/2 ("Akamai") fingerprint.
func TestChromeHTTP2Settings(t *testing.T) {
	got := captureChromeHandshake(t)

	want := []fhttp2.Setting{
		{ID: fhttp2.SettingHeaderTableSize, Val: 65536},
		{ID: fhttp2.SettingEnablePush, Val: 0},
		{ID: fhttp2.SettingInitialWindowSize, Val: 6291456},
		{ID: fhttp2.SettingMaxHeaderListSize, Val: 262144},
	}
	if len(got.settings) != len(want) {
		t.Fatalf("got %d settings %v, want %d %v", len(got.settings), got.settings, len(want), want)
	}
	for i := range want {
		if got.settings[i] != want[i] {
			t.Errorf("settings[%d] = %v, want %v", i, got.settings[i], want[i])
		}
	}

	if got.windowIncr != chromeConnectionFlow {
		t.Errorf("connection WINDOW_UPDATE = %d, want %d (Go's default 1073741824 is a bot signal)",
			got.windowIncr, chromeConnectionFlow)
	}
}

// TestChromePseudoHeaderOrder asserts m,a,s,p ordering. Go's net/http2 emits
// a,m,p,s, which is what the fingerprint databases key on.
func TestChromePseudoHeaderOrder(t *testing.T) {
	got := captureChromeHandshake(t)

	var pseudo []string
	for _, hf := range got.headers {
		if strings.HasPrefix(hf.Name, ":") {
			pseudo = append(pseudo, hf.Name)
		}
	}
	want := []string{":method", ":authority", ":scheme", ":path"}
	if len(pseudo) != len(want) {
		t.Fatalf("got pseudo-headers %v, want %v", pseudo, want)
	}
	for i := range want {
		if pseudo[i] != want[i] {
			t.Fatalf("pseudo-header order = %v, want %v", pseudo, want)
		}
	}
}

// TestChromeHeaderOrderDeterministic asserts regular headers follow the pinned
// Chrome order rather than Go's randomised map iteration, and that the magic
// ordering keys never reach the wire.
func TestChromeHeaderOrderDeterministic(t *testing.T) {
	got := captureChromeHandshake(t)

	var names []string
	for _, hf := range got.headers {
		if strings.HasPrefix(hf.Name, ":") {
			continue
		}
		if strings.Contains(strings.ToLower(hf.Name), "header-order") {
			t.Fatalf("fhttp ordering key leaked onto the wire: %q", hf.Name)
		}
		if hf.Name != strings.ToLower(hf.Name) {
			t.Errorf("header %q is not lowercase; HTTP/2 requires lowercase field names", hf.Name)
		}
		names = append(names, hf.Name)
	}

	rank := make(map[string]int, len(chromeHeaderOrder))
	for i, n := range chromeHeaderOrder {
		rank[n] = i
	}
	prev := -1
	for _, n := range names {
		r, ok := rank[n]
		if !ok {
			continue // unlisted headers are appended after the pinned ones
		}
		if r < prev {
			t.Fatalf("header %q out of order in %v (want the order in chromeHeaderOrder)", n, names)
		}
		prev = r
	}

	// Sanity: the request really did carry the headers we set.
	for _, required := range []string{"authorization", "user-agent", "content-type", "x-ds-pow-response"} {
		found := false
		for _, n := range names {
			if n == required {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected header %q on the wire, got %v", required, names)
		}
	}
}

// The HTTP/2 stack injects a gzip-only accept-encoding when the caller sets
// none. A client advertising Chrome but accepting only gzip is anomalous, so
// the explicit header must survive and must not be duplicated.
func TestChromeAcceptEncodingNotOverridden(t *testing.T) {
	got := captureChromeHandshake(t)

	var values []string
	for _, hf := range got.headers {
		if hf.Name == "accept-encoding" {
			values = append(values, hf.Value)
		}
	}
	if len(values) != 1 {
		t.Fatalf("accept-encoding appeared %d times (%v), want exactly once", len(values), values)
	}
	if values[0] != "gzip, deflate, br, zstd" {
		t.Fatalf("accept-encoding = %q, want Chrome's full set", values[0])
	}
}
