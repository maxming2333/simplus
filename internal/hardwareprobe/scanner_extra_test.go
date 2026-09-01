package hardwareprobe

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/leonfox28/simplus/internal/agentapi"
	"github.com/leonfox28/simplus/internal/attransport"
	"github.com/leonfox28/simplus/internal/modemadapter"
)

func usbOnlyScanner(t *testing.T) *Scanner {
	t.Helper()
	root := t.TempDir()
	usbRoot := filepath.Join(root, "sys", "bus", "usb", "devices")
	devRoot := filepath.Join(root, "dev")
	mustMkdir(t, usbRoot)
	mustMkdir(t, devRoot)
	writeUSBDevice(t, usbRoot, "1-3", map[string]string{
		"idVendor": "2ecc", "idProduct": "3012", "manufacturer": "CMIOT", "product": "ML307A",
		"serial": "ML307A-SERIAL-0001", "bcdDevice": "0100", "bConfigurationValue": "1",
	})
	writeInterface(t, usbRoot, "1-3:1.2", 2, "ff", "00", "00", "option", "ttyUSB6")
	return &Scanner{
		USBRoot: usbRoot, DevRoot: devRoot, Querier: &fakeQuerier{},
		Identities: deterministicPseudonymizer{}, Adapters: modemadapter.DefaultRegistry(),
	}
}

// TestScanWithoutExtraDevicesIsUnchanged is the regression guard for the
// disabled-by-default requirement: a nil hook must not alter discovery.
func TestScanWithoutExtraDevicesIsUnchanged(t *testing.T) {
	scanner := usbOnlyScanner(t)
	baseline, err := scanner.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(baseline) != 1 || baseline[0].ID != "usb-1-3" {
		t.Fatalf("baseline devices = %#v", baseline)
	}
	scanner.ExtraDevices = nil
	again, err := scanner.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !reflect.DeepEqual(baseline, again) {
		t.Fatal("nil extra-device hook changed discovery output")
	}
}

func TestScanMergesContributedDevicesSortedByIdentity(t *testing.T) {
	scanner := usbOnlyScanner(t)
	source, err := NewBridgeDeviceSource(
		modemadapter.DefaultRegistry(),
		[]BridgeSpec{{Key: "esp32-a", Profile: agentapi.ProfileML307A}},
		testLocator,
	)
	if err != nil {
		t.Fatalf("NewBridgeDeviceSource: %v", err)
	}
	scanner.ExtraDevices = source.Devices
	devices, err := scanner.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(devices) != 2 {
		t.Fatalf("devices = %#v", devices)
	}
	if devices[0].ID != "bridge-esp32-a" || devices[1].ID != "usb-1-3" {
		t.Fatalf("devices are not sorted by identity: %q, %q", devices[0].ID, devices[1].ID)
	}
	// The discovered USB device must be unaffected by the contributed one.
	if !hasCapability(devices[1], "at-control", agentapi.EvidenceObserved) {
		t.Fatalf("discovered device capabilities changed: %#v", devices[1].Capabilities)
	}
	if hasCapability(devices[0], "at-control", agentapi.EvidenceObserved) {
		t.Fatalf("contributed device advertised observed control: %#v", devices[0].Capabilities)
	}
}

func TestScanRejectsContributedDeviceProblems(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		extra func(context.Context) ([]agentapi.DeviceReport, error)
	}{
		{
			name:  "source failure",
			extra: func(context.Context) ([]agentapi.DeviceReport, error) { return nil, errors.New("bridge source failed") },
		},
		{
			name: "identity collision",
			extra: func(context.Context) ([]agentapi.DeviceReport, error) {
				return []agentapi.DeviceReport{{ID: "usb-1-3", Profile: agentapi.ProfileML307A}}, nil
			},
		},
		{
			name: "duplicate contribution",
			extra: func(context.Context) ([]agentapi.DeviceReport, error) {
				return []agentapi.DeviceReport{{ID: "bridge-a"}, {ID: "bridge-a"}}, nil
			},
		},
		{
			name: "missing identity",
			extra: func(context.Context) ([]agentapi.DeviceReport, error) {
				return []agentapi.DeviceReport{{Profile: agentapi.ProfileML307A}}, nil
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			scanner := usbOnlyScanner(t)
			scanner.ExtraDevices = testCase.extra
			if _, err := scanner.Scan(context.Background()); err == nil {
				t.Fatal("Scan accepted an invalid contributed device set")
			}
		})
	}
}

// TestUnattestedBridgeRefusesSIMAuthentication proves the fail-closed evidence
// policy reaches the SIM authentication gate rather than only the report.
func TestUnattestedBridgeRefusesSIMAuthentication(t *testing.T) {
	registry := modemadapter.DefaultRegistry()
	for _, testCase := range []struct {
		name     string
		attested bool
		expected error
	}{
		{name: "unattested", expected: agentapi.ErrSIMAKAUnsupported},
		{name: "attested", attested: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			source, err := NewBridgeDeviceSource(registry,
				[]BridgeSpec{{Key: "esp32-a", Profile: agentapi.ProfileML307A, AttestCapabilities: testCase.attested}},
				testLocator)
			if err != nil {
				t.Fatalf("NewBridgeDeviceSource: %v", err)
			}
			devices, err := source.Devices(context.Background())
			if err != nil {
				t.Fatalf("Devices: %v", err)
			}
			devices[0].Generation = 4
			snapshot := agentapi.Snapshot{Generation: 4, Devices: devices}
			scanner := &Scanner{
				USBRoot: "/unused", DevRoot: "/dev", Adapters: registry,
				Querier: NewATQuerierWithOpener(refusingOpener{}, deterministicPseudonymizer{}),
			}
			_, err = scanner.AuthenticateSIMAKA(context.Background(), snapshot, "bridge-esp32-a", "", agentapi.SIMAKAChallenge{})
			if testCase.expected != nil {
				if !errors.Is(err, testCase.expected) {
					t.Fatalf("AuthenticateSIMAKA error = %v, want %v", err, testCase.expected)
				}
				return
			}
			// With an attestation the capability gate passes, so the failure must
			// come from the transport instead of the capability policy.
			if errors.Is(err, agentapi.ErrSIMAKAUnsupported) {
				t.Fatalf("attested bridge was refused by the capability gate: %v", err)
			}
			if err == nil {
				t.Fatal("AuthenticateSIMAKA succeeded against a refusing transport")
			}
		})
	}
}

type refusingOpener struct{}

func (refusingOpener) Open(string) (attransport.Session, error) {
	return nil, attransport.NewOpenError(attransport.OpenUnavailable, true, errors.New("bridge unreachable"))
}
