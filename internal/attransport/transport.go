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
