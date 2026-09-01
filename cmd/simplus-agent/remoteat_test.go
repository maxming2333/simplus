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

func TestAttachRemoteATBridgesPublishesConfiguredBridges(t *testing.T) {
	registry := modemadapter.DefaultRegistry()
	scanner := hardwareprobe.NewScanner()
	scanner.Adapters = registry
	baseline := scanner.Querier

	path := writeBridgeConfig(t, `{"bridges":[{"key":"esp32-a","baseUrl":"http://bridge.invalid","profile":"ml307a","username":"agent","password":"secret"}]}`)
	if err := attachRemoteATBridges(scanner, registry, nil, path, discardLogger()); err != nil {
		t.Fatalf("attachRemoteATBridges: %v", err)
	}
	if scanner.ExtraDevices == nil {
		t.Fatal("bridge assembly did not register the contributed device source")
	}
	if scanner.Querier == nil || scanner.Querier == baseline {
		t.Fatal("bridge assembly did not replace the AT querier with the routing transport")
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

func TestAttachRemoteATBridgesFailsClosed(t *testing.T) {
	registry := modemadapter.DefaultRegistry()
	for _, testCase := range []struct{ name, body string }{
		{name: "unknown profile", body: `{"bridges":[{"key":"a","baseUrl":"http://bridge.invalid","profile":"unknown"}]}`},
		{name: "duplicate key", body: `{"bridges":[{"key":"a","baseUrl":"http://bridge.invalid","profile":"ml307a"},{"key":"a","baseUrl":"http://other.invalid","profile":"ml307a"}]}`},
		{name: "credentials half configured", body: `{"bridges":[{"key":"a","baseUrl":"http://bridge.invalid","profile":"ml307a","username":"agent"}]}`},
		{name: "invalid url", body: `{"bridges":[{"key":"a","baseUrl":"mqtt://bridge.invalid","profile":"ml307a"}]}`},
		{name: "no bridges", body: `{"bridges":[]}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			scanner := hardwareprobe.NewScanner()
			scanner.Adapters = registry
			baseline := scanner.Querier
			err := attachRemoteATBridges(scanner, registry, nil, writeBridgeConfig(t, testCase.body), discardLogger())
			if err == nil {
				t.Fatal("attachRemoteATBridges accepted an unusable configuration")
			}
			if scanner.ExtraDevices != nil {
				t.Fatal("failed assembly left a contributed device source behind")
			}
			if scanner.Querier != baseline {
				t.Fatal("failed assembly replaced the AT querier")
			}
		})
	}
}

func TestAttachRemoteATBridgesRejectsUnsafeConfigurationFile(t *testing.T) {
	registry := modemadapter.DefaultRegistry()
	scanner := hardwareprobe.NewScanner()
	scanner.Adapters = registry
	path := filepath.Join(t.TempDir(), "remote-at.json")
	body := `{"bridges":[{"key":"a","baseUrl":"http://bridge.invalid","profile":"ml307a"}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write bridge configuration: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("set bridge configuration mode: %v", err)
	}
	if err := attachRemoteATBridges(scanner, registry, nil, path, discardLogger()); err == nil {
		t.Fatal("attachRemoteATBridges accepted a world-readable configuration")
	}
	if err := attachRemoteATBridges(scanner, registry, nil, filepath.Join(t.TempDir(), "absent.json"), discardLogger()); err == nil {
		t.Fatal("attachRemoteATBridges accepted a missing configuration")
	}
}
