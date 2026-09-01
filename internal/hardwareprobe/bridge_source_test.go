package hardwareprobe

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/leonfox28/simplus/internal/agentapi"
	"github.com/leonfox28/simplus/internal/modemadapter"
)

func testLocator(key string) string { return "test-bridge:" + key }

func bridgeRegistry(t *testing.T) *modemadapter.Registry {
	t.Helper()
	registry, err := modemadapter.NewRegistry(modemadapter.ML307A{})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	return registry
}

func singleBridge(t *testing.T, spec BridgeSpec) agentapi.DeviceReport {
	t.Helper()
	source, err := NewBridgeDeviceSource(bridgeRegistry(t), []BridgeSpec{spec}, testLocator)
	if err != nil {
		t.Fatalf("NewBridgeDeviceSource: %v", err)
	}
	devices, err := source.Devices(context.Background())
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("devices = %d, want 1", len(devices))
	}
	return devices[0]
}

func TestBridgeDeviceIsResolvableByItsModelAdapter(t *testing.T) {
	device := singleBridge(t, BridgeSpec{Key: "esp32-a", Profile: agentapi.ProfileML307A})
	if device.ID != "bridge-esp32-a" {
		t.Fatalf("device ID = %q", device.ID)
	}
	if strings.HasPrefix(device.ID, "usb-") {
		t.Fatal("bridged device must not reuse the USB identity namespace")
	}
	if device.PhysicalPath != "esp32-a" || device.Profile != agentapi.ProfileML307A {
		t.Fatalf("device = %+v", device)
	}
	if device.DisplayName != (modemadapter.ML307A{}).DisplayName() {
		t.Fatalf("display name = %q", device.DisplayName)
	}
	if device.USB.VendorID != "" || device.USB.ProductID != "" || device.USB.SerialPresent {
		t.Fatalf("bridged device fabricated a USB identity: %+v", device.USB)
	}
	if device.Generation != 0 {
		t.Fatalf("generation = %d; the monitor must assign it", device.Generation)
	}
	endpoint, ok := (modemadapter.ML307A{}).Endpoint(device, modemadapter.EndpointPrimaryAT)
	if !ok {
		t.Fatal("model adapter did not resolve the bridged control endpoint")
	}
	if endpoint.Node != testLocator("esp32-a") || endpoint.Kind != agentapi.EndpointTTY {
		t.Fatalf("endpoint = %+v", endpoint)
	}
	scanner := &Scanner{Adapters: bridgeRegistry(t)}
	if resolved := scanner.preferredATEndpoint(device); resolved != testLocator("esp32-a") {
		t.Fatalf("scanner preferred endpoint = %q", resolved)
	}
}

func TestBridgeDeviceCapabilitiesFailClosedWithoutAttestation(t *testing.T) {
	device := singleBridge(t, BridgeSpec{Key: "esp32-a", Profile: agentapi.ProfileML307A})
	adapterCapabilities := (modemadapter.ML307A{}).Capabilities(device)
	if len(device.Capabilities) != len(adapterCapabilities) {
		t.Fatalf("capabilities = %d, adapter declares %d", len(device.Capabilities), len(adapterCapabilities))
	}
	observedFound := false
	for _, capability := range device.Capabilities {
		if capability.Status == agentapi.EvidenceObserved {
			t.Fatalf("capability %q is observed on an unattested bridge", capability.Capability)
		}
		if capability.Status == agentapi.EvidenceUnverified && len(capability.Evidence) == 1 && capability.Evidence[0] == unattestedEvidence {
			observedFound = true
		}
	}
	if !observedFound {
		t.Fatal("no capability carries the unattested-bridge evidence")
	}
	// The fail-closed default is what makes SIM authentication refuse a bridge.
	if observedCapability(device, "sim-auth") || observedCapability(device, "at-control") {
		t.Fatal("unattested bridge advertised an observed control capability")
	}
}

func TestBridgeDeviceCapabilitiesRecordOperatorAttestation(t *testing.T) {
	device := singleBridge(t, BridgeSpec{Key: "esp32-a", Profile: agentapi.ProfileML307A, AttestCapabilities: true})
	reference := (modemadapter.ML307A{}).Capabilities(agentapi.DeviceReport{Interfaces: device.Interfaces})
	statuses := make(map[string]string, len(reference))
	for _, capability := range reference {
		statuses[capability.Capability] = capability.Status
	}
	attestedSeen := 0
	for _, capability := range device.Capabilities {
		expected, ok := statuses[capability.Capability]
		if !ok {
			t.Fatalf("unexpected capability %q", capability.Capability)
		}
		if capability.Status != expected {
			t.Fatalf("capability %q status = %q, want the adapter status %q", capability.Capability, capability.Status, expected)
		}
		if capability.Status != agentapi.EvidenceObserved {
			continue
		}
		attestedSeen++
		if len(capability.Evidence) == 0 || capability.Evidence[len(capability.Evidence)-1] != attestedEvidence {
			t.Fatalf("observed capability %q does not record the attestation: %#v", capability.Capability, capability.Evidence)
		}
	}
	if attestedSeen == 0 {
		t.Fatal("attested bridge published no observed capability")
	}
	if !observedCapability(device, "sim-auth") {
		t.Fatal("attested bridge did not preserve the adapter sim-auth evidence")
	}
}

