package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leonfox28/simplus/internal/agentapi"
	"github.com/leonfox28/simplus/internal/atremote"
	"github.com/leonfox28/simplus/internal/hardwareprobe"
	"github.com/leonfox28/simplus/internal/modemadapter"
)

const (
	bridgeDeviceID = "bridge-esp32-a"
	localDeviceID  = "usb-1-3"
)

// bridgeMonitor builds the production shape: a configured bridge published as an
// ordinary device, discovered through the same contributed source the agent uses.
func bridgeMonitor(t *testing.T) (*agentapi.Monitor, uint64, *modemadapter.Registry) {
	t.Helper()
	registry, err := modemadapter.NewRegistry(modemadapter.QDC507SMS{}, modemadapter.ML307ASMS{})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	plan, err := planATTransport(writeBridgeConfig(t,
		`{"bridges":[{"key":"esp32-a","baseUrl":"http://bridge.invalid","profile":"ml307a"}]}`))
	if err != nil {
		t.Fatalf("planATTransport: %v", err)
	}
	scanner := hardwareprobe.NewScanner()
	scanner.Adapters = registry
	scanner.USBRoot, scanner.DevRoot = t.TempDir(), t.TempDir()
	if err := plan.attachBridgeDevices(scanner, registry, discardLogger()); err != nil {
		t.Fatalf("attachBridgeDevices: %v", err)
	}
	monitor := agentapi.NewMonitor(scanner)
	scanner.CurrentSnapshot = monitor.Snapshot
	snapshot, err := monitor.Refresh(t.Context())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	for _, device := range snapshot.Devices {
		if device.ID == bridgeDeviceID {
			return monitor, device.Generation, registry
		}
	}
	t.Fatalf("the bridge was not published as a device: %#v", snapshot.Devices)
	return nil, 0, nil
}

// localMonitor builds a device reached over a local tty rather than a bridge.
func localMonitor(t *testing.T) (*agentapi.Monitor, uint64, *modemadapter.Registry) {
	t.Helper()
	registry, err := modemadapter.NewRegistry(modemadapter.QDC507SMS{}, modemadapter.ML307ASMS{})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	scanner := hardwareprobe.NewScanner()
	scanner.Adapters = registry
	scanner.USBRoot, scanner.DevRoot = t.TempDir(), t.TempDir()
	scanner.ExtraDevices = func(context.Context) ([]agentapi.DeviceReport, error) {
		return []agentapi.DeviceReport{{
			ID: localDeviceID, DisplayName: "ML307A", PhysicalPath: "1-3", Profile: agentapi.ProfileML307A,
			USB: agentapi.USBIdentity{InterfaceCount: 1},
			Interfaces: []agentapi.USBInterface{{
				Number:    2,
				Endpoints: []agentapi.Endpoint{{Kind: agentapi.EndpointTTY, InterfaceNumber: 2, Node: "/dev/ttyUSB2"}},
			}},
		}}, nil
	}
	monitor := agentapi.NewMonitor(scanner)
	scanner.CurrentSnapshot = monitor.Snapshot
	snapshot, err := monitor.Refresh(t.Context())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return monitor, snapshot.Devices[0].Generation, registry
}

type stubBridgeCallReader struct {
	report      atremote.CallEventsSnapshot
	err         error
	seenAfter   uint32
	seenAddress string
	calls       int
}

func (reader *stubBridgeCallReader) CallEvents(_ context.Context, endpoint string, after uint32) (atremote.CallEventsSnapshot, error) {
	reader.calls++
	reader.seenAddress = endpoint
	reader.seenAfter = after
	return reader.report, reader.err
}

func bridgeReport() atremote.CallEventsSnapshot {
	return atremote.CallEventsSnapshot{
		BootID: "0f3a1c2b4d5e6f70", LatestSequence: 7, OldestSequence: 6,
		Events: []atremote.CallEvent{
			{Sequence: 6, Number: "13000000001", ObservedAt: 1772505600, ObservedMs: 900001},
			// An unsynchronized bridge clock.
			{Sequence: 7, Number: "", ObservedAt: 0, ObservedMs: 913001},
		},
	}
}

func TestCallEventsBackendResolvesTheBridgeAndCarriesTheCursor(t *testing.T) {
	monitor, generation, adapters := bridgeMonitor(t)
	reader := &stubBridgeCallReader{report: bridgeReport()}
	backend := newCallEventsBackend(monitor, adapters, reader)
	backend.now = func() time.Time { return time.Unix(1772600000, 0).UTC() }

	response, err := backend.CallEvents(context.Background(), agentapi.CallEventsRequest{
		AgentInstanceID: monitor.InstanceID(), DeviceID: bridgeDeviceID, After: 5, DeviceGeneration: generation,
	})
	if err != nil {
		t.Fatalf("CallEvents: %v", err)
	}
	if reader.seenAfter != 5 {
		t.Fatalf("cursor reached the bridge as %d, want 5", reader.seenAfter)
	}
	if !atremote.IsLocator(reader.seenAddress) {
		t.Fatalf("resolved endpoint %q is not a bridge locator", reader.seenAddress)
	}
	if response.BootID != "0f3a1c2b4d5e6f70" || len(response.Events) != 2 {
		t.Fatalf("response = %+v", response)
	}
	if got := response.Events[0].ObservedAt.Unix(); got != 1772505600 {
		t.Fatalf("known arrival time became %d", got)
	}
	// An unknown arrival time becomes the local receive time, never 1970.
	if got := response.Events[1].ObservedAt.Unix(); got != 1772600000 {
		t.Fatalf("unsynchronized arrival time became %d, want the receive time", got)
	}
	if response.Events[1].ObservedAt.IsZero() {
		t.Fatal("an unknown arrival time was left zero for the consumer to interpret")
	}
}

