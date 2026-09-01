package hardwareprobe

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/leonfox28/simplus/internal/agentapi"
	"github.com/leonfox28/simplus/internal/atremote"
	"github.com/leonfox28/simplus/internal/attransport"
	"github.com/leonfox28/simplus/internal/modemadapter"
)

// TestRemoteATBridgeReadOnlyProbe is opt-in HIL-0 evidence for the bridged
// control path: it runs the ordinary read-only probe plan against a real bridge
// and prints the typed result. It is skipped unless
// SIMPLUS_REMOTE_AT_HIL_CONFIG points at a private bridge configuration.
//
// It issues no write, no RF change and no SIM mutation. Do not add a
// side-effecting operation here; those need separate authorization under
// .trellis/spec/core/infra/hardware-and-hil-safety.md.
//
// Identities are pseudonymized by the same production path, so the printed
// summary carries fingerprints and a display hint rather than raw ICCID/IMEI.
func TestRemoteATBridgeReadOnlyProbe(t *testing.T) {
	configPath := os.Getenv("SIMPLUS_REMOTE_AT_HIL_CONFIG")
	if configPath == "" {
		t.Skip("set SIMPLUS_REMOTE_AT_HIL_CONFIG to run the opt-in remote bridge probe")
	}
	config, err := atremote.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load bridge configuration: %v", err)
	}
	bridgeOpener, err := atremote.NewOpener(config.Targets())
	if err != nil {
		t.Fatalf("build bridge opener: %v", err)
	}
	registry := modemadapter.DefaultRegistry()
	specs := make([]BridgeSpec, 0, len(config.Bridges))
	for _, bridge := range config.Bridges {
		specs = append(specs, BridgeSpec{
			Key: bridge.Target.Key, Profile: bridge.Profile, AttestCapabilities: bridge.AttestCapabilities,
		})
	}
	source, err := NewBridgeDeviceSource(registry, specs, atremote.Locator)
	if err != nil {
		t.Fatalf("build bridge device source: %v", err)
	}
	scanner := &Scanner{
		USBRoot: t.TempDir(), DevRoot: t.TempDir(), Adapters: registry,
		Identities: deterministicPseudonymizer{},
		Querier: NewATQuerierWithOpener(
			atremote.NewRoutingOpener(bridgeOpener, attransport.NewOpener()),
			deterministicPseudonymizer{},
		),
		ExtraDevices: source.Devices,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	devices, err := scanner.Scan(ctx)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(devices) == 0 {
		t.Fatal("no bridged device was published")
	}
	for index := range devices {
		devices[index].Generation = 1
	}
	probes, err := scanner.Probe(ctx, agentapi.Snapshot{Generation: 1, Devices: devices}, nil)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	for _, probe := range probes {
		rendered, _ := json.MarshalIndent(probe, "", "  ")
		t.Logf("bridged probe result:\n%s", rendered)
		if probe.State == agentapi.ProbeStateUnavailable || probe.State == agentapi.ProbeStateFailed {
			t.Fatalf("bridged probe failed: state=%q code=%q detail=%q",
				probe.State, probe.ErrorCode, probe.ErrorDetail)
		}
	}
}