func TestBridgeDeviceSourceIsDeterministicAndCopiesResults(t *testing.T) {
	specs := []BridgeSpec{
		{Key: "esp32-b", Profile: agentapi.ProfileML307A},
		{Key: "esp32-a", Profile: agentapi.ProfileML307A},
	}
	source, err := NewBridgeDeviceSource(bridgeRegistry(t), specs, testLocator)
	if err != nil {
		t.Fatalf("NewBridgeDeviceSource: %v", err)
	}
	first, err := source.Devices(context.Background())
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	if len(first) != 2 || first[0].ID != "bridge-esp32-a" || first[1].ID != "bridge-esp32-b" {
		t.Fatalf("devices are not sorted by identity: %#v", []string{first[0].ID, first[1].ID})
	}
	second, err := source.Devices(context.Background())
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("repeated scans produced different reports; the monitor would churn generations")
	}
	first[0].Capabilities[0].Status = "mutated"
	first[0].Interfaces[0].Endpoints[0].Node = "mutated"
	third, err := source.Devices(context.Background())
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	if !reflect.DeepEqual(second, third) {
		t.Fatal("caller mutation reached the cached reports")
	}
}

func TestBridgeDeviceSourceHonorsCancellation(t *testing.T) {
	source, err := NewBridgeDeviceSource(bridgeRegistry(t), []BridgeSpec{{Key: "a", Profile: agentapi.ProfileML307A}}, testLocator)
	if err != nil {
		t.Fatalf("NewBridgeDeviceSource: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.Devices(ctx); err == nil {
		t.Fatal("Devices ignored a cancelled context")
	}
}

func TestNewBridgeDeviceSourceRejectsUnusableSpecs(t *testing.T) {
	registry := bridgeRegistry(t)
	for _, testCase := range []struct {
		name     string
		registry *modemadapter.Registry
		specs    []BridgeSpec
		locator  func(string) string
	}{
		{name: "nil registry", specs: []BridgeSpec{{Key: "a", Profile: agentapi.ProfileML307A}}, locator: testLocator},
		{name: "nil locator", registry: registry, specs: []BridgeSpec{{Key: "a", Profile: agentapi.ProfileML307A}}},
		{name: "no specs", registry: registry, locator: testLocator},
		{name: "empty key", registry: registry, specs: []BridgeSpec{{Profile: agentapi.ProfileML307A}}, locator: testLocator},
		{
			name: "duplicate key", registry: registry, locator: testLocator,
			specs: []BridgeSpec{{Key: "a", Profile: agentapi.ProfileML307A}, {Key: "a", Profile: agentapi.ProfileML307A}},
		},
		{name: "unknown profile", registry: registry, specs: []BridgeSpec{{Key: "a", Profile: "unknown"}}, locator: testLocator},
		{name: "empty profile", registry: registry, specs: []BridgeSpec{{Key: "a"}}, locator: testLocator},
		{
			name: "locator returns nothing", registry: registry,
			specs: []BridgeSpec{{Key: "a", Profile: agentapi.ProfileML307A}}, locator: func(string) string { return "" },
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := NewBridgeDeviceSource(testCase.registry, testCase.specs, testCase.locator); err == nil {
				t.Fatal("NewBridgeDeviceSource accepted an unusable specification")
			}
		})
	}
}

// TestNewBridgeDeviceSourceRejectsDriverOwnedTransports keeps a model whose
// driver owns its own transport out of the bridged path. Publishing it would
// produce a device that looks operable and then fails inside a transport this
// seam does not provide.
func TestNewBridgeDeviceSourceRejectsDriverOwnedTransports(t *testing.T) {
	registry, err := modemadapter.NewRegistry(driverOwnedAdapter{}, modemadapter.ML307A{})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	if _, err := NewBridgeDeviceSource(registry, []BridgeSpec{{Key: "a", Profile: "driver-owned"}}, testLocator); err == nil {
		t.Fatal("NewBridgeDeviceSource accepted a driver-owned transport profile")
	}
	if _, err := NewBridgeDeviceSource(registry, []BridgeSpec{{Key: "a", Profile: agentapi.ProfileML307A}}, testLocator); err != nil {
		t.Fatalf("NewBridgeDeviceSource rejected a bridgeable profile: %v", err)
	}
}

