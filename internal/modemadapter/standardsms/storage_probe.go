package standardsms

import (
	"context"
	"strings"
	"sync"
)

// alternateStorage is the modem's own message memory, as opposed to the SIM's.
//
// The driver selects SIM storage for everything. 3GPP TS 23.038 message classes
// can in principle override that preference — class 1 is "ME-specific" — which
// would leave such a message in modem memory where the SIM listing never sees
// it, and where nothing ever deletes it.
//
// That is inference from the specification, not an observation. The purpose of
// this probe is to find out cheaply, over real operation, before paying for a
// two-pass listing: covering both storages properly means storage indices stop
// being unique on their own, which changes a persisted record shape and needs a
// schema migration.
const alternateStorage = "ME"

// AlternateStorageObserver is called when the probe finds messages in the
// alternate storage. It receives the storage name and its used count, never
// message content: the point is to learn whether anything lands there at all.
type AlternateStorageObserver func(storage string, used int)

// DriverOption configures optional driver behavior.
type DriverOption func(*Driver)

// WithAlternateStorageProbe enables an occasional check of the modem's own
// message memory, reporting through observer when it is not empty.
//
// It runs inside an existing listing's exclusive conversation, so nothing can
// interleave between selecting a storage and listing it. Placing the same probe
// in the bridge firmware would race: its periodic commands and the driver's share
// one serialized queue, so a probe could land between the driver's storage
// selection and its listing and make the driver list the wrong memory.
//
// every is the listing interval; a value below one disables the probe. It is
// opt-in so a model whose accepted evidence covers only SIM storage is not
// silently given new behavior.
func WithAlternateStorageProbe(every int, observer AlternateStorageObserver) DriverOption {
	return func(driver *Driver) {
		if every < 1 || observer == nil {
			return
		}
		driver.probeEvery = every
		driver.probeObserver = observer
	}
}

type storageProbeState struct {
	mu    sync.Mutex
	count int
}

// due reports whether this listing should carry a probe.
func (state *storageProbeState) due(every int) bool {
	if every < 1 {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.count++
	return state.count%every == 0
}

// probeAlternateStorage is best effort and never fails the listing that carries
// it. A diagnostic that can break a real operation is worse than no diagnostic.
//
// It restores SIM storage before returning. Every operation re-asserts storage
// selection anyway, so a failed restore cannot leak into the next one, but
// leaving the modem on the wrong memory would still be wrong for anything that
// reads state outside this driver.
func (driver *Driver) probeAlternateStorage(ctx context.Context, transport Transport, endpoint string) {
	if driver == nil || driver.probeObserver == nil || !driver.probeState.due(driver.probeEvery) {
		return
	}
	lines, err := transport.Command(ctx, endpoint, selectStorageCommand(alternateStorage), modeTimeout)
	if err != nil {
		return
	}
	defer func() {
		// Restore unconditionally, including when parsing the probe result failed.
		_, _ = transport.Command(ctx, endpoint, selectStorageCommand(primaryStorage), modeTimeout)
	}()
	body, err := responseBody(lines, false)
	if err != nil {
		return
	}
	used, ok := storageUsedCount(body)
	if !ok || used <= 0 {
		return
	}
	driver.probeObserver(alternateStorage, used)
}

// storageUsedCount reads the first used-count field of a storage selection
// response. It returns false rather than guessing on any unexpected shape.
func storageUsedCount(lines []string) (int, bool) {
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "+CPMS:") {
		return 0, false
	}
	fields, err := csvFields(strings.TrimSpace(strings.TrimPrefix(lines[0], "+CPMS:")))
	if err != nil || len(fields) < 2 {
		return 0, false
	}
	used, err := boundedInteger(fields[0], 0, maxStorageCount)
	if err != nil {
		return 0, false
	}
	return used, true
}
