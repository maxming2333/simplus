package calls

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/leonfox28/simplus/internal/application/inventory"
	"github.com/leonfox28/simplus/internal/domain/call"
)

// ErrPersistence marks a failure to make a record durable. It is distinguished
// because it is the one failure that must not advance a read position.
var ErrPersistence = errors.New("call record persistence failed")

// ErrCallEventsUnsupported means this Line's modem keeps no ring of observed
// calls. It is a supported configuration, not a fault: a locally attached modem's
// caller-line notifications are gone before anything could poll for them. The
// sweep treats it as "nothing to read here" so it is not retried and logged every
// two seconds forever.
var ErrCallEventsUnsupported = errors.New("call events are unsupported for this line")

// ObservedCall is one inbound call a modem saw and nothing answered.
type ObservedCall struct {
	Sequence uint32
	// Number is the caller's line as reported, which may be placeholder text when
	// the network withheld it. Whether it is usable is decided here.
	Number     string
	ObservedAt time.Time
}

// CallEventTarget addresses one bounded read of a device's observed calls.
type CallEventTarget struct {
	DeviceID         string
	DeviceGeneration uint64
	After            uint32
}

// CallEventReport is one bounded read.
type CallEventReport struct {
	BootID         string
	LatestSequence uint32
	OldestSequence uint32
	Events         []ObservedCall
}

// CallEventReader reads observed inbound calls for a device. The port is narrow
// on purpose: it can read notifications and nothing else. It cannot place,
// answer, reject or end a call, and no widening of this interface should give it
// the ability to.
type CallEventReader interface {
	ReadCallEvents(context.Context, CallEventTarget) (CallEventReport, error)
}

// InboundCallSyncResult is what one sweep did. It carries counts and no caller
// numbers: the warnings a caller derives from it are operational, and a log is
// not where a record of who called belongs.
type InboundCallSyncResult struct {
	// LinesPolled counts the Lines whose bridge was read this sweep.
	LinesPolled int
	// Recorded counts calls made durable for the first time.
	Recorded int
	// AlreadyKnown counts events whose record already existed, which is the
	// expected outcome of re-reading after a crash between persisting and saving
	// the read position.
	AlreadyKnown int
	// Degraded counts events that could not become a record — an unusable caller
	// address — and were skipped so they could not block every later event behind
	// them.
	Degraded int
	// BridgeRestarts counts bridges whose identifier changed, meaning any events
	// not yet read are gone with the bridge's memory.
	BridgeRestarts int
	// SubscriptionChanges counts bridges whose SIM changed, where the events still
	// held arrived under the previous subscription and were skipped rather than
	// attributed to the current one.
	SubscriptionChanges int
	// LostEvents counts calls that really happened and can never be recovered,
	// derived from this consumer's own position. It never becomes a record: one
	// with no number and no time would be indistinguishable from a real missed call
	// and would corrupt the data this exists to produce.
	LostEvents int
}

// SyncInboundCalls reads every voice-capable Line's bridge once and records what
// it finds.
//
// A failure on one Line never aborts the sweep; failures are joined and the loop
// continues, because one unreachable bridge must not hide another's calls.
func (service *Service) SyncInboundCalls(ctx context.Context) (InboundCallSyncResult, error) {
	var result InboundCallSyncResult
	if service == nil || service.events == nil {
		return result, nil
	}
	topology, err := service.lines.Topology(ctx)
	if err != nil {
		return result, err
	}
	var failures error
	for _, line := range topology.Lines {
		// Readiness is the only filter here. CellularVoice would be the wrong gate:
		// it means the Line can carry a voice call, and the agent-reported mapping
		// hardcodes it false precisely because that is unproven — gating on it would
		// make this sweep poll nothing at all on the hardware backend and the feature
		// would be silently dead.
		//
		// A notification is not a voice capability. Whether a particular device can
		// report observed calls is answered by the device itself: one with no event
		// ring returns ErrCallEventsUnsupported and is skipped quietly below.
		if line.State != inventory.LineReady {
			continue
		}
		target, resolveErr := resolveCallEventLine(topology, line)
		if resolveErr != nil {
			// An unresolvable Line is skipped silently rather than joined as a
			// failure: it is the ordinary state of a Line whose device or SIM is not
			// currently present, not something that went wrong.
			continue
		}
		lineResult, lineErr := service.syncLineCalls(ctx, target)
		result.add(lineResult)
		if errors.Is(lineErr, ErrCallEventsUnsupported) {
			continue
		}
		if lineErr != nil {
			failures = errors.Join(failures, lineErr)
		}
	}
	return result, failures
}

