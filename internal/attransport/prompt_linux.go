//go:build linux

package attransport

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

var _ PromptSession = (*session)(nil)

// Exchange writes a command, waits for the submit prompt, writes the payload,
// and returns the terminal response lines.
//
// The failure split matters more than the happy path. Anything that goes wrong
// before the payload reaches the modem is reported as ErrPromptNotReached, which
// tells the caller the operation had no effect. Once the payload has been
// written, a later failure is returned as an ordinary error, because the modem
// may have acted on it: a caller that retried would risk sending twice.
func (current *session) Exchange(ctx context.Context, command string, payload []byte, timeout time.Duration) ([]string, error) {
	if current == nil || current.fd < 0 {
		return nil, errors.Join(ErrPromptNotReached, errors.New("AT session is closed"))
	}
	if command == "" || len(command) > maximumCommandLength || strings.ContainsAny(command, "\r\n") {
		return nil, errors.Join(ErrPromptNotReached, errors.New("invalid bounded AT query"))
	}
	if len(payload) == 0 || len(payload) > maximumPromptPayload {
		return nil, errors.Join(ErrPromptNotReached, errors.New("invalid bounded AT submit payload"))
	}
	deadline := time.Now().Add(timeout)
	_ = unix.IoctlSetInt(current.fd, unix.TCFLSH, unix.TCIFLUSH)
	wireCommand := []byte(command + "\r")
	defer zero(wireCommand)
	if err := current.writeAll(ctx, wireCommand, deadline); err != nil {
		return nil, errors.Join(ErrPromptNotReached, err)
	}

	buffer := make([]byte, 0, 512)
	temporary := make([]byte, 256)
	defer func() { zero(buffer) }()
	defer zero(temporary)
	promptSeen := false
	for !promptSeen {
		if time.Now().After(deadline) {
			return nil, errors.Join(ErrPromptNotReached, errors.New("AT submit prompt timed out"))
		}
		count, err := current.readOnce(ctx, temporary, deadline)
		if err != nil {
			return nil, errors.Join(ErrPromptNotReached, err)
		}
		if count == 0 {
			continue
		}
		if len(buffer)+count > maximumResponseSize {
			return nil, errors.Join(ErrPromptNotReached, errors.New("AT response exceeds bounded size"))
		}
		buffer = append(buffer, temporary[:count]...)
		// An early terminal status means the modem rejected the command outright,
		// so no payload was ever invited. That is still "not reached".
		if HasTerminalResponse(splitLines(string(buffer), command)) {
			return nil, errors.Join(ErrPromptNotReached, errors.New("AT command was rejected before the submit prompt"))
		}
		promptSeen = bytes.IndexByte(buffer, SubmitPromptByte) >= 0
	}

	wirePayload := append([]byte(nil), payload...)
	defer zero(wirePayload)
	if err := current.writeAll(ctx, wirePayload, deadline); err != nil {
		// A partial payload write leaves the modem mid-submission. Report it as an
		// ordinary failure: the caller must treat the outcome as uncertain.
		return nil, err
	}

	buffer = buffer[:0]
	for {
		if time.Now().After(deadline) {
			return nil, errors.New("AT query timed out")
		}
		count, err := current.readOnce(ctx, temporary, deadline)
		if err != nil {
			return nil, err
		}
		if count == 0 {
			continue
		}
		if len(buffer)+count > maximumResponseSize {
			return nil, errors.New("AT response exceeds bounded size")
		}
		buffer = append(buffer, temporary[:count]...)
		lines := splitLines(string(buffer), command)
		if HasTerminalResponse(lines) {
			return lines, nil
		}
	}
}

func (current *session) writeAll(ctx context.Context, payload []byte, deadline time.Time) error {
	for len(payload) != 0 {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return errors.New("AT write timed out")
		}
		if err := pollContext(ctx, current.fd, unix.POLLOUT, remaining); err != nil {
			return err
		}
		written, err := unix.Write(current.fd, payload)
		if err != nil {
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
				continue
			}
			return err
		}
		payload = payload[written:]
	}
	return nil
}

func (current *session) readOnce(ctx context.Context, into []byte, deadline time.Time) (int, error) {
	if err := pollContext(ctx, current.fd, unix.POLLIN, time.Until(deadline)); err != nil {
		return 0, err
	}
	count, err := unix.Read(current.fd, into)
	if err != nil {
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			return 0, nil
		}
		return 0, err
	}
	return count, nil
}
