package hardwareprobe

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/leonfox28/simplus/internal/agentapi"
	"github.com/leonfox28/simplus/internal/modemadapter"
)

// bridgeDeviceIDPrefix keeps bridged device identities in their own stable
// namespace, separate from stableDeviceID's "usb-" USB topology namespace.
const bridgeDeviceIDPrefix = "bridge-"

// maximumBridgeInterfaceNumber bounds the search for the interface number a
// model adapter expects its primary control endpoint on. The number itself is a
// model fact owned by internal/modemadapter; this package discovers it by asking
// the adapter instead of hardcoding it.
const maximumBridgeInterfaceNumber = 15

const (
	unattestedEvidence = "control endpoint is a remote bridge without bounded HIL evidence"
	attestedEvidence   = "operator-attested remote bridge; not Simplus bounded HIL evidence"
)

// BridgeSpec is one reviewed remote control bridge to publish as a device.
type BridgeSpec struct {
	// Key is the bridge identity shared with the transport target table.
	Key string
	// Profile selects the model adapter through Registry.ForProfile. The USB
	// descriptor match path is not used, so no VID/PID is fabricated.
	Profile string
	// AttestCapabilities preserves the adapter's capability evidence for this
	// bridge. See BridgeDeviceSource for why the default is fail-closed.
	AttestCapabilities bool
}

// BridgeDeviceSource publishes reviewed remote control bridges as ordinary
// device reports so the existing probe, capability and SIM authentication
// orchestration applies unchanged.
//
// Every report is built once at construction and returned by copy. Synthesis is
// therefore deterministic and content-stable, which matters because
// agentapi.Monitor derives snapshot revisions and per-device generations from
// report content: a source that varied between scans would churn generations and
// invalidate queued operations.
type BridgeDeviceSource struct {
	devices []agentapi.DeviceReport
}

// NewBridgeDeviceSource validates every spec and pre-builds its device report.
//
// locator maps a bridge key to the opaque control-endpoint locator understood by
// the bridge transport. It is injected so the transport's locator scheme stays
// inside the transport package and this package never learns the wire shape.
func NewBridgeDeviceSource(registry *modemadapter.Registry, specs []BridgeSpec, locator func(string) string) (*BridgeDeviceSource, error) {
	if registry == nil {
		return nil, errors.New("bridge device source requires a modem adapter registry")
	}
	if locator == nil {
		return nil, errors.New("bridge device source requires a control endpoint locator")
	}
	if len(specs) == 0 {
		return nil, errors.New("bridge device source requires at least one bridge")
	}
	source := &BridgeDeviceSource{devices: make([]agentapi.DeviceReport, 0, len(specs))}
	seen := make(map[string]bool, len(specs))
	for _, spec := range specs {
		if spec.Key == "" {
			return nil, errors.New("bridge key must not be empty")
		}
		if seen[spec.Key] {
			return nil, fmt.Errorf("duplicate bridge key %q", spec.Key)
		}
		seen[spec.Key] = true
		device, err := buildBridgeDevice(registry, spec, locator(spec.Key))
		if err != nil {
			return nil, err
		}
		source.devices = append(source.devices, device)
	}
	sort.Slice(source.devices, func(left, right int) bool {
		return source.devices[left].ID < source.devices[right].ID
	})
	return source, nil
}

