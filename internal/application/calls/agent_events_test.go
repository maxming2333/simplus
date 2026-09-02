package calls

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leonfox28/simplus/internal/agentapi"
)

type stubCallEventClient struct {
	response agentapi.CallEventsResponse
	err      error
	seen     agentapi.CallEventsRequest
}

func (client *stubCallEventClient) ReadCallEvents(_ context.Context, request agentapi.CallEventsRequest) (agentapi.CallEventsResponse, error) {
	client.seen = request
	return client.response, client.err
}

const gatewayInstanceID = "01234567-89ab-cdef-0123-456789abcdef"

func TestAgentCallEventGatewayForwardsTheTargetAndTranslatesTheReport(t *testing.T) {
	client := &stubCallEventClient{response: agentapi.CallEventsResponse{
		BootID: "0f3a1c2b4d5e6f70", LatestSequence: 7, OldestSequence: 6,
		Events: []agentapi.CallEvent{
			{Sequence: 6, Number: "15817320262", ObservedAt: time.Unix(1772505600, 0).UTC()},
			{Sequence: 7, Number: "13334262200", ObservedAt: time.Unix(1772505700, 0).UTC()},
		},
	}}
	gateway, err := NewAgentCallEventGateway(client, gatewayInstanceID)
	if err != nil {
		t.Fatalf("NewAgentCallEventGateway: %v", err)
	}
	report, err := gateway.ReadCallEvents(context.Background(), CallEventTarget{
		DeviceID: "bridge-esp32-a", DeviceGeneration: 4, After: 5,
	})
	if err != nil {
		t.Fatalf("ReadCallEvents: %v", err)
	}
	if client.seen.AgentInstanceID != gatewayInstanceID || client.seen.DeviceID != "bridge-esp32-a" ||
		client.seen.DeviceGeneration != 4 || client.seen.After != 5 {
		t.Fatalf("request = %+v", client.seen)
	}
	if report.BootID != "0f3a1c2b4d5e6f70" || report.LatestSequence != 7 || report.OldestSequence != 6 {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Events) != 2 || report.Events[0].Number != "15817320262" ||
		!report.Events[1].ObservedAt.Equal(time.Unix(1772505700, 0).UTC()) {
		t.Fatalf("events = %+v", report.Events)
	}
}

// TestAgentCallEventGatewayReportsNoRingAsNothingToRead is the distinction that
// keeps a supported configuration quiet. A locally attached modem keeps no ring;
// treating that as a failure would log and retry every two seconds forever.
func TestAgentCallEventGatewayReportsNoRingAsNothingToRead(t *testing.T) {
	gateway, err := NewAgentCallEventGateway(&stubCallEventClient{err: agentapi.ErrCallEventsUnsupported}, gatewayInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = gateway.ReadCallEvents(context.Background(), CallEventTarget{DeviceID: "usb-1-3", DeviceGeneration: 1})
	if !errors.Is(err, ErrCallEventsUnsupported) {
		t.Fatalf("err = %v, want the absence of a ring, not a failure", err)
	}
}

func TestAgentCallEventGatewayForwardsRealFailures(t *testing.T) {
	for name, cause := range map[string]error{
		"unavailable":  agentapi.ErrCallEventsUnavailable,
		"stale device": agentapi.ErrCallEventsDeviceStale,
		"bad report":   agentapi.ErrCallEventsBackendInvalid,
	} {
		t.Run(name, func(t *testing.T) {
			gateway, err := NewAgentCallEventGateway(&stubCallEventClient{err: cause}, gatewayInstanceID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = gateway.ReadCallEvents(context.Background(), CallEventTarget{DeviceID: "bridge-a", DeviceGeneration: 1})
			if !errors.Is(err, cause) {
				t.Fatalf("err = %v, want the cause preserved", err)
			}
			if errors.Is(err, ErrCallEventsUnsupported) {
				t.Fatal("a real failure was hidden as an absent ring")
			}
		})
	}
}

func TestNewAgentCallEventGatewayRejectsAnIncompleteConfiguration(t *testing.T) {
	if _, err := NewAgentCallEventGateway(nil, gatewayInstanceID); err == nil {
		t.Fatal("a gateway was built without a client")
	}
	if _, err := NewAgentCallEventGateway(&stubCallEventClient{}, "not-a-uuid"); err == nil {
		t.Fatal("a gateway was built without a valid agent instance")
	}
}
