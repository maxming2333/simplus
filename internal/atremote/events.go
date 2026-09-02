package atremote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

// Inbound call notifications are read from the bridge, not asked of the modem.
//
// The bridge records the modem's caller-line URCs into a small ring of its own
// and serves them over a plain HTTP read. That is deliberately not an AT query:
// nothing here claims the modem UART, takes the operation gate, or goes through
// a session. A notification must not be able to block a message being sent.
//
// The alternative — having the bridge append synthetic listing entries so calls
// arrive disguised as messages — was rejected. It would need the bridge to forge
// a valid delivery PDU, invent a storage index that cannot collide with the
// modem's own, and then intercept reads and deletes for that index. It turns a
// transport into a data source, and upper layers would receive data no modem
// produced.
const eventsCallsPath = "/events/calls"

const (
	// callEventsCapacity is the bridge's ring size. A longer list than this did
	// not come from the bridge we agreed to talk to, so it is refused rather than
	// truncated: a truncated event list would silently skip calls.
	callEventsCapacity = 32

	// callEventsResponseSize bounds the reply independently of the AT transcript
	// ceiling. A full ring is roughly 32 entries of about 80 bytes plus the
	// envelope, so this leaves generous headroom while staying far below what a
	// bridge could use to force a large allocation. It is separate from
	// Target.ResponseSize because that value is tuned for storage listings and a
	// bridge configured with a small transcript ceiling must still be able to
	// report its calls.
	callEventsResponseSize = 16384

	// callEventsTimeout bounds one poll. It is short on purpose: the read is
	// served from the bridge's RAM with no modem involved, so a slow answer means
	// the bridge is in trouble and the next tick is a better use of the budget
	// than waiting. The shared client's own timeout is minutes long and cannot
	// serve as this bound.
	callEventsTimeout = 5 * time.Second

	// bootIDLength is the bridge's per-boot identifier width, 16 hexadecimal
	// characters.
	bootIDLength = 16
)

// Stable, credential-safe call event errors. As with the session errors, none
// carries a bridge host, URL, credential or raw response body.
var (
	ErrCallEventsUnavailable = errors.New("bridge call events are unavailable")
	ErrCallEventsFailed      = errors.New("bridge call event read failed")
	ErrCallEventsInvalid     = errors.New("bridge returned an invalid call event report")
)

var bootIDPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// CallEvent is one observed inbound call.
type CallEvent struct {
	// Sequence is monotonic within one boot and restarts at zero, which is why a
	// consumer's cursor is only meaningful together with BootID.
	Sequence uint32 `json:"sequence"`
	// Number is the caller's line, or placeholder text when the network withheld
	// it. It is not guaranteed to be a dialable number, so a consumer must
	// validate it rather than assume.
	Number string `json:"number"`
	// ObservedAt is the bridge's wall clock in Unix seconds, or zero when its
	// clock was not yet synchronized. Zero means unknown, never 1970: a consumer
	// must fall back to its own receive time.
	ObservedAt uint32 `json:"observedAt"`
	// ObservedMs is the bridge's uptime at the moment of recording, which lets a
	// consumer compute relative spacing even when the wall clock was unset.
	ObservedMs uint32 `json:"observedMs"`
}

// CallEventsSnapshot is one bounded read of a bridge's call ring.
type CallEventsSnapshot struct {
	// BootID identifies this run of the bridge. A consumer must compare it before
	// anything else: sequences restart at zero, so a consumer holding a cursor
	// from a previous boot would otherwise never read another event again.
	BootID string `json:"bootId"`
	// LatestSequence is the highest sequence the bridge has assigned.
	LatestSequence uint32 `json:"latestSequence"`
	// OldestSequence is the lowest sequence the bridge still holds. It is what
	// lets a consumer derive its own loss exactly:
	//
	//	lost = max(0, OldestSequence - (cursor + 1))
	//
	// The bridge does not count overwrites itself. It does not know what any
	// consumer has read and there may be several, so an overwrite counter would
	// inflate as soon as the ring wraps even when every entry had been consumed.
	OldestSequence uint32 `json:"oldestSequence"`
	// UptimeMs is the bridge's uptime, carried for correlation only.
	UptimeMs uint32 `json:"uptimeMs"`
	// Events are the entries after the requested cursor, in ascending sequence.
	Events []CallEvent `json:"events"`
}