func (service *Service) syncLineCalls(ctx context.Context, target callEventLine) (InboundCallSyncResult, error) {
	var result InboundCallSyncResult
	stored, hadCursor, err := service.repository.CallEventCursorFor(ctx, target.deviceID)
	if err != nil {
		return result, err
	}
	report, err := service.readCallEvents(ctx, target, stored, hadCursor)
	if err != nil {
		return result, err
	}
	result.LinesPolled = 1

	position := stored.LastSequence
	switch {
	case hadCursor && stored.BootID != report.BootID:
		// The bridge restarted. Its sequences restarted at zero too, so the stored
		// position is meaningless and anything unread went with the bridge's memory.
		// Resetting is the only correct action, and the changed identifier is what
		// says so rather than something inferred.
		result.BridgeRestarts = 1
		position = 0
	case hadCursor && stored.SubscriptionFingerprint != target.subscriptionFingerprint:
		// The SIM changed. Events still in the ring arrived under the previous
		// subscription, so they are skipped forward rather than replayed: recording
		// them against this Line would invent history that never happened on it.
		result.SubscriptionChanges = 1
		position = report.LatestSequence
	}

	// Loss is derived from this consumer's own position, which is exact per reader.
	// The bridge cannot compute it: it does not know what any consumer has read and
	// there may be several, so a bridge-side overwrite counter would inflate as soon
	// as the ring wrapped even when every entry had been consumed.
	if report.OldestSequence > position+1 {
		result.LostEvents = int(report.OldestSequence - (position + 1))
	}

	events := make([]ObservedCall, len(report.Events))
	copy(events, report.Events)
	sort.Slice(events, func(first, second int) bool { return events[first].Sequence < events[second].Sequence })
	for _, event := range events {
		if event.Sequence <= position {
			continue
		}
		recorded, err := service.recordObservedCall(ctx, target, report.BootID, event)
		if err != nil {
			// The read position is not advanced, so this event is re-read next sweep
			// and its stable identity absorbs the repeat. Advancing first would lose
			// it with no way to notice.
			return result, err
		}
		switch recorded {
		case recordedNew:
			result.Recorded++
		case recordedAlreadyKnown:
			result.AlreadyKnown++
		case recordedDegraded:
			// Advance past it anyway. An event that cannot become a record must not
			// block every later event behind it, the same judgement that makes an
			// undecodable message degrade rather than stall its batch.
			result.Degraded++
		}
		position = event.Sequence
	}

	next := call.EventCursor{
		BootID:                  report.BootID,
		SubscriptionFingerprint: target.subscriptionFingerprint,
		LastSequence:            position,
	}
	if hadCursor && stored == next {
		return result, nil
	}
	if err := service.repository.SaveCallEventCursor(ctx, target.deviceID, next, service.now().UTC()); err != nil {
		return result, err
	}
	return result, nil
}

