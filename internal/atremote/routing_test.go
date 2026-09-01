package atremote

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leonfox28/simplus/internal/attransport"
)

type recordingOpener struct {
	name      string
	endpoints []string
	session   attransport.Session
	err       error
}

func (opener *recordingOpener) Open(endpoint string) (attransport.Session, error) {
	opener.endpoints = append(opener.endpoints, endpoint)
	if opener.err != nil {
		return nil, opener.err
	}
	return opener.session, nil
}

type noopSession struct{}

func (noopSession) Query(context.Context, string, time.Duration) ([]string, error) { return nil, nil }

func (noopSession) Close() {}

func TestRoutingOpenerRoutesByLocatorOnly(t *testing.T) {
	bridge := &recordingOpener{name: "bridge", session: noopSession{}}
	local := &recordingOpener{name: "local", session: noopSession{}}
	opener := NewRoutingOpener(bridge, local)

	for _, endpoint := range []string{Locator("esp32-a"), EndpointScheme + "BAD_KEY", EndpointScheme} {
		if _, err := opener.Open(endpoint); err != nil {
			t.Fatalf("Open(%q): %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{"/dev/ttyUSB2", "/dev/ttyACM0", "", "relative/path"} {
		if _, err := opener.Open(endpoint); err != nil {
			t.Fatalf("Open(%q): %v", endpoint, err)
		}
	}
	if len(bridge.endpoints) != 3 {
		t.Fatalf("bridge endpoints = %#v", bridge.endpoints)
	}
	if len(local.endpoints) != 4 {
		t.Fatalf("local endpoints = %#v", local.endpoints)
	}
}

func TestRoutingOpenerNeverFallsBackBetweenTransports(t *testing.T) {
	bridgeFailure := attransport.NewOpenError(attransport.OpenBusy, true, errors.New("bridge busy"))
	localFailure := attransport.NewOpenError(attransport.OpenPermission, false, errors.New("tty permission"))
	bridge := &recordingOpener{name: "bridge", err: bridgeFailure}
	local := &recordingOpener{name: "local", err: localFailure}
	opener := NewRoutingOpener(bridge, local)

	_, err := opener.Open(Locator("esp32-a"))
	if !errors.Is(err, bridgeFailure) {
		t.Fatalf("bridge locator error = %v, want the bridge failure", err)
	}
	if len(local.endpoints) != 0 {
		t.Fatalf("bridge failure fell back to the local transport: %#v", local.endpoints)
	}

	_, err = opener.Open("/dev/ttyUSB2")
	if !errors.Is(err, localFailure) {
		t.Fatalf("tty endpoint error = %v, want the local failure", err)
	}
	if len(bridge.endpoints) != 1 {
		t.Fatalf("local failure fell back to the bridge transport: %#v", bridge.endpoints)
	}
}

func TestRoutingOpenerFailsClosedForMissingTransport(t *testing.T) {
	bridgeOnly := NewRoutingOpener(&recordingOpener{session: noopSession{}}, nil)
	if _, err := bridgeOnly.Open("/dev/ttyUSB2"); !openKindIs(err, attransport.OpenUnsupported) {
		t.Fatalf("missing local transport error = %v", err)
	}
	localOnly := NewRoutingOpener(nil, &recordingOpener{session: noopSession{}})
	if _, err := localOnly.Open(Locator("esp32-a")); !openKindIs(err, attransport.OpenUnsupported) {
		t.Fatalf("missing bridge transport error = %v", err)
	}
	empty := NewRoutingOpener(nil, nil)
	if _, err := empty.Open(Locator("esp32-a")); !openKindIs(err, attransport.OpenUnsupported) {
		t.Fatalf("empty router bridge error = %v", err)
	}
	if _, err := empty.Open("/dev/ttyUSB2"); !openKindIs(err, attransport.OpenUnsupported) {
		t.Fatalf("empty router local error = %v", err)
	}
}

func openKindIs(err error, expected string) bool {
	kind, _, ok := attransport.OpenFailure(err)
	return ok && kind == expected
}
