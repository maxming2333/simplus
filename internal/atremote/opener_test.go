package atremote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leonfox28/simplus/internal/attransport"
)

// fakeBridge is a synthetic peer that implements the bridge wire contract. It
// records what it received so tests can assert the exact request shape instead
// of reimplementing the production branch in an expected value.
type fakeBridge struct {
	mu sync.Mutex

	token          string
	openStatus     int
	openBody       string
	commandStatus  int
	commandBody    string
	commandDelay   time.Duration
	requireAuth    [2]string
	closedSessions []string
	commands       []commandRequest
	exchanges      []exchangeRequest
	recordHeaders  []string
	headers        []map[string]string
	openCount      int
	authFailures   int
}

func newFakeBridge() *fakeBridge {
	return &fakeBridge{token: "session-token-1", openStatus: http.StatusOK, commandStatus: http.StatusOK}
}

func (bridge *fakeBridge) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(sessionPath, func(writer http.ResponseWriter, request *http.Request) {
		if !bridge.authorized(request) {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch request.Method {
		case http.MethodPost:
			bridge.mu.Lock()
			bridge.openCount++
			status, body, token := bridge.openStatus, bridge.openBody, bridge.token
			bridge.mu.Unlock()
			if status != http.StatusOK {
				writer.WriteHeader(status)
				return
			}
			if body == "" {
				body = `{"session":"` + token + `","expiresInMs":30000}`
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, body)
		case http.MethodDelete:
			var decoded closeRequest
			_ = json.NewDecoder(io.LimitReader(request.Body, 4096)).Decode(&decoded)
			bridge.mu.Lock()
			bridge.closedSessions = append(bridge.closedSessions, decoded.Session)
			bridge.mu.Unlock()
			writer.WriteHeader(http.StatusNoContent)
		default:
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc(exchangePath, func(writer http.ResponseWriter, request *http.Request) {
		if !bridge.authorized(request) {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		bridge.captureHeaders(request)
		var decoded exchangeRequest
		if err := json.NewDecoder(io.LimitReader(request.Body, 8192)).Decode(&decoded); err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		bridge.mu.Lock()
		bridge.exchanges = append(bridge.exchanges, decoded)
		token := bridge.token
		bridge.mu.Unlock()
		if decoded.Session != token {
			writer.WriteHeader(http.StatusGone)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"lines":["+CMGS: 7","OK"]}`)
	})
	mux.HandleFunc(commandPath, func(writer http.ResponseWriter, request *http.Request) {
		if !bridge.authorized(request) {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		bridge.captureHeaders(request)
		var decoded commandRequest
		if err := json.NewDecoder(io.LimitReader(request.Body, 8192)).Decode(&decoded); err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		bridge.mu.Lock()
		bridge.commands = append(bridge.commands, decoded)
		status, body, delay, token := bridge.commandStatus, bridge.commandBody, bridge.commandDelay, bridge.token
		bridge.mu.Unlock()
		if decoded.Session != token {
			writer.WriteHeader(http.StatusGone)
			return
		}
		if delay > 0 {
			time.Sleep(delay)
		}
		if status != http.StatusOK {
			writer.WriteHeader(status)
			return
		}
		if body == "" {
			body = `{"lines":["OK"]}`
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, body)
	})
	return mux
}

func (bridge *fakeBridge) captureHeaders(request *http.Request) {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if len(bridge.recordHeaders) == 0 {
		return
	}
	seen := make(map[string]string, len(bridge.recordHeaders))
	for _, name := range bridge.recordHeaders {
		if value := request.Header.Get(name); value != "" {
			seen[name] = value
		}
	}
	bridge.headers = append(bridge.headers, seen)
}

func (bridge *fakeBridge) authorized(request *http.Request) bool {
	bridge.mu.Lock()
	expected := bridge.requireAuth
	bridge.mu.Unlock()
	if expected[0] == "" {
		return true
	}
	user, password, ok := request.BasicAuth()
	if ok && user == expected[0] && password == expected[1] {
		return true
	}
	bridge.mu.Lock()
	bridge.authFailures++
	bridge.mu.Unlock()
	return false
}

func (bridge *fakeBridge) snapshot() fakeBridge {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	return fakeBridge{
		closedSessions: append([]string(nil), bridge.closedSessions...),
		commands:       append([]commandRequest(nil), bridge.commands...),
		exchanges:      append([]exchangeRequest(nil), bridge.exchanges...),
		headers:        append([]map[string]string(nil), bridge.headers...),
		openCount:      bridge.openCount,
		authFailures:   bridge.authFailures,
	}
}

func startBridge(t *testing.T, bridge *fakeBridge, username, password string) *Opener {
	t.Helper()
	server := httptest.NewServer(bridge.handler())
	t.Cleanup(server.Close)
	target, err := NewTarget("esp32-a", server.URL, username, password, 2*time.Second)
	if err != nil {
		t.Fatalf("NewTarget: %v", err)
	}
	opener, err := NewOpener([]Target{target})
	if err != nil {
		t.Fatalf("NewOpener: %v", err)
	}
	return opener
}

func TestOpenAndQueryUseOneBridgeSessionAndCloseIt(t *testing.T) {
	bridge := newFakeBridge()
	bridge.commandBody = `{"lines":["+CPIN: READY","OK"]}`
	opener := startBridge(t, bridge, "agent", "secret")
	bridge.mu.Lock()
	bridge.requireAuth = [2]string{"agent", "secret"}
	bridge.mu.Unlock()

	session, err := opener.Open(Locator("esp32-a"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for range 3 {
		lines, queryErr := session.Query(context.Background(), "AT+CPIN?", 1500*time.Millisecond)
		if queryErr != nil {
			t.Fatalf("Query: %v", queryErr)
		}
		if len(lines) != 2 || lines[0] != "+CPIN: READY" || lines[1] != "OK" {
			t.Fatalf("lines = %#v", lines)
		}
	}
	session.Close()

	observed := bridge.snapshot()
	if observed.openCount != 1 {
		t.Fatalf("bridge sessions opened = %d, want 1", observed.openCount)
	}
	if observed.authFailures != 0 {
		t.Fatalf("bridge rejected the agent credential %d times", observed.authFailures)
	}
	if len(observed.commands) != 3 {
		t.Fatalf("bridge commands = %d, want 3", len(observed.commands))
	}
	for index, command := range observed.commands {
		if command.Session != "session-token-1" {
			t.Fatalf("command %d session = %q, want the single opened session", index, command.Session)
		}
		if command.Command != "AT+CPIN?" || command.TimeoutMS != 1500 {
			t.Fatalf("command %d = %+v", index, command)
		}
	}
	if len(observed.closedSessions) != 1 || observed.closedSessions[0] != "session-token-1" {
		t.Fatalf("closed sessions = %#v", observed.closedSessions)
	}
}

func TestCloseIsIdempotentAndFailsSubsequentQueries(t *testing.T) {
	bridge := newFakeBridge()
	opener := startBridge(t, bridge, "", "")
	session, err := opener.Open(Locator("esp32-a"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	session.Close()
	session.Close()
	if _, err := session.Query(context.Background(), "AT", time.Second); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("Query after Close error = %v, want ErrSessionClosed", err)
	}
	if closed := bridge.snapshot().closedSessions; len(closed) != 1 {
		t.Fatalf("close requests = %d, want 1", len(closed))
	}
}

func TestQueryRejectsUnboundedCommandsWithoutContactingBridge(t *testing.T) {
	bridge := newFakeBridge()
	opener := startBridge(t, bridge, "", "")
	session, err := opener.Open(Locator("esp32-a"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer session.Close()
	for _, testCase := range []struct{ name, command string }{
		{name: "empty", command: ""},
		{name: "carriage return", command: "AT\r"},
		{name: "line feed", command: "AT\n"},
		{name: "embedded newline", command: "AT+CFUN=1\nAT+CFUN=0"},
		{name: "oversize", command: "A" + strings.Repeat("T", maximumCommandLength)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := session.Query(context.Background(), testCase.command, time.Second); !errors.Is(err, ErrCommandInvalid) {
				t.Fatalf("Query error = %v, want ErrCommandInvalid", err)
			}
		})
	}
	if commands := bridge.snapshot().commands; len(commands) != 0 {
		t.Fatalf("bridge received %d rejected commands", len(commands))
	}
}

func TestQueryAcceptsExactCommandLengthBound(t *testing.T) {
	bridge := newFakeBridge()
	opener := startBridge(t, bridge, "", "")
	session, err := opener.Open(Locator("esp32-a"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer session.Close()
	command := "A" + strings.Repeat("T", maximumCommandLength-1)
	if _, err := session.Query(context.Background(), command, time.Second); err != nil {
		t.Fatalf("Query at the exact command bound: %v", err)
	}
}

func TestQueryRejectsUnboundedOrIncompleteResponses(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		body     string
		expected error
	}{
		{name: "no terminal status", body: `{"lines":["+CPIN: READY"]}`, expected: ErrResponseIncomplete},
		{name: "empty lines", body: `{"lines":[]}`, expected: ErrResponseIncomplete},
		{name: "only echo", body: `{"lines":["AT+CPIN?"]}`, expected: ErrResponseIncomplete},
		{name: "too many lines", body: `{"lines":[` + strings.TrimSuffix(strings.Repeat(`"x",`, maximumResponseLines+1), ",") + `]}`, expected: ErrResponseOversize},
		{name: "unknown field", body: `{"lines":["OK"],"urc":["+CMTI: 1"]}`, expected: ErrQueryFailed},
		{name: "not json", body: `OK`, expected: ErrQueryFailed},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			bridge := newFakeBridge()
			bridge.commandBody = testCase.body
			opener := startBridge(t, bridge, "", "")
			session, err := opener.Open(Locator("esp32-a"))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer session.Close()
			if _, err := session.Query(context.Background(), "AT+CPIN?", time.Second); !errors.Is(err, testCase.expected) {
				t.Fatalf("Query error = %v, want %v", err, testCase.expected)
			}
		})
	}
}

func TestQueryRejectsOversizeResponseBody(t *testing.T) {
	bridge := newFakeBridge()
	bridge.commandBody = `{"lines":["` + strings.Repeat("x", maximumResponseSize) + `","OK"]}`
	opener := startBridge(t, bridge, "", "")
	session, err := opener.Open(Locator("esp32-a"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer session.Close()
	if _, err := session.Query(context.Background(), "AT", time.Second); !errors.Is(err, ErrResponseOversize) {
		t.Fatalf("Query error = %v, want ErrResponseOversize", err)
	}
}

func TestQueryNormalizesBridgeLines(t *testing.T) {
	bridge := newFakeBridge()
	bridge.commandBody = `{"lines":["AT+CSQ\r","  +CSQ: 20,99  ","","\u0001\u0002","OK"]}`
	opener := startBridge(t, bridge, "", "")
	session, err := opener.Open(Locator("esp32-a"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer session.Close()
	lines, err := session.Query(context.Background(), "AT+CSQ", time.Second)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(lines) != 2 || lines[0] != "+CSQ: 20,99" || lines[1] != "OK" {
		t.Fatalf("lines = %#v", lines)
	}
}

func TestQueryClampsTimeoutSentToBridge(t *testing.T) {
	bridge := newFakeBridge()
	opener := startBridge(t, bridge, "", "")
	session, err := opener.Open(Locator("esp32-a"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer session.Close()
	for _, requested := range []time.Duration{0, -time.Second, time.Hour} {
		if _, err := session.Query(context.Background(), "AT", requested); err != nil {
			t.Fatalf("Query(%s): %v", requested, err)
		}
	}
	commands := bridge.snapshot().commands
	if len(commands) != 3 {
		t.Fatalf("commands = %d", len(commands))
	}
	// The ceiling is the target's own command timeout, not the protocol maximum:
	// a bridge must never be asked to occupy itself longer than it can afford.
	expected := []int64{
		minimumQueryTimeout.Milliseconds(),
		minimumQueryTimeout.Milliseconds(),
		defaultCommandTimeout.Milliseconds(),
	}
	for index, command := range commands {
		if command.TimeoutMS != expected[index] {
			t.Fatalf("command %d timeout = %d, want %d", index, command.TimeoutMS, expected[index])
		}
	}
}

func TestQueryHonorsCallerCancellation(t *testing.T) {
	bridge := newFakeBridge()
	bridge.commandDelay = 2 * time.Second
	opener := startBridge(t, bridge, "", "")
	session, err := opener.Open(Locator("esp32-a"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer session.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := session.Query(ctx, "AT", 5*time.Second); !errors.Is(err, ErrQueryFailed) {
		t.Fatalf("Query error = %v, want ErrQueryFailed", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Query ignored caller cancellation for %s", elapsed)
	}
}

func TestQueryRejectsStaleSessionResponse(t *testing.T) {
	bridge := newFakeBridge()
	opener := startBridge(t, bridge, "", "")
	session, err := opener.Open(Locator("esp32-a"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer session.Close()
	bridge.mu.Lock()
	bridge.token = "session-token-2"
	bridge.mu.Unlock()
	if _, err := session.Query(context.Background(), "AT", time.Second); !errors.Is(err, ErrQueryFailed) {
		t.Fatalf("Query error = %v, want ErrQueryFailed", err)
	}
}

func TestOpenClassifiesBridgeFailures(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		status    int
		body      string
		kind      string
		retryable bool
	}{
		{name: "conflict", status: http.StatusConflict, kind: attransport.OpenBusy, retryable: true},
		{name: "locked", status: http.StatusLocked, kind: attransport.OpenBusy, retryable: true},
		{name: "too many requests", status: http.StatusTooManyRequests, kind: attransport.OpenBusy, retryable: true},
		{name: "unauthorized", status: http.StatusUnauthorized, kind: attransport.OpenPermission},
		{name: "forbidden", status: http.StatusForbidden, kind: attransport.OpenPermission},
		{name: "bad request", status: http.StatusBadRequest, kind: attransport.OpenConfigure, retryable: true},
		{name: "unprocessable", status: http.StatusUnprocessableEntity, kind: attransport.OpenConfigure, retryable: true},
		{name: "not found", status: http.StatusNotFound, kind: attransport.OpenUnsupported},
		{name: "not implemented", status: http.StatusNotImplemented, kind: attransport.OpenUnsupported},
		{name: "method not allowed", status: http.StatusMethodNotAllowed, kind: attransport.OpenUnsupported},
		{name: "server error", status: http.StatusInternalServerError, kind: attransport.OpenUnavailable, retryable: true},
		{name: "gateway timeout", status: http.StatusGatewayTimeout, kind: attransport.OpenUnavailable, retryable: true},
		{name: "malformed session body", status: http.StatusOK, body: `{"session":"ok"`, kind: attransport.OpenConfigure, retryable: true},
		{name: "unknown session field", status: http.StatusOK, body: `{"session":"ok","mqtt":true}`, kind: attransport.OpenConfigure, retryable: true},
		{name: "empty session token", status: http.StatusOK, body: `{"session":"","expiresInMs":1}`, kind: attransport.OpenConfigure, retryable: true},
		{name: "invalid session token", status: http.StatusOK, body: `{"session":"tok en","expiresInMs":1}`, kind: attransport.OpenConfigure, retryable: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			bridge := newFakeBridge()
			bridge.openStatus = testCase.status
			bridge.openBody = testCase.body
			opener := startBridge(t, bridge, "", "")
			session, err := opener.Open(Locator("esp32-a"))
			if err == nil {
				session.Close()
				t.Fatal("Open accepted a failing bridge")
			}
			kind, retryable, ok := attransport.OpenFailure(err)
			if !ok {
				t.Fatalf("Open error is not a classified OpenError: %v", err)
			}
			if kind != testCase.kind || retryable != testCase.retryable {
				t.Fatalf("Open failure = %q retryable %v, want %q retryable %v", kind, retryable, testCase.kind, testCase.retryable)
			}
			if err.Error() != "AT endpoint is unavailable" {
				t.Fatalf("Open error text = %q; it must not describe the bridge", err.Error())
			}
		})
	}
}

func TestOpenRejectsUnknownAndMalformedLocators(t *testing.T) {
	bridge := newFakeBridge()
	opener := startBridge(t, bridge, "", "")
	for _, endpoint := range []string{
		Locator("esp32-b"), EndpointScheme + "BAD", EndpointScheme, "/dev/ttyUSB2", "",
	} {
		session, err := opener.Open(endpoint)
		if err == nil {
			session.Close()
			t.Fatalf("Open(%q) succeeded", endpoint)
		}
		kind, retryable, ok := attransport.OpenFailure(err)
		if !ok || kind != attransport.OpenUnavailable || retryable {
			t.Fatalf("Open(%q) failure = %q retryable %v", endpoint, kind, retryable)
		}
	}
	if observed := bridge.snapshot(); observed.openCount != 0 {
		t.Fatalf("bridge was contacted %d times for unknown locators", observed.openCount)
	}
}

func TestOpenRejectsUnreachableBridge(t *testing.T) {
	server := httptest.NewServer(newFakeBridge().handler())
	url := server.URL
	server.Close()
	target, err := NewTarget("esp32-a", url, "", "", time.Second)
	if err != nil {
		t.Fatalf("NewTarget: %v", err)
	}
	opener, err := NewOpener([]Target{target})
	if err != nil {
		t.Fatalf("NewOpener: %v", err)
	}
	session, err := opener.Open(Locator("esp32-a"))
	if err == nil {
		session.Close()
		t.Fatal("Open succeeded against a closed bridge")
	}
	kind, retryable, ok := attransport.OpenFailure(err)
	if !ok || kind != attransport.OpenUnavailable || !retryable {
		t.Fatalf("Open failure = %q retryable %v, want unavailable retryable", kind, retryable)
	}
}

func TestOpenRejectsRedirectingBridge(t *testing.T) {
	elsewhere := httptest.NewServer(newFakeBridge().handler())
	t.Cleanup(elsewhere.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, elsewhere.URL+sessionPath, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	target, err := NewTarget("esp32-a", redirector.URL, "", "", time.Second)
	if err != nil {
		t.Fatalf("NewTarget: %v", err)
	}
	opener, err := NewOpener([]Target{target})
	if err != nil {
		t.Fatalf("NewOpener: %v", err)
	}
	session, err := opener.Open(Locator("esp32-a"))
	if err == nil {
		session.Close()
		t.Fatal("Open followed a bridge redirect")
	}
}

func TestNilOpenerFailsClosed(t *testing.T) {
	var opener *Opener
	session, err := opener.Open(Locator("esp32-a"))
	if err == nil {
		session.Close()
		t.Fatal("nil opener returned a session")
	}
	kind, _, ok := attransport.OpenFailure(err)
	if !ok || kind != attransport.OpenUnsupported {
		t.Fatalf("nil opener failure = %q", kind)
	}
}

func TestConfiguredHeadersAndCeilingsReachTheBridge(t *testing.T) {
	bridge := newFakeBridge()
	bridge.recordHeaders = []string{"X-Bridge-Token", "Authorization"}
	server := httptest.NewServer(bridge.handler())
	t.Cleanup(server.Close)
	target, err := NewTargetWithOptions("esp32-a", server.URL, "", "", TargetOptions{
		RequestTimeout:  2 * time.Second,
		CommandTimeout:  3 * time.Second,
		ExchangeTimeout: 4 * time.Second,
		Headers:         map[string]string{"X-Bridge-Token": "opaque-token"},
	})
	if err != nil {
		t.Fatalf("NewTargetWithOptions: %v", err)
	}
	opener, err := NewOpener([]Target{target})
	if err != nil {
		t.Fatalf("NewOpener: %v", err)
	}
	session, err := opener.Open(Locator("esp32-a"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer session.Close()
	if _, err := session.Query(context.Background(), "AT", time.Hour); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if _, err := attransport.PromptExchange(context.Background(), session, "AT+CMGS=1", []byte("41\x1a"), time.Hour); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	observed := bridge.snapshot()
	if len(observed.commands) != 1 || observed.commands[0].TimeoutMS != 3000 {
		t.Fatalf("command timeout was not clamped to the target ceiling: %+v", observed.commands)
	}
	if len(observed.exchanges) != 1 || observed.exchanges[0].TimeoutMS != 4000 {
		t.Fatalf("exchange timeout was not clamped to the target ceiling: %+v", observed.exchanges)
	}
	for _, seen := range observed.headers {
		if seen["X-Bridge-Token"] != "opaque-token" {
			t.Fatalf("configured header did not reach the bridge: %#v", seen)
		}
	}
	if len(observed.headers) == 0 {
		t.Fatal("no request headers were recorded")
	}
}

func TestTargetRejectsUnsafeHeadersAndCeilings(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		options TargetOptions
	}{
		{name: "reserved content-type", options: TargetOptions{Headers: map[string]string{"Content-Type": "text/plain"}}},
		{name: "reserved host", options: TargetOptions{Headers: map[string]string{"host": "elsewhere"}}},
		{name: "reserved content-length", options: TargetOptions{Headers: map[string]string{"Content-Length": "0"}}},
		{name: "hop-by-hop connection", options: TargetOptions{Headers: map[string]string{"Connection": "close"}}},
		{name: "invalid header name", options: TargetOptions{Headers: map[string]string{"Bad Name": "x"}}},
		{name: "empty header value", options: TargetOptions{Headers: map[string]string{"X-Token": ""}}},
		{name: "control character in value", options: TargetOptions{Headers: map[string]string{"X-Token": "a\nb"}}},
		{name: "oversize header value", options: TargetOptions{Headers: map[string]string{"X-Token": strings.Repeat("x", maximumHeaderValueSize+1)}}},
		{name: "command timeout below bound", options: TargetOptions{CommandTimeout: time.Millisecond}},
		{name: "command timeout above bound", options: TargetOptions{CommandTimeout: time.Hour}},
		{name: "exchange timeout below bound", options: TargetOptions{ExchangeTimeout: time.Millisecond}},
		{name: "exchange timeout above bound", options: TargetOptions{ExchangeTimeout: time.Hour}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := NewTargetWithOptions("a", "http://192.0.2.10", "", "", testCase.options); err == nil {
				t.Fatal("NewTargetWithOptions accepted an unsafe option")
			}
		})
	}
	many := map[string]string{}
	for index := 0; index <= maximumHeaderCount; index++ {
		many[fmt.Sprintf("X-H%d", index)] = "v"
	}
	if _, err := NewTargetWithOptions("a", "http://192.0.2.10", "", "", TargetOptions{Headers: many}); err == nil {
		t.Fatal("NewTargetWithOptions accepted too many headers")
	}
}
