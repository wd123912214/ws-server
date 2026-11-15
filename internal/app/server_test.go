package app

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ws-server/internal/relay"
	"ws-server/internal/whitelist"
)

const testToken = "test-secret-token"
const testSuperToken = "test-super-secret"

type testPeer struct {
	uid      string
	kind     relay.Kind
	mu       sync.Mutex
	onSend   func(string)
	closed   bool
	messages []string
}

func (p *testPeer) UID() string      { return p.uid }
func (p *testPeer) Kind() relay.Kind { return p.kind }
func (p *testPeer) CloseDuplicate()  { p.closed = true }
func (p *testPeer) Send(message string) error {
	p.mu.Lock()
	p.messages = append(p.messages, message)
	callback := p.onSend
	p.mu.Unlock()
	if callback != nil {
		callback(message)
	}
	return nil
}

func TestHealthAndHeaderAuthentication(t *testing.T) {
	s := New(Config{Token: testToken}, relay.New())

	health := httptest.NewRecorder()
	s.Handler().ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK || strings.TrimSpace(health.Body.String()) != "OK" {
		t.Fatalf("health = %d %q", health.Code, health.Body.String())
	}

	unauthorized := httptest.NewRecorder()
	s.Handler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/api", strings.NewReader("x-a-b")))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	badWS := httptest.NewRecorder()
	s.Handler().ServeHTTP(badWS, httptest.NewRequest(http.MethodGet, "/ws", nil))
	if badWS.Code != http.StatusUnauthorized {
		t.Fatalf("ws unauthorized status = %d", badWS.Code)
	}
}

func TestDebugLoggingFollowsConfig(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	disabled := New(Config{Token: testToken, Logger: logger, Debug: false}, relay.New())
	disabled.debug("hidden")
	if output.Len() != 0 {
		t.Fatalf("debug=false produced log: %q", output.String())
	}

	enabled := New(Config{Token: testToken, Logger: logger, Debug: true}, relay.New())
	enabled.debug("visible", "uid", "client")
	if !strings.Contains(output.String(), "[WS-DEBUG] visible") || !strings.Contains(output.String(), "uid=client") {
		t.Fatalf("debug=true log missing: %q", output.String())
	}
}

func TestHTTPListCommands(t *testing.T) {
	r := relay.New()
	offlinePeer := &testPeer{uid: "offline", kind: relay.KindClient}
	r.Register(offlinePeer)
	r.Unregister(offlinePeer)
	r.Register(&testPeer{uid: "online", kind: relay.KindController})
	s := New(Config{Token: testToken}, r)

	all := callAPI(t, s.Handler(), "0-0-0")
	if all.Code != http.StatusOK || strings.TrimSpace(all.Body.String()) != "0-0-offline,online" {
		t.Fatalf("all = %d %q", all.Code, all.Body.String())
	}
	online := callAPI(t, s.Handler(), "0-0-1")
	if online.Code != http.StatusOK || strings.TrimSpace(online.Body.String()) != "0-1-online" {
		t.Fatalf("online = %d %q", online.Code, online.Body.String())
	}
}

func TestHTTPRoundTripAndErrorStatuses(t *testing.T) {
	r := relay.New()
	s := New(Config{Token: testToken, RequestTimeout: 15 * time.Millisecond}, r)

	missing := callAPI(t, s.Handler(), "missing-a-b")
	if missing.Code != http.StatusNotFound || strings.TrimSpace(missing.Body.String()) != "TARGET_NOT_FOUND" {
		t.Fatalf("missing = %d %q", missing.Code, missing.Body.String())
	}

	offline := &testPeer{uid: "offline", kind: relay.KindClient}
	r.Register(offline)
	r.Unregister(offline)
	offlineResponse := callAPI(t, s.Handler(), "offline-a-b")
	if offlineResponse.Code != http.StatusConflict || strings.TrimSpace(offlineResponse.Body.String()) != "TARGET_OFFLINE" {
		t.Fatalf("offline = %d %q", offlineResponse.Code, offlineResponse.Body.String())
	}

	timeoutTarget := &testPeer{uid: "slow", kind: relay.KindClient}
	r.Register(timeoutTarget)
	timedOut := callAPI(t, s.Handler(), "slow-a-b")
	if timedOut.Code != http.StatusGatewayTimeout || strings.TrimSpace(timedOut.Body.String()) != "TARGET_TIMEOUT" {
		t.Fatalf("timeout = %d %q", timedOut.Code, timedOut.Body.String())
	}

	responder := &testPeer{uid: "target", kind: relay.KindClient}
	responder.onSend = func(forwarded string) {
		parts := strings.SplitN(forwarded, "-", 3)
		go func() { _ = r.Handle(responder, parts[0]+"-result-ok") }()
	}
	r.Register(responder)
	success := callAPI(t, s.Handler(), "target-command-payload")
	if success.Code != http.StatusOK || strings.TrimSpace(success.Body.String()) != "target-result-ok" {
		t.Fatalf("success = %d %q", success.Code, success.Body.String())
	}
}

