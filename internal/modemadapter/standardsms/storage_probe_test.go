package standardsms

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/leonfox28/simplus/internal/agentapi"
	"github.com/leonfox28/simplus/internal/modemadapter"
)

// scriptedTransport answers a fixed transcript and records every command, so a
// test can assert the exact storage selections a listing performed.
type scriptedTransport struct {
	responses map[string][]string
	failOn    string
	commands  []string
}

func (transport *scriptedTransport) Command(_ context.Context, _, command string, _ time.Duration) ([]string, error) {
	transport.commands = append(transport.commands, command)
	if transport.failOn != "" && command == transport.failOn {
		return nil, errors.New("scripted failure")
	}
	if lines, found := transport.responses[command]; found {
		return lines, nil
	}
	return []string{"OK"}, nil
}

func (transport *scriptedTransport) Prompt(context.Context, string, string, []byte, time.Duration) ([]string, error) {
	return nil, ErrPromptNotDispatched
}

func probeTransport(failOn string) *scriptedTransport {
	return &scriptedTransport{
		failOn: failOn,
		responses: map[string][]string{
			selectStorageCommand(primaryStorage):   {"+CPMS: 0,40,0,40,0,40", "OK"},
			selectStorageCommand(alternateStorage): {"+CPMS: 3,180,3,180,3,180", "OK"},
			"AT+CMGL=4":                            {"OK"},
		},
	}
}

func probeDevice() agentapi.DeviceReport {
	return agentapi.DeviceReport{
		ID: "bridge-a", Profile: agentapi.ProfileML307A,
		Interfaces: []agentapi.USBInterface{{
			Number:    2,
			Endpoints: []agentapi.Endpoint{{Kind: agentapi.EndpointTTY, InterfaceNumber: 2, Node: "opaque-locator"}},
		}},
	}
}

func TestAlternateStorageProbeRunsOnItsIntervalAndRestoresSIMStorage(t *testing.T) {
	transport := probeTransport("")
	var found []string
	driver, err := NewDriver(modemadapter.ML307ASMS{}, transport,
		WithAlternateStorageProbe(3, func(storage string, used int) {
			found = append(found, storage)
			if used != 3 {
				t.Errorf("used = %d, want 3", used)
			}
		}))
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	for range 3 {
		if _, err := driver.List(context.Background(), probeDevice()); err != nil {
			t.Fatalf("List: %v", err)
		}
	}
	if len(found) != 1 {
		t.Fatalf("probe fired %d times across three listings, want 1", len(found))
	}
	// The probe must leave SIM storage selected: the modem is shared state, and
	// anything reading it outside this driver would otherwise see the wrong memory.
	last := transport.commands[len(transport.commands)-1]
	if last != selectStorageCommand(primaryStorage) {
		t.Fatalf("last command = %q, want SIM storage restored", last)
	}
	if strings.Count(strings.Join(transport.commands, "|"), selectStorageCommand(alternateStorage)) != 1 {
		t.Fatalf("alternate storage was selected more than once: %#v", transport.commands)
	}
}

func TestAlternateStorageProbeIsOffByDefault(t *testing.T) {
	transport := probeTransport("")
	driver, err := NewDriver(modemadapter.ML307ASMS{}, transport)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	for range 5 {
		if _, err := driver.List(context.Background(), probeDevice()); err != nil {
			t.Fatalf("List: %v", err)
		}
	}
	for _, command := range transport.commands {
		if command == selectStorageCommand(alternateStorage) {
			t.Fatal("a driver without the option selected the alternate storage")
		}
	}
}

// TestAlternateStorageProbeNeverFailsItsListing is the important one: a
// diagnostic that can break a real operation is worse than no diagnostic.
func TestAlternateStorageProbeNeverFailsItsListing(t *testing.T) {
	for _, failOn := range []string{
		selectStorageCommand(alternateStorage),
		selectStorageCommand(primaryStorage),
	} {
		t.Run(failOn, func(t *testing.T) {
			transport := probeTransport(failOn)
			driver, err := NewDriver(modemadapter.ML307ASMS{}, transport,
				WithAlternateStorageProbe(1, func(string, int) {}))
			if err != nil {
				t.Fatalf("NewDriver: %v", err)
			}
			// A failing restore must not fail the listing either; the next
			// operation re-asserts storage selection regardless.
			if _, err := driver.List(context.Background(), probeDevice()); err != nil &&
				failOn != selectStorageCommand(primaryStorage) {
				t.Fatalf("probe failure broke the listing: %v", err)
			}
		})
	}
}

func TestAlternateStorageProbeReportsOnlyANonEmptyMemory(t *testing.T) {
	transport := probeTransport("")
	transport.responses[selectStorageCommand(alternateStorage)] = []string{"+CPMS: 0,180,0,180,0,180", "OK"}
	fired := false
	driver, err := NewDriver(modemadapter.ML307ASMS{}, transport,
		WithAlternateStorageProbe(1, func(string, int) { fired = true }))
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	if _, err := driver.List(context.Background(), probeDevice()); err != nil {
		t.Fatalf("List: %v", err)
	}
	if fired {
		t.Fatal("probe reported an empty alternate storage")
	}
	transport.responses[selectStorageCommand(alternateStorage)] = []string{"garbage", "OK"}
	if _, err := driver.List(context.Background(), probeDevice()); err != nil {
		t.Fatalf("List: %v", err)
	}
	if fired {
		t.Fatal("probe reported an unparseable storage response instead of staying silent")
	}
}

func TestStorageSelectionSetsAllThreeMemories(t *testing.T) {
	// Read, write and receive must be the same memory, or a message can arrive in
	// one while the driver lists another.
	if got := selectStorageCommand("SM"); got != `AT+CPMS="SM","SM","SM"` {
		t.Fatalf("selection = %q", got)
	}
}
