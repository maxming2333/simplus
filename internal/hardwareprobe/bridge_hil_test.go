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
	"github.com/leonfox28/simplus/internal/modemadapter/standardsms"
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
	source, err := NewBridgeDeviceSource(registry, bridgeSpecsFor(config), atremote.Locator)
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

// TestRemoteATBridgeCellularSMSReadPath is opt-in HIL-0 evidence for the
// inbound cellular SMS machinery over a bridged control endpoint. It drives the
// real standardsms driver, which issues the same fixed transcript the production
// adapter uses, and asserts the modem accepted it.
//
// It reads only: PDU mode selection, storage selection, and a storage listing.
// It sends nothing and deletes nothing, so it is safe to run against a
// designated SIM. Outbound submission needs a registered network and is not
// covered here.
func TestRemoteATBridgeCellularSMSReadPath(t *testing.T) {
	configPath := os.Getenv("SIMPLUS_REMOTE_AT_HIL_CONFIG")
	if configPath == "" {
		t.Skip("set SIMPLUS_REMOTE_AT_HIL_CONFIG to run the opt-in remote bridge SMS probe")
	}
	config, err := atremote.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load bridge configuration: %v", err)
	}
	bridgeOpener, err := atremote.NewOpener(config.Targets())
	if err != nil {
		t.Fatalf("build bridge opener: %v", err)
	}
	transport, err := standardsms.NewOpenerTransport(
		atremote.NewRoutingOpener(bridgeOpener, attransport.NewOpener()),
	)
	if err != nil {
		t.Fatalf("build SMS transport: %v", err)
	}
	model := modemadapter.ML307ASMS{}
	driver, err := standardsms.NewDriver(model, transport)
	if err != nil {
		t.Fatalf("build SMS driver: %v", err)
	}
	registry, err := modemadapter.NewRegistry(model)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	source, err := NewBridgeDeviceSource(registry, bridgeSpecsFor(config), atremote.Locator)
	if err != nil {
		t.Fatalf("build bridge device source: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	devices, err := source.Devices(ctx)
	if err != nil {
		t.Fatalf("contributed devices: %v", err)
	}
	for _, device := range devices {
		if device.Profile != agentapi.ProfileML307A {
			continue
		}
		stored, listErr := driver.List(ctx, device)
		if listErr != nil {
			t.Fatalf("bridged SMS storage listing failed for %s: %v", device.ID, listErr)
		}
		t.Logf("bridged SMS storage listing for %s returned %d stored message(s)", device.ID, len(stored))
		for _, message := range stored {
			// Never log PDU content: it carries sender identity and message text.
			t.Logf("  storage index %d status %d tpdu %d bytes", message.Index, message.Status, message.TPDULength)
		}
	}
}

func bridgeSpecsFor(config atremote.Config) []BridgeSpec {
	specs := make([]BridgeSpec, 0, len(config.Bridges))
	for _, bridge := range config.Bridges {
		specs = append(specs, BridgeSpec{
			Key: bridge.Target.Key, Profile: bridge.Profile, AttestCapabilities: bridge.AttestCapabilities,
		})
	}
	return specs
}
