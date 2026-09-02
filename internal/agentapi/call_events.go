package agentapi

import (
	"context"
	"errors"
	"strings"
	"time"
)

// FeatureCallEvents advertises bounded reads of observed inbound calls.
const FeatureCallEvents = "call-events-v1"

// CallEventsTimeout is the whole agent-side budget for one read. It is short
// because the bridge serves these from its own memory with no modem involved: a
// slow answer means the bridge is in trouble, and the caller's next poll is a
// better use of the budget than waiting.
const CallEventsTimeout = 10 * time.Second

// maximumCallEvents bounds one reply. It matches the bridge's ring size. A
// longer list is refused rather than truncated, because truncating would
// silently skip calls.
const maximumCallEvents = 32

// maximumCallNumberLength matches the bridge's own field width for a caller line.
const maximumCallNumberLength = 23

// bootIDLength is the width of a bridge's per-boot identifier.
const bootIDLength = 16

var (
	ErrCallEventsRequestInvalid = errors.New("invalid Agent call events request")
	ErrCallEventsAgentStale     = errors.New("Agent instance changed")
	ErrCallEventsDeviceNotFound = errors.New("Agent call events device is not present")
	ErrCallEventsDeviceStale    = errors.New("Agent call events device generation changed")
	ErrCallEventsUnsupported    = errors.New("Agent call events are unsupported for this device")
	ErrCallEventsUnavailable    = errors.New("Agent call events are unavailable")
	ErrCallEventsIdentity       = errors.New("Agent call events identity changed")
	ErrCallEventsBackendInvalid = errors.New("Agent call events backend returned an invalid report")
)

// CallEventsRequest asks for the events a device's bridge holds after a cursor.
type CallEventsRequest struct {
	AgentInstanceID string `json:"agentInstanceId"`
	DeviceID        string `json:"deviceId"`
	// After is the consumer's cursor. Only events with a strictly greater
	// sequence are returned. It is only meaningful together with the BootID the
	// consumer last saw, which is why the reply always carries one.
	After            uint32 `json:"after"`
	DeviceGeneration uint64 `json:"deviceGeneration"`
	// The identity expectations are required for the same reason the message
	// operations require them, even though these events come from the bridge's
	// memory rather than the SIM. The call arrived on a radio belonging to
	// whichever subscription was present at the time; the bridge's ring outlives a
	// SIM change, so reading it without pinning identity could attribute a call to
	// a subscription that was not there when it arrived. That would corrupt exactly
	// the data this operation exists to produce.
	ExpectedEquipmentFingerprint    string `json:"expectedEquipmentFingerprint"`
	ExpectedSubscriptionFingerprint string `json:"expectedSubscriptionFingerprint"`
}

// CallEvent is one observed inbound call.
type CallEvent struct {
	// Sequence is monotonic within one boot of the bridge and restarts at zero.
	Sequence uint32 `json:"sequence"`
	// Number is the caller's line, or placeholder text when the network withheld
	// it. It is carried as opaque text: whether it is a dialable number is the
	// consumer's judgement, not this boundary's.
	Number string `json:"number"`
	// ObservedAt is when the call arrived. It is never zero. When the bridge's own
	// clock was unsynchronized the backend substitutes the moment it received the
	// report, so an ambiguous value cannot cross this boundary and no consumer can
	// mistake an unknown time for 1970.
	ObservedAt time.Time `json:"observedAt"`
}

// CallEventsResponse is one bounded read of a bridge's call ring.
type CallEventsResponse struct {
	ProtocolVersion int    `json:"protocolVersion"`
	AgentInstanceID string `json:"agentInstanceId"`
	// BootID identifies the bridge's current run. A consumer must compare it
	// before using anything else here: sequences restart at zero, so a consumer
	// holding a cursor from a previous boot would otherwise never read another
	// event again.
	BootID string `json:"bootId"`
	// LatestSequence is the highest sequence the bridge has assigned.
	LatestSequence uint32 `json:"latestSequence"`
	// OldestSequence is the lowest the bridge still holds, which lets a consumer
	// derive its own loss exactly as max(0, oldest-(cursor+1)). The bridge does
	// not count overwrites itself: it does not know what any consumer has read and
	// there may be several, so such a counter would inflate as soon as the ring
	// wrapped even when every entry had been consumed.
	OldestSequence uint32 `json:"oldestSequence"`
	// Events are ascending by sequence and never nil.
	Events []CallEvent `json:"events"`
}