// CallEvents reads the call events a bridge holds after the given cursor.
//
// endpoint is the same opaque control-endpoint locator the AT path uses, so a
// caller never has to know how a bridge is addressed. Resolving it here is what
// keeps the routing scheme inside this package.
func (opener *Opener) CallEvents(ctx context.Context, endpoint string, after uint32) (CallEventsSnapshot, error) {
	if opener == nil || len(opener.targets) == 0 {
		return CallEventsSnapshot{}, ErrCallEventsUnavailable
	}
	key, ok := ParseLocator(endpoint)
	if !ok {
		return CallEventsSnapshot{}, ErrCallEventsUnavailable
	}
	target, ok := opener.targets[key]
	if !ok {
		return CallEventsSnapshot{}, ErrCallEventsUnavailable
	}
	// A tokenless session value reuses the one place that gets bridge requests
	// right: base URL composition, configured headers ahead of Basic auth, the
	// response limit reader, and credential scrubbing. Rewriting that for a
	// second call site would eventually omit one of them.
	current := &session{
		client:        opener.client,
		target:        target,
		timeout:       target.RequestTimeout,
		responseLimit: callEventsResponseSize,
	}
	requestCtx, cancel := context.WithTimeout(ctx, callEventsTimeout)
	defer cancel()
	path := eventsCallsPath + "?after=" + strconv.FormatUint(uint64(after), 10) +
		"&limit=" + strconv.Itoa(callEventsCapacity)
	body, status, err := current.do(requestCtx, http.MethodGet, path, nil)
	defer zero(body)
	if err != nil {
		return CallEventsSnapshot{}, ErrCallEventsFailed
	}
	if status != http.StatusOK {
		return CallEventsSnapshot{}, ErrCallEventsFailed
	}
	return decodeCallEvents(body)
}

// decodeCallEvents parses and validates a bridge reply, rejecting anything
// unexpected outright. A partially trusted report is worse than no report: the
// cursor it would advance cannot be walked back.
func decodeCallEvents(body []byte) (CallEventsSnapshot, error) {
	var snapshot CallEventsSnapshot
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return CallEventsSnapshot{}, ErrCallEventsInvalid
	}
	if !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return CallEventsSnapshot{}, ErrCallEventsInvalid
	}
	if !bootIDPattern.MatchString(snapshot.BootID) {
		return CallEventsSnapshot{}, ErrCallEventsInvalid
	}
	if len(snapshot.Events) > callEventsCapacity {
		return CallEventsSnapshot{}, ErrCallEventsInvalid
	}
	if snapshot.OldestSequence > snapshot.LatestSequence {
		return CallEventsSnapshot{}, ErrCallEventsInvalid
	}
	// Ascending, strictly increasing, and within what the bridge claims to hold.
	// Out-of-order entries would make a cursor skip events, and a sequence above
	// the reported latest means the report is internally inconsistent.
	previous := uint32(0)
	for index, event := range snapshot.Events {
		if event.Sequence == 0 || event.Sequence > snapshot.LatestSequence {
			return CallEventsSnapshot{}, ErrCallEventsInvalid
		}
		if index > 0 && event.Sequence <= previous {
			return CallEventsSnapshot{}, ErrCallEventsInvalid
		}
		previous = event.Sequence
		if len(event.Number) > maximumCallNumberLength {
			return CallEventsSnapshot{}, ErrCallEventsInvalid
		}
	}
	return snapshot, nil
}

// maximumCallNumberLength matches the bridge's own field width. The number is
// carried as opaque text here; whether it is dialable is the consumer's judgement.
const maximumCallNumberLength = 23
