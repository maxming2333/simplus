package attransport

import (
	"context"
	"errors"
	"strings"
	"time"
)

const (
	OpenBusy        = "busy"
	OpenPermission  = "permission"
	OpenUnavailable = "unavailable"
	OpenConfigure   = "configure"
	OpenUnsupported = "unsupported"
)

// Query is the only command surface exposed to compiled-in modem adapters.
// It is never reachable from Web/API boundaries.
type Query func(context.Context, string, time.Duration) ([]string, error)

type Session interface {
	Query(context.Context, string, time.Duration) ([]string, error)
	Close()
}

// PromptSession adds the one framing shape that 3GPP TS 27.005 requires beyond
// request/response: a command that answers with a submit prompt instead of a
// terminal status, after which the caller writes a payload.
//
// It is optional. A transport that cannot express it must simply not implement
// it, and PromptExchange then fails closed rather than guessing.
type PromptSession interface {
	Session
	Exchange(context.Context, string, []byte, time.Duration) ([]string, error)
}

var (
	// ErrPromptUnsupported means the selected transport has no prompted-payload
	// framing. A caller must treat the operation as not attempted.
	ErrPromptUnsupported = errors.New("AT transport does not support a prompted payload exchange")

	// ErrPromptNotReached means the command was written but the submit prompt
	// never arrived, so the payload was never dispatched. This is the only
	// prompt failure a caller may retry without risking a duplicate side effect.
	ErrPromptNotReached = errors.New("AT submit prompt was not reached")
)

// PromptExchange performs a prompted-payload exchange on a session that
// supports it. It exists so callers do not scatter type assertions, and so a
// transport without prompt framing fails with one stable sentinel.
func PromptExchange(ctx context.Context, session Session, command string, payload []byte, timeout time.Duration) ([]string, error) {
	prompt, ok := session.(PromptSession)
	if !ok || prompt == nil {
		return nil, ErrPromptUnsupported
	}
	return prompt.Exchange(ctx, command, payload, timeout)
}

type Opener interface {
	Open(string) (Session, error)
}

type OpenError struct {
	Kind      string
	Retryable bool
	cause     error
}

// NewOpenError builds a classified open failure for an out-of-package Opener
// implementation. The cause stays unexported so a transport cannot leak an
// endpoint path, bridge URL or credential through Error(); it is reachable only
// through errors.Unwrap by a caller that already owns the transport.
func NewOpenError(kind string, retryable bool, cause error) *OpenError {
	return &OpenError{Kind: kind, Retryable: retryable, cause: cause}
}

func (err *OpenError) Error() string { return "AT endpoint is unavailable" }

func (err *OpenError) Unwrap() error { return err.cause }

func OpenFailure(err error) (kind string, retryable bool, ok bool) {
	var failure *OpenError
	if !errors.As(err, &failure) {
		return "", false, false
	}
	return failure.Kind, failure.Retryable, true
}

func NewOpener() Opener { return newPlatformOpener() }

// SubmitPromptByte is the 3GPP TS 27.005 submit prompt. Recognizing it is
// framing, exactly like recognizing OK/ERROR in HasTerminalResponse: the
// transport owns when a payload may be written, while the model adapter owns
// which command produced the prompt and what the payload contains.
const SubmitPromptByte = '>'

func HasTerminalResponse(lines []string) bool {
	for _, line := range lines {
		if line == "OK" || line == "ERROR" || strings.HasPrefix(line, "+CME ERROR:") || strings.HasPrefix(line, "+CMS ERROR:") {
			return true
		}
	}
	return false
}

func HasTerminalOK(lines []string) bool {
	for _, line := range lines {
		if line == "OK" {
			return true
		}
	}
	return false
}