// Devices satisfies the scanner's extra-device hook. It performs no I/O: a
// bridge's reachability is proven by probing, not by discovery, so an
// unreachable bridge stays visible with a typed probe error instead of silently
// disappearing from inventory.
func (source *BridgeDeviceSource) Devices(ctx context.Context) ([]agentapi.DeviceReport, error) {
	if source == nil {
		return nil, errors.New("bridge device source is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	copied := make([]agentapi.DeviceReport, 0, len(source.devices))
	for _, device := range source.devices {
		copied = append(copied, cloneDeviceReport(device))
	}
	return copied, nil
}

func buildBridgeDevice(registry *modemadapter.Registry, spec BridgeSpec, node string) (agentapi.DeviceReport, error) {
	if node == "" {
		return agentapi.DeviceReport{}, fmt.Errorf("bridge %q has no control endpoint locator", spec.Key)
	}
	adapter, ok := registry.ForProfile(spec.Profile)
	if !ok {
		return agentapi.DeviceReport{}, fmt.Errorf("bridge %q profile %q has no registered model adapter", spec.Key, spec.Profile)
	}
	if _, ok := adapter.(modemadapter.SMSAdapter); ok {
		// An SMS-capable adapter owns its own dedicated transport, which this
		// seam does not provide. Refuse to synthesize a device that would look
		// operable for SMS and then fail inside a driver-owned transport.
		return agentapi.DeviceReport{}, fmt.Errorf("bridge %q profile %q owns a dedicated driver transport and cannot be bridged", spec.Key, spec.Profile)
	}
	usbInterface, ok := synthesizeBridgeInterface(adapter, node)
	if !ok {
		return agentapi.DeviceReport{}, fmt.Errorf("bridge %q profile %q does not accept a bridged control endpoint", spec.Key, spec.Profile)
	}
	device := agentapi.DeviceReport{
		ID:           bridgeDeviceIDPrefix + spec.Key,
		PhysicalPath: spec.Key,
		Profile:      adapter.Profile(),
		DisplayName:  adapter.DisplayName(),
		// No USB descriptor exists for a bridged control endpoint. Reporting a
		// zero identity is honest; fabricating a VID/PID would make the report
		// indistinguishable from attached hardware.
		USB:        agentapi.USBIdentity{InterfaceCount: 1},
		Interfaces: []agentapi.USBInterface{usbInterface},
	}
	device.Capabilities = bridgeCapabilities(adapter.Capabilities(device), spec.AttestCapabilities)
	return device, nil
}

// synthesizeBridgeInterface finds the interface number the model adapter expects
// its primary control endpoint on by offering candidates until the adapter
// resolves the bridged node itself.
func synthesizeBridgeInterface(adapter modemadapter.Adapter, node string) (agentapi.USBInterface, bool) {
	for number := 0; number <= maximumBridgeInterfaceNumber; number++ {
		candidate := agentapi.USBInterface{
			Number: number,
			Endpoints: []agentapi.Endpoint{
				{Kind: agentapi.EndpointTTY, InterfaceNumber: number, Node: node},
			},
		}
		probe := agentapi.DeviceReport{Interfaces: []agentapi.USBInterface{candidate}}
		resolved, ok := adapter.Endpoint(probe, modemadapter.EndpointPrimaryAT)
		if ok && resolved.Node == node {
			return candidate, true
		}
	}
	return agentapi.USBInterface{}, false
}

// bridgeCapabilities applies the evidence policy for a bridged control path.
//
// A model adapter's observed statuses are backed by bounded HIL on a locally
// attached module. That evidence does not transfer to a third-party bridge, so
// by default every observed status is downgraded to unverified. Because
// internal/application/inventory/agent_source.go maps only observed evidence to
// business capabilities, the default is fail-closed: the device is listed, it
// can be probed, and no modem function, Line or SIM authentication becomes
// available.
//
// An operator may attest a specific bridge. That preserves the adapter's
// statuses and records the attestation in the evidence text so the distinction
// between attested and observed stays visible wherever evidence is rendered.
func bridgeCapabilities(capabilities []agentapi.CapabilityEvidence, attested bool) []agentapi.CapabilityEvidence {
	result := make([]agentapi.CapabilityEvidence, 0, len(capabilities))
	for _, capability := range capabilities {
		if capability.Status != agentapi.EvidenceObserved {
			result = append(result, agentapi.CapabilityEvidence{
				Capability: capability.Capability, Status: capability.Status,
				Evidence: append([]string(nil), capability.Evidence...),
			})
			continue
		}
		if attested {
			result = append(result, agentapi.CapabilityEvidence{
				Capability: capability.Capability, Status: agentapi.EvidenceObserved,
				Evidence: append(append([]string(nil), capability.Evidence...), attestedEvidence),
			})
			continue
		}
		result = append(result, agentapi.CapabilityEvidence{
			Capability: capability.Capability, Status: agentapi.EvidenceUnverified,
			Evidence: []string{unattestedEvidence},
		})
	}
	return result
}

func cloneDeviceReport(device agentapi.DeviceReport) agentapi.DeviceReport {
	clone := device
	clone.Interfaces = make([]agentapi.USBInterface, 0, len(device.Interfaces))
	for _, usbInterface := range device.Interfaces {
		copied := usbInterface
		copied.Endpoints = append([]agentapi.Endpoint(nil), usbInterface.Endpoints...)
		clone.Interfaces = append(clone.Interfaces, copied)
	}
	clone.Capabilities = make([]agentapi.CapabilityEvidence, 0, len(device.Capabilities))
	for _, capability := range device.Capabilities {
		copied := capability
		copied.Evidence = append([]string(nil), capability.Evidence...)
		clone.Capabilities = append(clone.Capabilities, copied)
	}
	return clone
}
