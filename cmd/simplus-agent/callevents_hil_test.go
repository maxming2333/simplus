package main

import (
	"os"
	"testing"

	"github.com/leonfox28/simplus/internal/agentapi"
	"github.com/leonfox28/simplus/internal/hardwareprobe"
	"github.com/leonfox28/simplus/internal/modemadapter"
)

// TestCallEventsBackendAgainstRealBridge runs the composed agent operation against
// a real bridge, from the published device report through endpoint resolution to a
// response the protocol layer accepts.
//
// It covers the parts that only real hardware exercises: that the bridge really is
// published as a device, that its control endpoint round-trips through the locator
// back to the configured target, and that what the device actually returns passes
// the protocol's own response validation. Every other hop in this path is a
// translation with deterministic coverage; these are the ones where an assumption
// about the hardware could hide.
//
// Opt-in. Set SIMPLUS_REMOTE_AT_HIL_CONFIG to a private bridge configuration file.
func TestCallEventsBackendAgainstRealBridge(t *testing.T) {
	configPath := os.Getenv("SIMPLUS_REMOTE_AT_HIL_CONFIG")
	if configPath == "" {
		t.Skip("set SIMPLUS_REMOTE_AT_HIL_CONFIG to run the bridge call events check")
	}
	registry, err := modemadapter.NewRegistry(modemadapter.QDC507SMS{}, modemadapter.ML307ASMS{})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	plan, err := planATTransport(configPath)
	if err != nil {
		t.Fatalf("planATTransport: %v", err)
	}
	if len(plan.bridges.Bridges) == 0 {
		t.Fatal("the configuration published no bridge")
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

	// The composed service, not just the backend: its validation is what a real
	// report has to satisfy.
	service := plan.callEventsService(monitor, registry)
	if service == nil {
		t.Fatal("a configured bridge produced no call events service")
	}

	checked := 0
	for _, device := range snapshot.Devices {
		if len(device.Interfaces) == 0 {
			continue
		}
		response, err := service.Read(t.Context(), agentapi.CallEventsRequest{
			AgentInstanceID:  monitor.InstanceID(),
			DeviceID:         device.ID,
			DeviceGeneration: device.Generation,
		})
		if err != nil {
			t.Fatalf("device %s: %v", device.ID, err)
		}
		checked++
		t.Logf("device=%s bootId=%s oldest=%d latest=%d events=%d",
			device.ID, response.BootID, response.OldestSequence, response.LatestSequence, len(response.Events))
		for _, event := range response.Events {
			// An arrival time of zero must never reach a consumer, whether the bridge
			// knew the wall clock or not.
			if event.ObservedAt.IsZero() {
				t.Fatalf("event %d reached the protocol with no arrival time", event.Sequence)
			}
			if event.ObservedAt.Year() < 2000 {
				t.Fatalf("event %d arrival time is %v, want a real time or the receive time",
					event.Sequence, event.ObservedAt)
			}
			// Log the shape, not the caller: this is a diagnostic, and the caller
			// belongs in the call history rather than in test output.
			t.Logf("  event seq=%d observedAt=%s callerLength=%d",
				event.Sequence, event.ObservedAt.Format("2006-01-02T15:04:05Z"), len(event.Number))
		}
		// Reading past the newest entry is the steady state between calls and must be
		// empty rather than an error, since the sweep performs it every two seconds.
		drained, err := service.Read(t.Context(), agentapi.CallEventsRequest{
			AgentInstanceID:  monitor.InstanceID(),
			DeviceID:         device.ID,
			DeviceGeneration: device.Generation,
			After:            response.LatestSequence,
		})
		if err != nil {
			t.Fatalf("device %s drained read: %v", device.ID, err)
		}
		if len(drained.Events) != 0 {
			t.Fatalf("reading past sequence %d returned %d events", response.LatestSequence, len(drained.Events))
		}
		if drained.BootID != response.BootID {
			t.Fatalf("boot identifier changed between reads: %q then %q", response.BootID, drained.BootID)
		}
	}
	if checked == 0 {
		t.Fatal("no bridge device was reachable through the composed operation")
	}
}
