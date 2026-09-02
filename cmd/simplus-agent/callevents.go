package main

import (
	"context"
	"errors"
	"time"

	"github.com/leonfox28/simplus/internal/agentapi"
	"github.com/leonfox28/simplus/internal/atremote"
	"github.com/leonfox28/simplus/internal/modemadapter"
)

// bridgeCallReader reads a bridge's observed inbound calls. Only the remote AT
// transport implements it; the interface exists so this backend can be tested
// without a bridge.
type bridgeCallReader interface {
	CallEvents(ctx context.Context, endpoint string, after uint32) (atremote.CallEventsSnapshot, error)
}

// callEventsBackend answers the agent's call-events operation.
//
// It lives in the executable that assembles the transport because it must decide
// whether a device is reached through a bridge, and that routing fact is not
// allowed to leak into the protocol, application or adapter layers. The protocol
// layer therefore passes an opaque device identity and this backend resolves it,
// exactly as the RF backend owns its own endpoint resolution.
type callEventsBackend struct {
	monitor  *agentapi.Monitor
	adapters *modemadapter.Registry
	reader   bridgeCallReader
	// now supplies the substitute for an unsynchronized bridge clock.
	now func() time.Time
}

func newCallEventsBackend(monitor *agentapi.Monitor, adapters *modemadapter.Registry, reader bridgeCallReader) *callEventsBackend {
	if monitor == nil || adapters == nil || reader == nil {
		return nil
	}
	return &callEventsBackend{monitor: monitor, adapters: adapters, reader: reader, now: time.Now}
}

// CallEvents reads one bounded page of a device's observed inbound calls.
//
// It takes no operation gate and opens no AT session. A notification must never
// be able to queue behind, or delay, a message being sent.
func (backend *callEventsBackend) CallEvents(ctx context.Context, request agentapi.CallEventsRequest) (agentapi.CallEventsResponse, error) {
	if backend == nil || backend.reader == nil {
		return agentapi.CallEventsResponse{}, agentapi.ErrCallEventsUnavailable
	}
	endpoint, err := backend.resolveBridgeEndpoint(request.DeviceID)
	if err != nil {
		return agentapi.CallEventsResponse{}, err
	}
	report, err := backend.reader.CallEvents(ctx, endpoint, request.After)
	if err != nil {
		// The transport's errors are deliberately opaque about the bridge, so they
		// are collapsed into the operation's own vocabulary rather than forwarded.
		if errors.Is(err, atremote.ErrCallEventsUnavailable) {
			return agentapi.CallEventsResponse{}, agentapi.ErrCallEventsUnsupported
		}
		return agentapi.CallEventsResponse{}, agentapi.ErrCallEventsUnavailable
	}
	// The receive time is captured once, after the read, so every event in this
	// page that needs a substitute gets the same one. Sampling per event would
	// order them by when they were converted rather than when they arrived.
	received := backend.now().UTC()
	events := make([]agentapi.CallEvent, 0, len(report.Events))
	for _, event := range report.Events {
		events = append(events, agentapi.CallEvent{
			Sequence:   event.Sequence,
			Number:     event.Number,
			ObservedAt: observedAt(event.ObservedAt, received),
		})
	}
	return agentapi.CallEventsResponse{
		BootID:         report.BootID,
		LatestSequence: report.LatestSequence,
		OldestSequence: report.OldestSequence,
		Events:         events,
	}, nil
}

// resolveBridgeEndpoint finds the control endpoint of a device that can serve
// call events, and refuses one that cannot.
func (backend *callEventsBackend) resolveBridgeEndpoint(deviceID string) (string, error) {
	for _, device := range backend.monitor.Snapshot().Devices {
		if device.ID != deviceID {
			continue
		}
		adapter, ok := backend.adapters.ForProfile(device.Profile)
		if !ok {
			return "", agentapi.ErrCallEventsUnsupported
		}
		endpoint, ok := adapter.Endpoint(device, modemadapter.EndpointPrimaryAT)
		if !ok || endpoint.Node == "" {
			return "", agentapi.ErrCallEventsUnsupported
		}
		// A locally attached modem has no event ring: nothing between the modem and
		// this process records its caller-line notifications, and they are gone by
		// the time anyone could poll for them. Saying so is better than returning an
		// empty page, which a consumer could not distinguish from "no calls".
		if !atremote.IsLocator(endpoint.Node) {
			return "", agentapi.ErrCallEventsUnsupported
		}
		return endpoint.Node, nil
	}
	return "", agentapi.ErrCallEventsDeviceNotFound
}

// observedAt substitutes the local receive time when the bridge's clock was
// unsynchronized, so an unknown arrival time never reaches a consumer as a value
// it might read as 1970.
func observedAt(bridgeSeconds uint32, received time.Time) time.Time {
	if bridgeSeconds == 0 {
		return received
	}
	return time.Unix(int64(bridgeSeconds), 0).UTC()
}
