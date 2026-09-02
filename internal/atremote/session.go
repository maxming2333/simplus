package atremote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/leonfox28/simplus/internal/attransport"
)

// Bounds mirror internal/attransport/session_linux.go exactly. A bridged
// conversation must not accept a command or response that the locally attached
// path would refuse, otherwise the transport choice would change adapter
// behavior.
const (
	maximumCommandLength = 1024
	maximumResponseSize  = 8192
	maximumResponseLines = 256

	minimumQueryTimeout = 100 * time.Millisecond
	maximumQueryTimeout = 180 * time.Second

	// bridgeGrace lets the bridge answer with its own bounded timeout status
	// before the HTTP request itself is abandoned.
	bridgeGrace = 5 * time.Second
)

// Stable, credential-safe session errors. None of them carries a bridge host,
// URL, credential or raw response body.
var (
	ErrSessionClosed      = errors.New("AT session is closed")
	ErrCommandInvalid     = errors.New("invalid bounded AT query")
	ErrResponseOversize   = errors.New("AT response exceeds bounded size")
	ErrResponseIncomplete = errors.New("AT response has no terminal status")
	ErrQueryFailed        = errors.New("AT query failed")
)

type commandRequest struct {
	Session   string `json:"session"`
	Command   string `json:"command"`
	TimeoutMS int64  `json:"timeoutMs"`
}

type commandResponse struct {
	Lines []string `json:"lines"`
}

type closeRequest struct {
	Session string `json:"session"`
}

type session struct {
	client  *http.Client
	target  Target
	token   string
	closed  bool
	timeout time.Duration
}

var _ attransport.Session = (*session)(nil)

// Query sends one bounded command over the bridge session and returns the
// response lines once a terminal status is present. It never retries: a lost
// bridged command has the same uncertain-outcome shape as a lost tty command,
// and resending it could repeat a side effect.
func (current *session) Query(ctx context.Context, command string, timeout time.Duration) ([]string, error) {
	if current == nil || current.closed || current.token == "" {
		return nil, ErrSessionClosed
	}
	if command == "" || len(command) > maximumCommandLength || strings.ContainsAny(command, "\r\n") {
		return nil, ErrCommandInvalid
	}
	bounded := boundedQueryTimeout(timeout, current.target.CommandTimeout)
	payload, err := json.Marshal(commandRequest{
		Session: current.token, Command: command, TimeoutMS: bounded.Milliseconds(),
	})
	if err != nil {
		return nil, ErrQueryFailed
	}
	defer zero(payload)

	requestCtx, cancel := context.WithTimeout(ctx, bounded+bridgeGrace)
	defer cancel()
	body, status, err := current.do(requestCtx, http.MethodPost, commandPath, payload)
	defer zero(body)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, ErrQueryFailed
	}
	var decoded commandResponse
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&decoded); decodeErr != nil {
		return nil, ErrQueryFailed
	}
	lines, boundsErr := normalizeLines(decoded.Lines, command)
	if boundsErr != nil {
		return nil, boundsErr
	}
	if !attransport.HasTerminalResponse(lines) {
		return nil, ErrResponseIncomplete
	}
	return lines, nil
}

// Close releases the bridge's exclusive hold on the modem UART. It is best
// effort by design: a bridge that lost the request must expire the session on
// its own, and a failed release must not turn into an unbounded retry inside a
// deferred Close.
func (current *session) Close() {
	if current == nil || current.closed {
		return
	}
	token := current.token
	current.closed = true
	current.token = ""
	if token == "" {
		return
	}
	payload, err := json.Marshal(closeRequest{Session: token})
	if err != nil {
		return
	}
	defer zero(payload)
	ctx, cancel := context.WithTimeout(context.Background(), current.timeout)
	defer cancel()
	body, _, _ := current.do(ctx, http.MethodDelete, sessionPath, payload)
	zero(body)
}

// do performs one bounded JSON request and returns the size-limited body.
// Response bodies are read through a limit reader so a hostile or broken bridge
// cannot force an unbounded allocation.
func (current *session) do(ctx context.Context, method, path string, payload []byte) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(ctx, method, current.target.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, ErrQueryFailed
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	// Configured headers are applied before Basic auth so a bridge that
	// authenticates differently can still not displace an explicit credential.
	for name, value := range current.target.Headers {
		request.Header.Set(name, value)
	}
	if current.target.Username != "" {
		request.SetBasicAuth(current.target.Username, current.target.Password)
	}
	response, err := current.client.Do(request)
	if err != nil {
		return nil, 0, ErrQueryFailed
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maximumResponseSize))
		_ = response.Body.Close()
	}()
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseSize+1))
	if err != nil {
		zero(body)
		return nil, response.StatusCode, ErrQueryFailed
	}
	if len(body) > maximumResponseSize {
		zero(body)
		return nil, response.StatusCode, ErrResponseOversize
	}
	return body, response.StatusCode, nil
}

// normalizeLines applies the same rules as the tty transport's splitLines:
// carriage returns split lines, whitespace is trimmed, empty lines and the
// echoed command are dropped, and control characters are removed. The bridge is
// not trusted to have done any of this.
func normalizeLines(raw []string, command string) ([]string, error) {
	if len(raw) > maximumResponseLines {
		return nil, ErrResponseOversize
	}
	lines := make([]string, 0, len(raw))
	total := 0
	for _, entry := range raw {
		entry = strings.ReplaceAll(entry, "\r", "\n")
		for _, part := range strings.Split(entry, "\n") {
			part = strings.TrimSpace(part)
			if part == "" || part == command {
				continue
			}
			part = safeText(part, maximumResponseSize)
			if part == "" || part == command {
				continue
			}
			total += len(part)
			if total > maximumResponseSize || len(lines) >= maximumResponseLines {
				return nil, ErrResponseOversize
			}
			lines = append(lines, part)
		}
	}
	return lines, nil
}

// boundedQueryTimeout clamps a caller timeout into what this bridge accepts.
//
// The ceiling is per-bridge on purpose. A caller's budget is chosen for a
// locally attached modem; a bridge may only be able to occupy itself for a
// fraction of it, and exceeding that makes the bridge unresponsive instead of
// producing a useful answer. Clamping here keeps the caller's contract intact
// and turns the difference into an explicit bounded outcome.
func boundedQueryTimeout(timeout, ceiling time.Duration) time.Duration {
	if ceiling <= 0 || ceiling > maximumQueryTimeout {
		ceiling = maximumQueryTimeout
	}
	if timeout < minimumQueryTimeout {
		return minimumQueryTimeout
	}
	if timeout > ceiling {
		return ceiling
	}
	return timeout
}

func safeText(value string, limit int) string {
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return -1
		}
		return character
	}, value)
	value = strings.TrimSpace(value)
	if len(value) > limit {
		value = value[:limit]
	}
	return value
}

func zero(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
