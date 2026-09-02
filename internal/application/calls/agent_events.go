package calls

import (
	"context"
	"errors"

	"github.com/leonfox28/simplus/internal/agentapi"
)

// CallEventClientAPI is the agent operation this gateway needs, and nothing more.
type CallEventClientAPI interface {
	ReadCallEvents(context.Context, agentapi.CallEventsRequest) (agentapi.CallEventsResponse, error)
}

// AgentCallEventGateway reads observed inbound calls through the agent.
type AgentCallEventGateway struct {
	client     CallEventClientAPI
	instanceID string
}

func NewAgentCallEventGateway(client CallEventClientAPI, instanceID string) (*AgentCallEventGateway, error) {
	if client == nil || !agentapi.IsValidAgentInstanceID(instanceID) {
		return nil, errors.New("Agent call event gateway configuration is invalid")
	}
	return &AgentCallEventGateway{client: client, instanceID: instanceID}, nil
}

var _ CallEventReader = (*AgentCallEventGateway)(nil)

// ReadCallEvents performs one bounded read.
//
// A device the agent does not serve call events for is reported as an empty ring
// at the caller's own position rather than as a failure. That distinction matters:
// a failure would be retried and logged every two seconds for the lifetime of a
// deployment whose modem is locally attached and has no event ring at all, which is
// a supported configuration and not a fault.
func (gateway *AgentCallEventGateway) ReadCallEvents(ctx context.Context, target CallEventTarget) (CallEventReport, error) {
	response, err := gateway.client.ReadCallEvents(ctx, agentapi.CallEventsRequest{
		AgentInstanceID:  gateway.instanceID,
		DeviceID:         target.DeviceID,
		After:            target.After,
		DeviceGeneration: target.DeviceGeneration,
	})
	if err != nil {
		if errors.Is(err, agentapi.ErrCallEventsUnsupported) {
			return CallEventReport{}, ErrCallEventsUnsupported
		}
		return CallEventReport{}, err
	}
	events := make([]ObservedCall, 0, len(response.Events))
	for _, event := range response.Events {
		events = append(events, ObservedCall{
			Sequence: event.Sequence, Number: event.Number, ObservedAt: event.ObservedAt,
		})
	}
	return CallEventReport{
		BootID:         response.BootID,
		LatestSequence: response.LatestSequence,
		OldestSequence: response.OldestSequence,
		Events:         events,
	}, nil
}
