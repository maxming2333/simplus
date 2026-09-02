package calls

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leonfox28/simplus/internal/application/inventory"
	"github.com/leonfox28/simplus/internal/domain/call"
	"github.com/leonfox28/simplus/internal/domain/hardware"
	"github.com/leonfox28/simplus/internal/domain/pagination"
)

const (
	syncDeviceID    = "bridge-esp32-a"
	syncLineID      = "line_0123456789012345678901"
	syncBootID      = "0f3a1c2b4d5e6f70"
	syncEquipment   = "1111111111111111111111111111111111111111111111111111111111111111"
	syncSubscriberA = "2222222222222222222222222222222222222222222222222222222222222222"
	syncSubscriberB = "3333333333333333333333333333333333333333333333333333333333333333"
)

// callStore is an in-memory repository that records the order of the two writes
// that matter: making a record durable, and saving the read position.
type callStore struct {
	cursors map[string]call.EventCursor
	records map[string]call.Record
	order   []string

	recordErr error
	cursorErr error
}

func newCallStore() *callStore {
	return &callStore{cursors: map[string]call.EventCursor{}, records: map[string]call.Record{}}
}

func (store *callStore) CallEventCursorFor(_ context.Context, deviceID string) (call.EventCursor, bool, error) {
	cursor, found := store.cursors[deviceID]
	return cursor, found, nil
}

func (store *callStore) SaveCallEventCursor(_ context.Context, deviceID string, cursor call.EventCursor, _ time.Time) error {
	if store.cursorErr != nil {
		return store.cursorErr
	}
	store.order = append(store.order, "cursor:"+deviceID)
	store.cursors[deviceID] = cursor
	return nil
}

func (store *callStore) RecordObservedInboundCall(_ context.Context, value call.Record) (call.Record, bool, error) {
	if store.recordErr != nil {
		return call.Record{}, false, store.recordErr
	}
	store.order = append(store.order, "record:"+value.OperationID)
	if existing, found := store.records[value.OperationID]; found {
		return existing, true, nil
	}
	store.records[value.OperationID] = value
	return value, false, nil
}

func (store *callStore) CreateCall(_ context.Context, value call.Record) (call.Record, bool, error) {
	return value, false, nil
}

func (store *callStore) SetCallState(_ context.Context, _, _, _ string, _ time.Time) (call.Record, error) {
	return call.Record{}, nil
}

func (store *callStore) ListCallsPage(context.Context, pagination.Request) (pagination.Page[call.Record], error) {
	return pagination.Page[call.Record]{}, nil
}

func (store *callStore) GetCallByOperation(context.Context, string) (call.Record, bool, error) {
	return call.Record{}, false, nil
}

func (store *callStore) GetCallByID(context.Context, string) (call.Record, bool, error) {
	return call.Record{}, false, nil
}

func (store *callStore) HasActiveCallForLine(context.Context, string) (bool, error) {
	return false, nil
}

func (store *callStore) ReconcileCalls(context.Context, string, time.Time) (int64, error) {
	return 0, nil
}

type staticLines struct{ topology inventory.Topology }

func (lines staticLines) Topology(context.Context) (inventory.Topology, error) {
	return lines.topology, nil
}

func voiceTopology(subscription string) inventory.Topology {
	return inventory.Topology{
		Devices: []hardware.PhysicalDevice{{ID: syncDeviceID, EquipmentIdentityFingerprint: syncEquipment}},
		SubscriptionProfiles: []inventory.SubscriptionProfile{{SubscriptionProfile: hardware.SubscriptionProfile{
			ID: "profile-1", State: hardware.ProfileActive, IdentityFingerprint: subscription,
		}}},
		Lines: []inventory.Line{{
			ID: syncLineID, PhysicalDeviceID: syncDeviceID, SubscriptionProfileID: "profile-1",
			Generation: 4, State: inventory.LineReady,
			Capabilities: hardware.Capabilities{CellularVoice: true},
		}},
	}
}