// TestNewBridgeDeviceSourceRejectsAdaptersWithoutAControlEndpointRole covers a
// model that declares no primary AT role at all.
func TestNewBridgeDeviceSourceRejectsAdaptersWithoutAControlEndpointRole(t *testing.T) {
	registry, err := modemadapter.NewRegistry(endpointlessAdapter{}, modemadapter.ML307A{})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	if _, err := NewBridgeDeviceSource(registry, []BridgeSpec{{Key: "a", Profile: "endpointless"}}, testLocator); err == nil {
		t.Fatal("NewBridgeDeviceSource accepted a profile with no primary AT role")
	}
}

// TestBridgeInterfaceSearchMatchesAdapterExpectation proves the interface number
// is discovered from the adapter rather than hardcoded in this package.
func TestBridgeInterfaceSearchMatchesAdapterExpectation(t *testing.T) {
	for _, expected := range []int{0, 1, 2, 7, maximumBridgeInterfaceNumber} {
		adapter := fixedInterfaceAdapter{number: expected}
		usbInterface, ok := synthesizeBridgeInterface(adapter, testLocator("a"))
		if !ok {
			t.Fatalf("interface %d was not discovered", expected)
		}
		if usbInterface.Number != expected {
			t.Fatalf("interface number = %d, want %d", usbInterface.Number, expected)
		}
		if len(usbInterface.Endpoints) != 1 || usbInterface.Endpoints[0].InterfaceNumber != expected {
			t.Fatalf("interface = %+v", usbInterface)
		}
	}
	if _, ok := synthesizeBridgeInterface(fixedInterfaceAdapter{number: maximumBridgeInterfaceNumber + 1}, testLocator("a")); ok {
		t.Fatal("interface search exceeded its bound")
	}
}

type fixedInterfaceAdapter struct{ number int }

func (fixedInterfaceAdapter) Profile() string                         { return "fixed" }
func (fixedInterfaceAdapter) DisplayName() string                     { return "Fixed" }
func (fixedInterfaceAdapter) Matches(modemadapter.USBDescriptor) bool { return false }

func (adapter fixedInterfaceAdapter) Endpoint(device agentapi.DeviceReport, role modemadapter.EndpointRole) (agentapi.Endpoint, bool) {
	if role != modemadapter.EndpointPrimaryAT {
		return agentapi.Endpoint{}, false
	}
	for _, usbInterface := range device.Interfaces {
		if usbInterface.Number != adapter.number {
			continue
		}
		for _, candidate := range usbInterface.Endpoints {
			if candidate.Kind == agentapi.EndpointTTY && candidate.Node != "" {
				return candidate, true
			}
		}
	}
	return agentapi.Endpoint{}, false
}

func (fixedInterfaceAdapter) Capabilities(agentapi.DeviceReport) []agentapi.CapabilityEvidence {
	return nil
}

type endpointlessAdapter struct{ fixedInterfaceAdapter }

func (endpointlessAdapter) Profile() string     { return "endpointless" }
func (endpointlessAdapter) DisplayName() string { return "Endpointless" }

func (endpointlessAdapter) Endpoint(agentapi.DeviceReport, modemadapter.EndpointRole) (agentapi.Endpoint, bool) {
	return agentapi.Endpoint{}, false
}

type driverOwnedAdapter struct{ fixedInterfaceAdapter }

func (driverOwnedAdapter) Profile() string     { return "driver-owned" }
func (driverOwnedAdapter) DisplayName() string { return "Driver Owned" }

func (adapter driverOwnedAdapter) Endpoint(device agentapi.DeviceReport, role modemadapter.EndpointRole) (agentapi.Endpoint, bool) {
	return fixedInterfaceAdapter{number: 0}.Endpoint(device, role)
}

func (driverOwnedAdapter) ListSMS(context.Context, modemadapter.SMSRuntimeTarget) ([]agentapi.SMSMessageReference, error) {
	return nil, nil
}

func (driverOwnedAdapter) ReadSMS(context.Context, modemadapter.SMSRuntimeTarget, string) (agentapi.SMSStoredMessage, error) {
	return agentapi.SMSStoredMessage{}, nil
}

func (driverOwnedAdapter) SendSMS(context.Context, modemadapter.SMSRuntimeTarget, agentapi.SMSSendRequest) (agentapi.SMSSubmission, error) {
	return agentapi.SMSSubmission{}, nil
}

func (driverOwnedAdapter) AcknowledgeSMS(context.Context, modemadapter.SMSRuntimeTarget, agentapi.SMSAcknowledgeRequest) (bool, error) {
	return false, nil
}