// CallEventsBackend reads a device's observed inbound calls.
//
// It receives the whole request and resolves the device itself, so this package
// never learns how a bridge is addressed. That keeps the transport's routing
// scheme out of the protocol layer, the same way the RF backend owns endpoint
// resolution.
type CallEventsBackend interface {
	CallEvents(ctx context.Context, request CallEventsRequest) (CallEventsResponse, error)
}

// CallEventsService validates a request against current hardware state before
// letting a backend read anything.
type CallEventsService struct {
	monitor *Monitor
	backend CallEventsBackend
}

// NewCallEventsService returns nil when either dependency is missing, so a
// deployment without a configured bridge exposes no route at all rather than one
// that fails at request time.
func NewCallEventsService(monitor *Monitor, backend CallEventsBackend) *CallEventsService {
	if monitor == nil || backend == nil {
		return nil
	}
	return &CallEventsService{monitor: monitor, backend: backend}
}

// Read validates staleness and identity, then reads. Every check fails closed.
func (service *CallEventsService) Read(ctx context.Context, request CallEventsRequest) (CallEventsResponse, error) {
	if service == nil || service.backend == nil {
		return CallEventsResponse{}, ErrCallEventsUnavailable
	}
	if err := validateCallEventsRequest(request); err != nil {
		return CallEventsResponse{}, err
	}
	if request.AgentInstanceID != service.monitor.InstanceID() {
		return CallEventsResponse{}, ErrCallEventsAgentStale
	}
	snapshot := service.monitor.Snapshot()
	device, found := DeviceReport{}, false
	for _, candidate := range snapshot.Devices {
		if candidate.ID == request.DeviceID {
			device, found = candidate, true
			break
		}
	}
	if !found {
		return CallEventsResponse{}, ErrCallEventsDeviceNotFound
	}
	if device.Generation != request.DeviceGeneration {
		return CallEventsResponse{}, ErrCallEventsDeviceStale
	}
	response, err := service.backend.CallEvents(ctx, request)
	if err != nil {
		return CallEventsResponse{}, err
	}
	response.ProtocolVersion = ProtocolVersion
	response.AgentInstanceID = service.monitor.InstanceID()
	if err := validateCallEventsResponse(response, request); err != nil {
		return CallEventsResponse{}, ErrCallEventsBackendInvalid
	}
	return response, nil
}

func validateCallEventsRequest(request CallEventsRequest) error {
	if !IsValidAgentInstanceID(request.AgentInstanceID) ||
		strings.TrimSpace(request.DeviceID) == "" || len(request.DeviceID) > 128 ||
		request.DeviceGeneration == 0 ||
		!isSHA256Hex(request.ExpectedEquipmentFingerprint) ||
		!isSHA256Hex(request.ExpectedSubscriptionFingerprint) {
		return ErrCallEventsRequestInvalid
	}
	return nil
}

// validateCallEventsResponse is applied by the server before replying and by the
// client after receiving, so neither end trusts the other. A report that would
// advance a consumer's cursor incorrectly cannot be walked back, which is why it
// is refused outright rather than partially accepted.
func validateCallEventsResponse(response CallEventsResponse, request CallEventsRequest) error {
	if err := validateSMSEnvelope(response.ProtocolVersion, response.AgentInstanceID, request.AgentInstanceID); err != nil {
		return ErrCallEventsBackendInvalid
	}
	if !isLowerHex(response.BootID, bootIDLength) {
		return ErrCallEventsBackendInvalid
	}
	if response.Events == nil || len(response.Events) > maximumCallEvents {
		return ErrCallEventsBackendInvalid
	}
	if response.OldestSequence > response.LatestSequence {
		return ErrCallEventsBackendInvalid
	}
	previous := request.After
	for _, event := range response.Events {
		// Strictly ascending and past the requested cursor. Anything else would
		// make a consumer skip an event or reprocess one it already recorded.
		if event.Sequence <= previous || event.Sequence > response.LatestSequence {
			return ErrCallEventsBackendInvalid
		}
		previous = event.Sequence
		if len(event.Number) > maximumCallNumberLength {
			return ErrCallEventsBackendInvalid
		}
		// A zero time must never reach a consumer: the backend substitutes its own
		// receive time when the bridge's clock was unset, precisely so nothing
		// downstream has to decide whether zero means 1970 or unknown.
		if event.ObservedAt.IsZero() {
			return ErrCallEventsBackendInvalid
		}
	}
	return nil
}

func isLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
