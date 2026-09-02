package atremote

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/leonfox28/simplus/internal/attransport"
)

// maximumPromptPayload bounds one prompted submission, matching the local tty
// transport. The largest shape in use is a single 3GPP SMS submit PDU in
// hexadecimal plus its terminator.
const maximumPromptPayload = 1024

var _ attransport.PromptSession = (*session)(nil)

type exchangeRequest struct {
	Session    string `json:"session"`
	Command    string `json:"command"`
	PayloadHex string `json:"payloadHex"`
	TimeoutMS  int64  `json:"timeoutMs"`
}

type exchangeResponse struct {
	Lines []string `json:"lines"`
}

// Exchange performs a prompted-payload exchange through the bridge.
//
// The payload is hex-encoded on the wire because it is binary: a 3GPP submit
// payload ends in a control character, which cannot travel literally in JSON.
// The bridge decodes it and writes the exact bytes.
//
// The failure split is the contract's most important part. A bridge answer that
// proves the payload was never written maps to attransport.ErrPromptNotReached,
// which tells the caller the operation had no effect. Anything else — including
// a lost response — stays an ordinary error, because the modem may already have
// submitted and a retry would send twice.
func (current *session) Exchange(ctx context.Context, command string, payload []byte, timeout time.Duration) ([]string, error) {
	if current == nil || current.closed || current.token == "" {
		return nil, errors.Join(attransport.ErrPromptNotReached, ErrSessionClosed)
	}
	if command == "" || len(command) > maximumCommandLength || strings.ContainsAny(command, "\r\n") {
		return nil, errors.Join(attransport.ErrPromptNotReached, ErrCommandInvalid)
	}
	if len(payload) == 0 || len(payload) > maximumPromptPayload {
		return nil, errors.Join(attransport.ErrPromptNotReached, ErrCommandInvalid)
	}
	bounded := boundedQueryTimeout(timeout, current.target.ExchangeTimeout)
	encoded := make([]byte, hex.EncodedLen(len(payload)))
	hex.Encode(encoded, payload)
	defer zero(encoded)
	request, err := json.Marshal(exchangeRequest{
		Session: current.token, Command: command,
		PayloadHex: string(encoded), TimeoutMS: bounded.Milliseconds(),
	})
	if err != nil {
		return nil, errors.Join(attransport.ErrPromptNotReached, ErrQueryFailed)
	}
	defer zero(request)

	requestCtx, cancel := context.WithTimeout(ctx, bounded+bridgeGrace)
	defer cancel()
	body, status, err := current.do(requestCtx, http.MethodPost, exchangePath, request)
	defer zero(body)
	if err != nil {
		// The request itself failed, so it is unknown whether the bridge reached
		// the prompt. Fail uncertain, not not-reached.
		return nil, ErrQueryFailed
	}
	switch status {
	case http.StatusOK:
	case http.StatusPreconditionFailed:
		// The bridge states it never reached the submit prompt, so nothing was
		// dispatched. This is the only status a caller may safely retry.
		return nil, errors.Join(attransport.ErrPromptNotReached, ErrQueryFailed)
	default:
		return nil, ErrQueryFailed
	}
	var decoded exchangeResponse
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
