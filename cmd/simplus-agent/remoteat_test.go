package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/leonfox28/simplus/internal/agentapi"
	"github.com/leonfox28/simplus/internal/hardwareprobe"
	"github.com/leonfox28/simplus/internal/modemadapter"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func writeBridgeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "remote-at.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write bridge configuration: %v", err)
	}
	// Chmod explicitly: run() sets a restrictive process umask, so the WriteFile
	// permission argument alone does not pin the resulting mode.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("set bridge configuration mode: %v", err)
	}
	return path
}

// TestPlanATTransportWithoutConfigurationKeepsTheLocalPath is the regression
// guard for the disabled-by-default requirement.
func TestPlanATTransportWithoutConfigurationKeepsTheLocalPath(t *testing.T) {
	plan, err := planATTransport("")
	if err != nil {
		t.Fatalf("planATTransport: %v", err)
	}
	if plan.opener == nil {
		t.Fatal("plan has no AT transport")
	}
	if len(plan.bridges.Bridges) != 0 {
		t.Fatalf("plan published bridges without configuration: %+v", plan.bridges)
	}
	scanner := hardwareprobe.NewScanner()
	if err := plan.attachBridgeDevices(scanner, modemadapter.DefaultRegistry(), discardLogger()); err != nil {
		t.Fatalf("attachBridgeDevices: %v", err)
	}
	if scanner.ExtraDevices != nil {
		t.Fatal("an unconfigured plan registered a contributed device source")
	}
}

func TestConfiguredBridgePublishesADeviceForItsModel(t *testing.T) {
	path := writeBridgeConfig(t, `{"bridges":[{"key":"esp32-a","baseUrl":"http://bridge.invalid","profile":"ml307a","username":"agent","password":"secret"}]}`)
	plan, err := planATTransport(path)
	if err != nil {
		t.Fatalf("planATTransport: %v", err)
	}
	if plan.opener == nil || len(plan.bridges.Bridges) != 1 {
		t.Fatalf("plan = %+v", plan.bridges)
	}
	scanner := hardwareprobe.NewScanner()
	registry := modemadapter.DefaultRegistry()
	scanner.Adapters = registry
	if err := plan.attachBridgeDevices(scanner, registry, discardLogger()); err != nil {
		t.Fatalf("attachBridgeDevices: %v", err)
	}
	if scanner.ExtraDevices == nil {
		t.Fatal("configured bridge did not register a contributed device source")
	}
	devices, err := scanner.ExtraDevices(context.Background())
	if err != nil {
		t.Fatalf("contributed devices: %v", err)
	}
	if len(devices) != 1 || devices[0].ID != "bridge-esp32-a" || devices[0].Profile != agentapi.ProfileML307A {
		t.Fatalf("contributed devices = %#v", devices)
	}
	endpoint, ok := (modemadapter.ML307A{}).Endpoint(devices[0], modemadapter.EndpointPrimaryAT)
	if !ok || endpoint.Node == "" {
		t.Fatalf("contributed device control endpoint = %+v", endpoint)
	}
	for _, capability := range devices[0].Capabilities {
		if capability.Status == agentapi.EvidenceObserved {
			t.Fatalf("unattested bridge published observed capability %q", capability.Capability)
		}
	}
}

// TestBridgedModelMustNotRequireALocalControlEndpoint proves the production
// registry composition stays publishable on a bridged path for the model that
// runs over the shared AT seam, and refuses the one that owns a tty-only driver.
func TestBridgedModelMustNotRequireALocalControlEndpoint(t *testing.T) {
	if (modemadapter.ML307ASMS{}).Profile() != agentapi.ProfileML307A {
		t.Fatal("ML307A SMS composition changed its profile")
	}
	if _, ok := any(modemadapter.ML307ASMS{}).(modemadapter.LocalTTYAdapter); ok {
		t.Fatal("ML307A SMS composition must not require a local control endpoint")
	}
	local, ok := any(modemadapter.QDC507SMS{}).(modemadapter.LocalTTYAdapter)
	if !ok || !local.RequiresLocalTTY() {
		t.Fatal("QDC507 SMS composition must declare that it requires a local control endpoint")
	}
}

func TestPlanATTransportFailsClosed(t *testing.T) {
	for _, testCase := range []struct{ name, body string }{
		{name: "invalid url", body: `{"bridges":[{"key":"a","baseUrl":"mqtt://bridge.invalid","profile":"ml307a"}]}`},
		{name: "no bridges", body: `{"bridges":[]}`},
		{name: "duplicate key", body: `{"bridges":[{"key":"a","baseUrl":"http://bridge.invalid","profile":"ml307a"},{"key":"a","baseUrl":"http://other.invalid","profile":"ml307a"}]}`},
		{name: "credentials half configured", body: `{"bridges":[{"key":"a","baseUrl":"http://bridge.invalid","profile":"ml307a","username":"agent"}]}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := planATTransport(writeBridgeConfig(t, testCase.body)); err == nil {
				t.Fatal("planATTransport accepted an unusable configuration")
			}
		})
	}
}

func TestAttachBridgeDevicesFailsClosedForUnusableProfiles(t *testing.T) {
	// Use the production-shaped registry: the safe DefaultRegistry holds
	// discovery-only adapters, which carry no driver-transport declaration.
	registry, registryErr := modemadapter.NewRegistry(modemadapter.QDC507SMS{}, modemadapter.ML307ASMS{})
	if registryErr != nil {
		t.Fatalf("build registry: %v", registryErr)
	}
	for _, testCase := range []struct{ name, body string }{
		{name: "unknown profile", body: `{"bridges":[{"key":"a","baseUrl":"http://bridge.invalid","profile":"unknown"}]}`},
		{name: "local-only model", body: `{"bridges":[{"key":"a","baseUrl":"http://bridge.invalid","profile":"qdc507"}]}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			plan, err := planATTransport(writeBridgeConfig(t, testCase.body))
			if err != nil {
				t.Fatalf("planATTransport: %v", err)
			}
			scanner := hardwareprobe.NewScanner()
			scanner.Adapters = registry
			if err := plan.attachBridgeDevices(scanner, registry, discardLogger()); err == nil {
				t.Fatal("attachBridgeDevices accepted an unusable profile")
			}
			if scanner.ExtraDevices != nil {
				t.Fatal("failed assembly left a contributed device source behind")
			}
		})
	}
}

func TestPlanATTransportRejectsUnsafeConfigurationFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote-at.json")
	body := `{"bridges":[{"key":"a","baseUrl":"http://bridge.invalid","profile":"ml307a"}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write bridge configuration: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("set bridge configuration mode: %v", err)
	}
	if _, err := planATTransport(path); err == nil {
		t.Fatal("planATTransport accepted a world-readable configuration")
	}
	if _, err := planATTransport(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("planATTransport accepted a missing configuration")
	}
}
