package standardsms

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/leonfox28/simplus/internal/agentapi"
	"github.com/leonfox28/simplus/internal/attransport"
	"github.com/leonfox28/simplus/internal/modemadapter"
)

// countingOpener records how many conversations a whole operation costs, which
// is the observable difference between per-command and per-operation scoping.
type countingOpener struct {
	opens     int
	endpoints []string
	responses map[string][]string
	promptErr error
	prompts   []string
}

func (opener *countingOpener) Open(endpoint string) (attransport.Session, error) {
	opener.opens++
	opener.endpoints = append(opener.endpoints, endpoint)
	return &countingSession{opener: opener}, nil
}

type countingSession struct {
	opener *countingOpener
	closed bool
}

func (session *countingSession) Query(_ context.Context, command string, _ time.Duration) ([]string, error) {
	if lines, found := session.opener.responses[command]; found {
		return lines, nil
	}
	return []string{"OK"}, nil
}

func (session *countingSession) Exchange(_ context.Context, command string, _ []byte, _ time.Duration) ([]string, error) {
	session.opener.prompts = append(session.opener.prompts, command)
	if session.opener.promptErr != nil {
		return nil, session.opener.promptErr
	}
	return []string{"+CMGS: 7", "OK"}, nil
}

func (session *countingSession) Close() { session.closed = true }

// bridgedDevice deliberately uses an opaque control locator with no recognizable
// scheme: this package must work for any endpoint the composition root resolves
// and must never be able to tell one transport from another.
func bridgedDevice() agentapi.DeviceReport {
	return agentapi.DeviceReport{
		ID: "bridge-a", Profile: agentapi.ProfileML307A,
		Interfaces: []agentapi.USBInterface{{
			Number:    2,
			Endpoints: []agentapi.Endpoint{{Kind: agentapi.EndpointTTY, InterfaceNumber: 2, Node: "opaque-control-locator-a"}},
		}},
	}
}

func openerDriver(t *testing.T, opener *countingOpener) *Driver {
	t.Helper()
	transport, err := NewOpenerTransport(opener)
	if err != nil {
		t.Fatalf("NewOpenerTransport: %v", err)
	}
	driver, err := NewDriver(modemadapter.ML307ASMS{}, transport)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	return driver
}

// TestOperationUsesOneConversation is the regression guard for the scoping fix.
// Per-command conversations would let another consumer interleave between the
// mandatory mode/storage selection and the operation itself, which is exactly
// what the exclusive conversation exists to prevent.
func TestOperationUsesOneConversation(t *testing.T) {
	for _, testCase := range []struct {
		name string
		run  func(*Driver, agentapi.DeviceReport) error
	}{
		{
			name: "list",
			run: func(driver *Driver, device agentapi.DeviceReport) error {
				_, err := driver.List(context.Background(), device)
				return err
			},
		},
		{
			name: "read",
			run: func(driver *Driver, device agentapi.DeviceReport) error {
				_, err := driver.Read(context.Background(), device, 1)
				return err
			},
		},
		{
			name: "delete",
			run: func(driver *Driver, device agentapi.DeviceReport) error {
				return driver.Delete(context.Background(), device, 1)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			opener := &countingOpener{responses: map[string][]string{
				`AT+CPMS="SM","SM","SM"`: {"+CPMS: 0,30,0,30,0,30", "OK"},
				"AT+CMGR=1":              {`+CMGR: 0,,25`, "0891683110301105F0240B818176257803F10008527080316405238C0500034A0201", "OK"},
			}}
			driver := openerDriver(t, opener)
			if err := testCase.run(driver, bridgedDevice()); err != nil {
				t.Fatalf("%s: %v", testCase.name, err)
			}
			if opener.opens != 1 {
				t.Fatalf("%s used %d conversations, want exactly 1", testCase.name, opener.opens)
			}
			if len(opener.endpoints) != 1 || opener.endpoints[0] != "opaque-control-locator-a" {
				t.Fatalf("conversation endpoints = %#v", opener.endpoints)
			}
		})
	}
}

func TestMultipartSubmissionStaysInOneConversation(t *testing.T) {
	opener := &countingOpener{responses: map[string][]string{
		`AT+CPMS="SM","SM","SM"`: {"+CPMS: 0,30,0,30,0,30", "OK"},
	}}
	driver := openerDriver(t, opener)
	result, err := driver.Send(context.Background(), bridgedDevice(), "+12025550123", strings.Repeat("A", 200))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(result.Parts) < 2 {
		t.Fatalf("expected a multipart submission, got %d part(s)", len(result.Parts))
	}
	if opener.opens != 1 {
		t.Fatalf("multipart submission used %d conversations, want exactly 1", opener.opens)
	}
	if len(opener.prompts) != len(result.Parts) {
		t.Fatalf("prompt exchanges = %d, submitted parts = %d", len(opener.prompts), len(result.Parts))
	}
}

// TestPromptFailureClassificationSurvivesScoping keeps the retry-safety split
// intact: only an explicit not-reached signal may be reported as not dispatched.
func TestPromptFailureClassificationSurvivesScoping(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		cause         error
		notDispatched bool
	}{
		{name: "prompt never reached", cause: attransport.ErrPromptNotReached, notDispatched: true},
		{name: "prompt unsupported", cause: attransport.ErrPromptUnsupported, notDispatched: true},
		{name: "uncertain outcome", cause: errors.New("AT query timed out")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			opener := &countingOpener{
				responses: map[string][]string{`AT+CPMS="SM","SM","SM"`: {"+CPMS: 0,30,0,30,0,30", "OK"}},
				promptErr: testCase.cause,
			}
			driver := openerDriver(t, opener)
			_, err := driver.Send(context.Background(), bridgedDevice(), "+12025550123", "hello")
			if err == nil {
				t.Fatal("Send succeeded against a failing prompt")
			}
			if errors.Is(err, ErrPromptNotDispatched) != testCase.notDispatched {
				t.Fatalf("not-dispatched classification = %v, want %v (err=%v)",
					!testCase.notDispatched, testCase.notDispatched, err)
			}
		})
	}
}

// TestBoundConversationRefusesAnotherEndpoint proves a scoped conversation is not
// silently redirected, which would let one device's operation reach another.
func TestBoundConversationRefusesAnotherEndpoint(t *testing.T) {
	transport, err := NewOpenerTransport(&countingOpener{})
	if err != nil {
		t.Fatalf("NewOpenerTransport: %v", err)
	}
	bound, release, err := transport.Begin("opaque-control-locator-a")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer release()
	if _, err := bound.Command(context.Background(), "opaque-control-locator-b", "AT", time.Second); !errors.Is(err, ErrControlEndpoint) {
		t.Fatalf("mismatched endpoint error = %v, want ErrControlEndpoint", err)
	}
	if _, err := bound.Prompt(context.Background(), "opaque-control-locator-b", "AT+CMGS=1", []byte("41\x1a"), time.Second); !errors.Is(err, ErrPromptNotDispatched) {
		t.Fatalf("mismatched endpoint prompt error = %v, want ErrPromptNotDispatched", err)
	}
}