// scriptedReader answers a queue of reports and records every cursor it was asked
// for, so a test can prove how many reads a sweep performed.
type scriptedReader struct {
	reports []CallEventReport
	err     error
	asked   []uint32
}

func (reader *scriptedReader) ReadCallEvents(_ context.Context, target CallEventTarget) (CallEventReport, error) {
	reader.asked = append(reader.asked, target.After)
	if reader.err != nil {
		return CallEventReport{}, reader.err
	}
	if len(reader.reports) == 0 {
		return CallEventReport{}, errors.New("no scripted report")
	}
	report := reader.reports[0]
	if len(reader.reports) > 1 {
		reader.reports = reader.reports[1:]
	}
	return report, nil
}

func newSyncService(t *testing.T, store *callStore, reader CallEventReader, subscription string) *Service {
	t.Helper()
	service, err := New(t.Context(), store, staticLines{topology: voiceTopology(subscription)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	service.now = func() time.Time { return time.Unix(1772600000, 0).UTC() }
	service.UseCallEventReader(reader)
	return service
}

func observedAt(offset int64) time.Time { return time.Unix(1772505600+offset, 0).UTC() }

func report(bootID string, oldest, latest uint32, events ...ObservedCall) CallEventReport {
	return CallEventReport{BootID: bootID, OldestSequence: oldest, LatestSequence: latest, Events: events}
}

func TestSyncInboundCallsRecordsAndAdvances(t *testing.T) {
	store := newCallStore()
	reader := &scriptedReader{reports: []CallEventReport{report(syncBootID, 1, 2,
		ObservedCall{Sequence: 1, Number: "13000000001", ObservedAt: observedAt(0)},
		ObservedCall{Sequence: 2, Number: "+8613000000002", ObservedAt: observedAt(60)},
	)}}
	service := newSyncService(t, store, reader, syncSubscriberA)

	result, err := service.SyncInboundCalls(t.Context())
	if err != nil {
		t.Fatalf("SyncInboundCalls: %v", err)
	}
	if result.LinesPolled != 1 || result.Recorded != 2 || result.AlreadyKnown != 0 || result.LostEvents != 0 {
		t.Fatalf("result = %+v", result)
	}
	if got := store.cursors[syncDeviceID]; got.LastSequence != 2 || got.BootID != syncBootID ||
		got.SubscriptionFingerprint != syncSubscriberA {
		t.Fatalf("cursor = %+v", got)
	}
	// Every record is a call nobody answered, recorded as terminal against the Line.
	for _, record := range store.records {
		if record.Direction != call.DirectionInbound || record.State != call.StateEnded ||
			record.EndReason != call.ReasonNotAnswered || record.LineID != syncLineID {
			t.Fatalf("record = %+v", record)
		}
		if record.CreatedAt.IsZero() {
			t.Fatal("a record was stored with no arrival time")
		}
	}
}

// TestSyncInboundCallsPersistsBeforeAdvancing is the ordering that makes a crash
// survivable: the record has to be durable before the position moves past it.
func TestSyncInboundCallsPersistsBeforeAdvancing(t *testing.T) {
	store := newCallStore()
	reader := &scriptedReader{reports: []CallEventReport{report(syncBootID, 1, 1,
		ObservedCall{Sequence: 1, Number: "13000000001", ObservedAt: observedAt(0)},
	)}}
	service := newSyncService(t, store, reader, syncSubscriberA)
	if _, err := service.SyncInboundCalls(t.Context()); err != nil {
		t.Fatalf("SyncInboundCalls: %v", err)
	}
	if len(store.order) != 2 || store.order[1] != "cursor:"+syncDeviceID {
		t.Fatalf("write order = %v, want the record durable before the position moved", store.order)
	}
}

func TestSyncInboundCallsLeavesThePositionOnAPersistenceFailure(t *testing.T) {
	store := newCallStore()
	store.recordErr = errors.New("disk is gone")
	reader := &scriptedReader{reports: []CallEventReport{report(syncBootID, 1, 1,
		ObservedCall{Sequence: 1, Number: "13000000001", ObservedAt: observedAt(0)},
	)}}
	service := newSyncService(t, store, reader, syncSubscriberA)
	result, err := service.SyncInboundCalls(t.Context())
	if !errors.Is(err, ErrPersistence) {
		t.Fatalf("err = %v, want a persistence failure", err)
	}
	if result.Recorded != 0 {
		t.Fatalf("result = %+v", result)
	}
	// The position must not have moved, so the event is re-read next sweep.
	if _, found := store.cursors[syncDeviceID]; found {
		t.Fatal("the read position advanced past an event that was never stored")
	}
}

func TestSyncInboundCallsAbsorbsARepeatedEvent(t *testing.T) {
	store := newCallStore()
	event := ObservedCall{Sequence: 1, Number: "13000000001", ObservedAt: observedAt(0)}
	reader := &scriptedReader{reports: []CallEventReport{report(syncBootID, 1, 1, event)}}
	service := newSyncService(t, store, reader, syncSubscriberA)
	if _, err := service.SyncInboundCalls(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash between persisting and saving: the position is rolled back
	// but the record stayed.
	delete(store.cursors, syncDeviceID)
	result, err := service.SyncInboundCalls(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.AlreadyKnown != 1 || result.Recorded != 0 {
		t.Fatalf("result = %+v, want the repeat absorbed", result)
	}
	if len(store.records) != 1 {
		t.Fatalf("records = %d, want the repeat absorbed", len(store.records))
	}
}

// TestSyncInboundCallsSeparatesTheSameSequenceAcrossTwoBoots is why the identity
// includes the boot identifier: without it the first call after a restart would be
// absorbed as a replay of the first call before it.
func TestSyncInboundCallsSeparatesTheSameSequenceAcrossTwoBoots(t *testing.T) {
	store := newCallStore()
	first := report(syncBootID, 1, 1, ObservedCall{Sequence: 1, Number: "13000000001", ObservedAt: observedAt(0)})
	second := report("aabbccddeeff0011", 1, 1, ObservedCall{Sequence: 1, Number: "13000000002", ObservedAt: observedAt(600)})
	reader := &scriptedReader{reports: []CallEventReport{first, second, second}}
	service := newSyncService(t, store, reader, syncSubscriberA)
	if _, err := service.SyncInboundCalls(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err := service.SyncInboundCalls(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.BridgeRestarts != 1 {
		t.Fatalf("result = %+v, want the restart noticed", result)
	}
	if result.Recorded != 1 {
		t.Fatalf("result = %+v, want the new boot's first call recorded", result)
	}
	if len(store.records) != 2 {
		t.Fatalf("records = %d, want both calls kept apart", len(store.records))
	}
}

// TestSyncInboundCallsRereadsFromTheStartAfterARestart guards the one extra read.
// The position has to be sent before the boot identifier is known, so filtering
// with a stale position would silently skip the new boot's first events.
func TestSyncInboundCallsRereadsFromTheStartAfterARestart(t *testing.T) {
	store := newCallStore()
	store.cursors[syncDeviceID] = call.EventCursor{
		BootID: syncBootID, SubscriptionFingerprint: syncSubscriberA, LastSequence: 9,
	}
	restarted := report("aabbccddeeff0011", 1, 2,
		ObservedCall{Sequence: 1, Number: "13000000001", ObservedAt: observedAt(0)},
		ObservedCall{Sequence: 2, Number: "13000000002", ObservedAt: observedAt(60)},
	)
	reader := &scriptedReader{reports: []CallEventReport{restarted, restarted}}
	service := newSyncService(t, store, reader, syncSubscriberA)
	result, err := service.SyncInboundCalls(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.asked) != 2 || reader.asked[0] != 9 || reader.asked[1] != 0 {
		t.Fatalf("cursors asked for = %v, want the stale one then a fresh read", reader.asked)
	}
	if result.Recorded != 2 {
		t.Fatalf("result = %+v, want both of the new boot's calls", result)
	}
	// And a steady state must not pay for that second read.
	steady := &scriptedReader{reports: []CallEventReport{report("aabbccddeeff0011", 1, 2)}}
	service.UseCallEventReader(steady)
	if _, err := service.SyncInboundCalls(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(steady.asked) != 1 {
		t.Fatalf("a steady sweep performed %d reads, want one", len(steady.asked))
	}
}

// TestSyncInboundCallsSkipsEventsFromAPreviousSubscription is the fence that
// replaced verifying identity over AT. Recording these would invent history that
// never happened on this Line.
func TestSyncInboundCallsSkipsEventsFromAPreviousSubscription(t *testing.T) {
	store := newCallStore()
	store.cursors[syncDeviceID] = call.EventCursor{
		BootID: syncBootID, SubscriptionFingerprint: syncSubscriberA, LastSequence: 1,
	}
	reader := &scriptedReader{reports: []CallEventReport{report(syncBootID, 1, 3,
		ObservedCall{Sequence: 2, Number: "13000000001", ObservedAt: observedAt(0)},
		ObservedCall{Sequence: 3, Number: "13000000002", ObservedAt: observedAt(60)},
	)}}
	service := newSyncService(t, store, reader, syncSubscriberB)
	result, err := service.SyncInboundCalls(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.SubscriptionChanges != 1 || result.Recorded != 0 {
		t.Fatalf("result = %+v, want the events skipped", result)
	}
	if len(store.records) != 0 {
		t.Fatal("a call was attributed to a subscription it did not arrive on")
	}
	// Skipped forward, not replayed: the position lands on the newest entry so the
	// next real call is picked up.
	if got := store.cursors[syncDeviceID]; got.LastSequence != 3 || got.SubscriptionFingerprint != syncSubscriberB {
		t.Fatalf("cursor = %+v", got)
	}
}

func TestSyncInboundCallsDerivesLossFromItsOwnPositionAndNeverRecordsIt(t *testing.T) {
	store := newCallStore()
	store.cursors[syncDeviceID] = call.EventCursor{
		BootID: syncBootID, SubscriptionFingerprint: syncSubscriberA, LastSequence: 2,
	}
	// The bridge no longer holds 3 through 5.
	reader := &scriptedReader{reports: []CallEventReport{report(syncBootID, 6, 6,
		ObservedCall{Sequence: 6, Number: "13000000001", ObservedAt: observedAt(0)},
	)}}
	service := newSyncService(t, store, reader, syncSubscriberA)
	result, err := service.SyncInboundCalls(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.LostEvents != 3 {
		t.Fatalf("lost = %d, want three unrecoverable calls", result.LostEvents)
	}
	// Loss is a count and nothing else. A record with no number and no time would
	// be indistinguishable from a real missed call.
	if len(store.records) != 1 {
		t.Fatalf("records = %d, want only the call that was actually read", len(store.records))
	}
}

// TestSyncInboundCallsDerivesNoLossForAConsumedRing is the reason loss is derived
// by the consumer rather than counted by the bridge: a busy line whose events were
// all read must produce nothing.
func TestSyncInboundCallsDerivesNoLossForAConsumedRing(t *testing.T) {
	store := newCallStore()
	store.cursors[syncDeviceID] = call.EventCursor{
		BootID: syncBootID, SubscriptionFingerprint: syncSubscriberA, LastSequence: 40,
	}
	// The ring wrapped long ago and holds 9 through 40, all already consumed.
	reader := &scriptedReader{reports: []CallEventReport{report(syncBootID, 9, 40)}}
	service := newSyncService(t, store, reader, syncSubscriberA)
	result, err := service.SyncInboundCalls(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.LostEvents != 0 {
		t.Fatalf("lost = %d, want none for a fully consumed ring", result.LostEvents)
	}
}

func TestSyncInboundCallsDegradesAnUnusableCallerWithoutBlockingLaterEvents(t *testing.T) {
	store := newCallStore()
	reader := &scriptedReader{reports: []CallEventReport{report(syncBootID, 1, 3,
		ObservedCall{Sequence: 1, Number: "", ObservedAt: observedAt(0)},
		ObservedCall{Sequence: 2, Number: "Unknown", ObservedAt: observedAt(30)},
		ObservedCall{Sequence: 3, Number: "13000000001", ObservedAt: observedAt(60)},
	)}}
	service := newSyncService(t, store, reader, syncSubscriberA)
	result, err := service.SyncInboundCalls(t.Context())
	if err != nil {
		t.Fatalf("an unusable caller failed the sweep: %v", err)
	}
	if result.Degraded != 2 || result.Recorded != 1 {
		t.Fatalf("result = %+v", result)
	}
	// The position moved past the unusable events, so they cannot block every later
	// event behind them forever.
	if got := store.cursors[syncDeviceID].LastSequence; got != 3 {
		t.Fatalf("cursor = %d, want it past the degraded events", got)
	}
}

func TestSyncInboundCallsAppliesEventsInSequenceOrder(t *testing.T) {
	store := newCallStore()
	reader := &scriptedReader{reports: []CallEventReport{report(syncBootID, 1, 3,
		ObservedCall{Sequence: 3, Number: "13000000002", ObservedAt: observedAt(60)},
		ObservedCall{Sequence: 1, Number: "13000000001", ObservedAt: observedAt(0)},
		ObservedCall{Sequence: 2, Number: "15900000000", ObservedAt: observedAt(30)},
	)}}
	service := newSyncService(t, store, reader, syncSubscriberA)
	result, err := service.SyncInboundCalls(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Recorded != 3 {
		t.Fatalf("result = %+v", result)
	}
	if got := store.cursors[syncDeviceID].LastSequence; got != 3 {
		t.Fatalf("cursor = %d, want the highest sequence", got)
	}
}

func TestSyncInboundCallsNeverStoresAnEpochArrivalTime(t *testing.T) {
	store := newCallStore()
	reader := &scriptedReader{reports: []CallEventReport{report(syncBootID, 1, 1,
		ObservedCall{Sequence: 1, Number: "13000000001"},
	)}}
	service := newSyncService(t, store, reader, syncSubscriberA)
	if _, err := service.SyncInboundCalls(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, record := range store.records {
		if record.CreatedAt.Year() < 2000 {
			t.Fatalf("stored arrival time %v, want the receive time substituted", record.CreatedAt)
		}
	}
}

func TestSyncInboundCallsDoesNothingWithoutAReaderOrAResolvableLine(t *testing.T) {
	t.Run("no reader", func(t *testing.T) {
		store := newCallStore()
		service, err := New(t.Context(), store, staticLines{topology: voiceTopology(syncSubscriberA)})
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.SyncInboundCalls(t.Context())
		if err != nil || result.LinesPolled != 0 {
			t.Fatalf("result = %+v err = %v, want a no-op", result, err)
		}
	})
	for name, mutate := range map[string]func(*inventory.Topology){
		"line not ready": func(topology *inventory.Topology) {
			topology.Lines[0].State = inventory.LineUnavailable
		},
		// An unresolvable Line is the ordinary state of one whose device or SIM is
		// absent, so it is skipped rather than reported as a failure.
		"unresolvable device": func(topology *inventory.Topology) {
			topology.Devices[0].EquipmentIdentityFingerprint = ""
		},
		"inactive profile": func(topology *inventory.Topology) {
			topology.SubscriptionProfiles[0].State = "inactive"
		},
	} {
		t.Run(name, func(t *testing.T) {
			topology := voiceTopology(syncSubscriberA)
			mutate(&topology)
			store := newCallStore()
			service, err := New(t.Context(), store, staticLines{topology: topology})
			if err != nil {
				t.Fatal(err)
			}
			service.now = func() time.Time { return time.Unix(1772600000, 0).UTC() }
			reader := &scriptedReader{reports: []CallEventReport{report(syncBootID, 1, 1,
				ObservedCall{Sequence: 1, Number: "13000000001", ObservedAt: observedAt(0)},
			)}}
			service.UseCallEventReader(reader)
			result, err := service.SyncInboundCalls(t.Context())
			if err != nil {
				t.Fatalf("err = %v, want the Line skipped quietly", err)
			}
			if result.LinesPolled != 0 || len(reader.asked) != 0 {
				t.Fatalf("result = %+v, reads = %v", result, reader.asked)
			}
		})
	}
}

func TestSyncInboundCallsReportsAReadFailureWithoutMovingThePosition(t *testing.T) {
	store := newCallStore()
	reader := &scriptedReader{err: errors.New("bridge is unreachable")}
	service := newSyncService(t, store, reader, syncSubscriberA)
	result, err := service.SyncInboundCalls(t.Context())
	if err == nil {
		t.Fatal("an unreachable bridge was reported as success")
	}
	if result.LinesPolled != 0 || len(store.cursors) != 0 {
		t.Fatalf("result = %+v cursors = %v", result, store.cursors)
	}
}

func TestObservedCallIdentityIsStableAndScopedByBoot(t *testing.T) {
	first := observedCallIdentity(syncDeviceID, syncBootID, 1)
	if first != observedCallIdentity(syncDeviceID, syncBootID, 1) {
		t.Fatal("identity is not stable across calls")
	}
	for name, other := range map[string]string{
		"different sequence": observedCallIdentity(syncDeviceID, syncBootID, 2),
		"different boot":     observedCallIdentity(syncDeviceID, "aabbccddeeff0011", 1),
		"different device":   observedCallIdentity("bridge-other", syncBootID, 1),
	} {
		if other == first {
			t.Fatalf("%s produced the same identity", name)
		}
	}
	// The identity is used inside an operation id, which is bounded by the store.
	if len("obs_"+first) > 128 || len("call_"+first) < 16 {
		t.Fatalf("identity length is out of the storable range: %q", first)
	}
}

func TestSyncInboundCallsTreatsAModemWithNoRingAsNothingToRead(t *testing.T) {
	// A locally attached modem is a supported configuration. Reporting it as a
	// failure would log and retry every two seconds for the life of the deployment.
	store := newCallStore()
	reader := &scriptedReader{err: ErrCallEventsUnsupported}
	service := newSyncService(t, store, reader, syncSubscriberA)
	result, err := service.SyncInboundCalls(t.Context())
	if err != nil {
		t.Fatalf("err = %v, want a modem with no ring treated as nothing to read", err)
	}
	if result.LinesPolled != 0 || len(store.cursors) != 0 {
		t.Fatalf("result = %+v cursors = %v", result, store.cursors)
	}
}

// TestSyncInboundCallsPollsALineWithoutTheVoiceCapability is the regression guard
// for a gate that would have made this feature silently dead.
//
// The agent-reported capability mapping hardcodes CellularVoice false, because
// carrying a voice call is unproven on this hardware. Filtering on it would have
// skipped every Line on the hardware backend — no polling, no records, no error,
// nothing in any log. A notification is not a voice capability, and whether a
// device can report observed calls is the device's answer to give.
func TestSyncInboundCallsPollsALineWithoutTheVoiceCapability(t *testing.T) {
	topology := voiceTopology(syncSubscriberA)
	topology.Lines[0].Capabilities.CellularVoice = false
	store := newCallStore()
	service, err := New(t.Context(), store, staticLines{topology: topology})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Unix(1772600000, 0).UTC() }
	reader := &scriptedReader{reports: []CallEventReport{report(syncBootID, 1, 1,
		ObservedCall{Sequence: 1, Number: "13000000001", ObservedAt: observedAt(0)},
	)}}
	service.UseCallEventReader(reader)
	result, err := service.SyncInboundCalls(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.LinesPolled != 1 || result.Recorded != 1 {
		t.Fatalf("result = %+v, want the Line polled and the call recorded", result)
	}
}
