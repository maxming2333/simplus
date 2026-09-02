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

var _ Transport = (*OpenerTransport)(nil)

func NewOpenerTransport(opener attransport.Opener) (*OpenerTransport, error) {
	if opener == nil {
		return nil, errors.New("3GPP SMS opener transport requires an AT transport")
	}
	return &OpenerTransport{opener: opener}, nil
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
	lines, err := attransport.PromptExchange(ctx, session, command, payload, timeout)
	if err != nil {
		// Only the transport's explicit "prompt never reached" signal proves the
		// payload had no effect. Everything else stays uncertain.
		if errors.Is(err, attransport.ErrPromptUnsupported) || errors.Is(err, attransport.ErrPromptNotReached) {
			return nil, errors.Join(ErrPromptNotDispatched, err)
		}
		return nil, err
	}
	return lines, nil
}

// validateExchange applies the same bounds as the tty transport so the choice of
// transport cannot change which commands a model may issue.
func validateExchange(endpoint, command string, timeout time.Duration) error {
	if endpoint == "" {
		return ErrControlEndpoint
	}
	return validateTTYExchange(endpoint, command, timeout)
}