// TestCallEventsBackendSubstitutesOneReceiveTimeForTheWholePage guards ordering:
// sampling the clock per event would order them by when they were converted
// rather than by when they arrived.
func TestCallEventsBackendSubstitutesOneReceiveTimeForTheWholePage(t *testing.T) {
	monitor, generation, adapters := bridgeMonitor(t)
	report := atremote.CallEventsSnapshot{BootID: "0f3a1c2b4d5e6f70", LatestSequence: 3, OldestSequence: 1}
	for sequence := uint32(1); sequence <= 3; sequence++ {
		report.Events = append(report.Events, atremote.CallEvent{Sequence: sequence, Number: "1", ObservedAt: 0})
	}
	backend := newCallEventsBackend(monitor, adapters, &stubBridgeCallReader{report: report})
	ticks := 0
	backend.now = func() time.Time {
		ticks++
		return time.Unix(int64(1772600000+ticks), 0).UTC()
	}
	response, err := backend.CallEvents(context.Background(), agentapi.CallEventsRequest{
		AgentInstanceID: monitor.InstanceID(), DeviceID: bridgeDeviceID, DeviceGeneration: generation,
	})
	if err != nil {
		t.Fatalf("CallEvents: %v", err)
	}
	if ticks != 1 {
		t.Fatalf("the clock was sampled %d times for one page, want once", ticks)
	}
	for index, event := range response.Events {
		if !event.ObservedAt.Equal(response.Events[0].ObservedAt) {
			t.Fatalf("event %d got a different substitute time", index)
		}
	}
}

func TestCallEventsBackendRefusesDevicesWithoutAnEventRing(t *testing.T) {
	monitor, generation, adapters := bridgeMonitor(t)
	reader := &stubBridgeCallReader{report: bridgeReport()}
	backend := newCallEventsBackend(monitor, adapters, reader)

	t.Run("unknown device", func(t *testing.T) {
		_, err := backend.CallEvents(context.Background(), agentapi.CallEventsRequest{
			AgentInstanceID: monitor.InstanceID(), DeviceID: "bridge-absent", DeviceGeneration: generation,
		})
		if !errors.Is(err, agentapi.ErrCallEventsDeviceNotFound) {
			t.Fatalf("err = %v, want the device reported absent", err)
		}
		if reader.calls != 0 {
			t.Fatal("the bridge was read for a device that is not present")
		}
	})

	t.Run("locally attached modem", func(t *testing.T) {
		// Nothing records a local modem's caller-line notifications, so they are
		// gone before anyone could poll. Saying unsupported is better than an empty
		// page, which a consumer cannot distinguish from "no calls".
		local, localGeneration, localAdapters := localMonitor(t)
		localBackend := newCallEventsBackend(local, localAdapters, reader)
		_, err := localBackend.CallEvents(context.Background(), agentapi.CallEventsRequest{
			AgentInstanceID: local.InstanceID(), DeviceID: localDeviceID, DeviceGeneration: localGeneration,
		})
		if !errors.Is(err, agentapi.ErrCallEventsUnsupported) {
			t.Fatalf("err = %v, want unsupported for a device with no event ring", err)
		}
	})
}

func TestCallEventsBackendCollapsesTransportErrorsIntoItsOwnVocabulary(t *testing.T) {
	monitor, generation, adapters := bridgeMonitor(t)
	for name, tc := range map[string]struct {
		err  error
		want error
	}{
		"unreviewed bridge": {atremote.ErrCallEventsUnavailable, agentapi.ErrCallEventsUnsupported},
		"read failed":       {atremote.ErrCallEventsFailed, agentapi.ErrCallEventsUnavailable},
		"bad report":        {atremote.ErrCallEventsInvalid, agentapi.ErrCallEventsUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			backend := newCallEventsBackend(monitor, adapters, &stubBridgeCallReader{err: tc.err})
			_, err := backend.CallEvents(context.Background(), agentapi.CallEventsRequest{
				AgentInstanceID: monitor.InstanceID(), DeviceID: bridgeDeviceID, DeviceGeneration: generation,
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			// The transport's errors are opaque about the bridge on purpose; that must
			// not be undone by forwarding them.
			if err != nil && errors.Is(err, tc.err) && tc.err != tc.want {
				t.Fatal("a transport error was forwarded rather than collapsed")
			}
		})
	}
}

func TestNewCallEventsBackendIsNilWithoutItsDependencies(t *testing.T) {
	monitor, _, adapters := bridgeMonitor(t)
	reader := &stubBridgeCallReader{}
	if newCallEventsBackend(nil, adapters, reader) != nil ||
		newCallEventsBackend(monitor, nil, reader) != nil ||
		newCallEventsBackend(monitor, adapters, nil) != nil {
		t.Fatal("a backend was built without all of its dependencies")
	}
	var absent *callEventsBackend
	if _, err := absent.CallEvents(context.Background(), agentapi.CallEventsRequest{}); !errors.Is(err, agentapi.ErrCallEventsUnavailable) {
		t.Fatalf("err = %v, want unavailable", err)
	}
}

// TestCallEventsServiceIsAbsentWithoutABridge holds the composition rule: a
// deployment with no event ring must expose no route rather than a failing one.
func TestCallEventsServiceIsAbsentWithoutABridge(t *testing.T) {
	monitor, _, adapters := bridgeMonitor(t)
	plan, err := planATTransport("")
	if err != nil {
		t.Fatalf("planATTransport: %v", err)
	}
	if plan.callEventsService(monitor, adapters) != nil {
		t.Fatal("a call events service was composed without a configured bridge")
	}
}