func TestHTTPRejectsWrongMethodLargeBodyAndMalformedMessage(t *testing.T) {
	s := New(Config{Token: testToken, MaxMessageBytes: 16}, relay.New())

	method := authorizedRequest(http.MethodGet, "/api", "x-a-b")
	methodResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(methodResponse, method)
	if methodResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status = %d", methodResponse.Code)
	}

	large := callAPI(t, s.Handler(), strings.Repeat("x", 17))
	if large.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large status = %d", large.Code)
	}

	invalid := callAPI(t, s.Handler(), "bad")
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d", invalid.Code)
	}
}

func TestWhitelistRejectsUnknownWSUIDBeforeUpgrade(t *testing.T) {
	manager, err := whitelist.New([]string{"allowed"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{Token: testToken, SuperAdminToken: testSuperToken, Whitelist: manager}, relay.New())

	request := httptest.NewRequest(http.MethodGet, "/ws", nil)
	request.Header.Set("X-Token", testToken)
	request.Header.Set("X-UID", "blocked")
	request.Header.Set("X-Type", "client")
	recorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || strings.TrimSpace(recorder.Body.String()) != "UID_NOT_ALLOWED" {
		t.Fatalf("blocked WS = %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestSuperAdminCanQueryAndReplaceWhitelist(t *testing.T) {
	persisted := []string(nil)
	manager, err := whitelist.New([]string{"removed", "kept"}, func(uids []string) error {
		persisted = append([]string(nil), uids...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r := relay.New()
	removed := &testPeer{uid: "removed", kind: relay.KindClient}
	kept := &testPeer{uid: "kept", kind: relay.KindController}
	r.Register(removed)
	r.Register(kept)
	s := New(Config{Token: testToken, SuperAdminToken: testSuperToken, Whitelist: manager}, r)

	forbidden := callAPI(t, s.Handler(), "7-7-7")
	if forbidden.Code != http.StatusForbidden || strings.TrimSpace(forbidden.Body.String()) != "SUPER_ADMIN_REQUIRED" {
		t.Fatalf("forbidden = %d %q", forbidden.Code, forbidden.Body.String())
	}

	query := callSuperAPI(t, s.Handler(), "7-7-7")
	if query.Code != http.StatusOK || strings.TrimSpace(query.Body.String()) != "7-7-kept*removed" {
		t.Fatalf("query = %d %q", query.Code, query.Body.String())
	}

	replace := callSuperAPI(t, s.Handler(), "8-8-new*kept*new")
	if replace.Code != http.StatusOK || strings.TrimSpace(replace.Body.String()) != "8-8-kept*new" {
		t.Fatalf("replace = %d %q", replace.Code, replace.Body.String())
	}
	if !removed.closed || kept.closed || !slices.Equal(persisted, []string{"kept", "new"}) {
		t.Fatalf("removed.closed=%v kept.closed=%v persisted=%v", removed.closed, kept.closed, persisted)
	}

	invalid := callSuperAPI(t, s.Handler(), "8-8-bad-uid")
	if invalid.Code != http.StatusBadRequest || strings.TrimSpace(invalid.Body.String()) != "INVALID_WHITELIST" {
		t.Fatalf("invalid = %d %q", invalid.Code, invalid.Body.String())
	}
}

func callAPI(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, authorizedRequest(http.MethodPost, "/api", body))
	return recorder
}

func authorizedRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("X-Token", testToken)
	return request
}

func callSuperAPI(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := authorizedRequest(http.MethodPost, "/api", body)
	request.Header.Set("X-Super-Token", testSuperToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}
