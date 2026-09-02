package standardsms

import (
	"context"
	"errors"
	"time"

	"github.com/leonfox28/simplus/internal/attransport"
)

// OpenerTransport runs the 3GPP SMS transcript over the shared AT transport
// seam instead of a dedicated tty implementation.
//
// This is what lets one SMS driver serve both a locally attached modem and one
// reached through another control transport: the composition root decides which
// transport the opener resolves to, and this type never learns the difference.
// TTYTransport remains for models whose accepted evidence was collected through
// it; the two are alternatives, never fallbacks for each other.
type OpenerTransport struct {
	opener attransport.Opener
}

// ScopedTransport is implemented by a transport that can hold one exclusive
// conversation for the duration of one SMS operation.
//
// It is optional. A transport without it runs one conversation per command,
// which is correct but pays the mode/storage selection cost repeatedly and lets
// another consumer interleave between the setup and the operation.
type ScopedTransport interface {
	Transport
	Begin(endpoint string) (Transport, func(), error)
}

var (
	_ Transport       = (*OpenerTransport)(nil)
	_ ScopedTransport = (*OpenerTransport)(nil)
	_ Transport       = (*boundTransport)(nil)
)

func NewOpenerTransport(opener attransport.Opener) (*OpenerTransport, error) {
	if opener == nil {
		return nil, errors.New("3GPP SMS opener transport requires an AT transport")
	}
	return &OpenerTransport{opener: opener}, nil
}

// Begin claims one conversation for a whole operation. The returned Transport
// serves only the endpoint it was opened for; a mismatched endpoint is refused
// rather than silently redirected.
func (transport *OpenerTransport) Begin(endpoint string) (Transport, func(), error) {
	if transport == nil || transport.opener == nil {
		return nil, nil, ErrControlEndpoint
	}
	if endpoint == "" {
		return nil, nil, ErrControlEndpoint
	}
	session, err := transport.opener.Open(endpoint)
	if err != nil {
		return nil, nil, errors.Join(ErrTransport, err)
	}
	bound := &boundTransport{endpoint: endpoint, session: session}
	return bound, func() { session.Close() }, nil
}

// boundTransport runs commands on one already-open conversation.
type boundTransport struct {
	endpoint string
	session  attransport.Session
}

func (bound *boundTransport) Command(ctx context.Context, endpoint, command string, timeout time.Duration) ([]string, error) {
	if bound == nil || bound.session == nil || endpoint != bound.endpoint {
		return nil, ErrControlEndpoint
	}
	if err := validateExchange(endpoint, command, timeout); err != nil {
		return nil, err
	}
	lines, err := bound.session.Query(ctx, command, timeout)
	if err != nil {
		return nil, errors.Join(ErrTransport, err)
	}
	return lines, nil
}

func (bound *boundTransport) Prompt(ctx context.Context, endpoint, command string, payload []byte, timeout time.Duration) ([]string, error) {
	if bound == nil || bound.session == nil || endpoint != bound.endpoint {
		return nil, errors.Join(ErrPromptNotDispatched, ErrControlEndpoint)
	}
	if err := validateExchange(endpoint, command, timeout); err != nil {
		return nil, errors.Join(ErrPromptNotDispatched, err)
	}
	if err := validateSubmitPayload(payload); err != nil {
		return nil, errors.Join(ErrPromptNotDispatched, err)
	}
	return classifyPromptResult(attransport.PromptExchange(ctx, bound.session, command, payload, timeout))
}

// classifyPromptResult keeps the not-dispatched decision in one place: only the
// transport's explicit signals prove the payload had no effect, so everything
// else must stay uncertain and must not be retried.
func classifyPromptResult(lines []string, err error) ([]string, error) {
	if err == nil {
		return lines, nil
	}
	if errors.Is(err, attransport.ErrPromptUnsupported) || errors.Is(err, attransport.ErrPromptNotReached) {
		return nil, errors.Join(ErrPromptNotDispatched, err)
	}
	return nil, err
}

func (transport *OpenerTransport) Command(ctx context.Context, endpoint, command string, timeout time.Duration) ([]string, error) {
	if transport == nil || transport.opener == nil {
		return nil, ErrControlEndpoint
	}
	if err := validateExchange(endpoint, command, timeout); err != nil {
		return nil, err
	}
	session, err := transport.opener.Open(endpoint)
	if err != nil {
		return nil, errors.Join(ErrTransport, err)
	}
	defer session.Close()
	lines, err := session.Query(ctx, command, timeout)
	if err != nil {
		return nil, errors.Join(ErrTransport, err)
	}
	return lines, nil
}

// Prompt performs the submit-prompt exchange. A transport without prompt framing
// fails closed with ErrPromptNotDispatched rather than pretending the payload
// was sent, because the caller uses that distinction to decide whether a retry
// could duplicate a message.
func (transport *OpenerTransport) Prompt(ctx context.Context, endpoint, command string, payload []byte, timeout time.Duration) ([]string, error) {
	if transport == nil || transport.opener == nil {
		return nil, errors.Join(ErrPromptNotDispatched, ErrControlEndpoint)
	}
	if err := validateExchange(endpoint, command, timeout); err != nil {
		return nil, errors.Join(ErrPromptNotDispatched, err)
	}
	if err := validateSubmitPayload(payload); err != nil {
		return nil, errors.Join(ErrPromptNotDispatched, err)
	}
	session, err := transport.opener.Open(endpoint)
	if err != nil {
		return nil, errors.Join(ErrPromptNotDispatched, ErrTransport, err)
	}
	defer session.Close()
	return classifyPromptResult(attransport.PromptExchange(ctx, session, command, payload, timeout))
}

// validateExchange applies the same bounds as the tty transport so the choice of
// transport cannot change which commands a model may issue.
func validateExchange(endpoint, command string, timeout time.Duration) error {
	if endpoint == "" {
		return ErrControlEndpoint
	}
	return validateTTYExchange(endpoint, command, timeout)
}