// readCallEvents performs the bounded read, re-reading from the start exactly
// once when the stored position turns out to belong to a previous boot.
//
// The re-read is necessary because the cursor has to be sent before the boot
// identifier is known. Filtering with a stale position would silently skip the new
// boot's first events, and always reading from zero would pay for a whole ring on
// every sweep forever to avoid a cost incurred once per restart.
func (service *Service) readCallEvents(ctx context.Context, target callEventLine, stored call.EventCursor, hadCursor bool) (CallEventReport, error) {
	after := uint32(0)
	if hadCursor {
		after = stored.LastSequence
	}
	report, err := service.events.ReadCallEvents(ctx, CallEventTarget{
		DeviceID: target.deviceID, DeviceGeneration: target.generation, After: after,
	})
	if err != nil {
		return CallEventReport{}, err
	}
	if after == 0 || report.BootID == stored.BootID {
		return report, nil
	}
	return service.events.ReadCallEvents(ctx, CallEventTarget{
		DeviceID: target.deviceID, DeviceGeneration: target.generation, After: 0,
	})
}

type recordOutcome int

const (
	recordedNew recordOutcome = iota
	recordedAlreadyKnown
	recordedDegraded
)

func (service *Service) recordObservedCall(ctx context.Context, target callEventLine, bootID string, event ObservedCall) (recordOutcome, error) {
	if !numberPattern.MatchString(event.Number) {
		// A withheld or placeholder caller cannot be stored as an address. It is
		// reported as degraded rather than stored as something invented.
		return recordedDegraded, nil
	}
	observed := event.ObservedAt.UTC()
	if observed.IsZero() {
		// Defence in depth. The reader is required to substitute its own receive
		// time, so this should be unreachable; recording 1970 as an arrival time
		// would be worse than the cost of the check.
		observed = service.now().UTC()
	}
	identity := observedCallIdentity(target.deviceID, bootID, event.Sequence)
	service.mu.Lock()
	defer service.mu.Unlock()
	_, replayed, err := service.repository.RecordObservedInboundCall(ctx, call.Record{
		ID:            "call_" + identity,
		OperationID:   "obs_" + identity,
		LineID:        target.line.ID,
		RemoteAddress: event.Number,
		Direction:     call.DirectionInbound,
		State:         call.StateEnded,
		EndReason:     call.ReasonNotAnswered,
		CreatedAt:     observed,
		UpdatedAt:     observed,
	})
	if err != nil {
		return recordedDegraded, fmt.Errorf("%w: record observed inbound call: %v", ErrPersistence, err)
	}
	if replayed {
		return recordedAlreadyKnown, nil
	}
	return recordedNew, nil
}

// observedCallIdentity derives a stable identity from the facts that make an
// event unique. The boot identifier is essential: without it sequence one of a new
// boot collides with sequence one of the previous boot, and the second call would
// be absorbed as a replay of the first.
func observedCallIdentity(deviceID, bootID string, sequence uint32) string {
	digest := sha256.Sum256([]byte(deviceID + "\x00" + bootID + "\x00" + strconv.FormatUint(uint64(sequence), 10)))
	return base64.RawURLEncoding.EncodeToString(digest[:16])
}

type callEventLine struct {
	line                    inventory.Line
	deviceID                string
	generation              uint64
	subscriptionFingerprint string
}

// resolveCallEventLine derives the transport identity of a Line's device and the
// subscription its calls belong to. It is the same resolution the messaging path
// performs, and it fails for the same reasons: a Line whose device or active
// profile is not currently identifiable has no bridge to read.
func resolveCallEventLine(topology inventory.Topology, line inventory.Line) (callEventLine, error) {
	runtime, err := inventory.ResolveRuntimeIdentity(topology, line)
	if err != nil {
		return callEventLine{}, err
	}
	return callEventLine{
		line: line, deviceID: runtime.TransportDeviceID, generation: line.Generation,
		subscriptionFingerprint: runtime.SubscriptionFingerprint,
	}, nil
}

func (result *InboundCallSyncResult) add(other InboundCallSyncResult) {
	result.LinesPolled += other.LinesPolled
	result.Recorded += other.Recorded
	result.AlreadyKnown += other.AlreadyKnown
	result.Degraded += other.Degraded
	result.BridgeRestarts += other.BridgeRestarts
	result.SubscriptionChanges += other.SubscriptionChanges
	result.LostEvents += other.LostEvents
}
