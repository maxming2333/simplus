package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type stubCallEventsBackend struct {
	response CallEventsResponse
	err      error
	seen     CallEventsRequest
}

func (backend *stubCallEventsBackend) CallEvents(_ context.Context, request CallEventsRequest) (CallEventsResponse, error) {
	backend.seen = request
	return backend.response, backend.err
}

const (
	callEventsBootID     = "0f3a1c2b4d5e6f70"
	callEventsInstanceID = "01234567-89ab-cdef-0123-456789abcdef"
)

func callEventsMonitor(t *testing.T) (*Monitor, uint64) {
	t.Helper()
	monitor := newMonitor(&monitorScanner{devices: []DeviceReport{{
		ID: "bridge-a", DisplayName: "ML307A", PhysicalPath: "bridge-a", Profile: ProfileML307A,
	}}}, callEventsInstanceID, 1)
	snapshot, err := monitor.Refresh(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return monitor, snapshot.Devices[0].Generation
}

func callEventsRequest(monitor *Monitor, generation uint64) CallEventsRequest {
	return CallEventsRequest{
		AgentInstanceID:  monitor.InstanceID(),
		DeviceID:         "bridge-a",
		After:            5,
		DeviceGeneration: generation,
	}
}

func postCallEvents(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/calls/events", strings.NewReader(body)))
	return recorder
}

func callEventsReport() CallEventsResponse {
	return CallEventsResponse{
		BootID: callEventsBootID, LatestSequence: 7, OldestSequence: 6,
		Events: []CallEvent{
			{Sequence: 6, Number: "15817320262", ObservedAt: time.Unix(1772505600, 0).UTC()},
			{Sequence: 7, Number: "", ObservedAt: time.Unix(1772505700, 0).UTC()},
		},
	}
}

func TestCallEventsServiceReadsAndStampsTheEnvelope(t *testing.T) {
	monitor, generation := callEventsMonitor(t)
	backend := &stubCallEventsBackend{response: callEventsReport()}
	service := NewCallEventsService(monitor, backend)
	response, err := service.Read(context.Background(), callEventsRequest(monitor, generation))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if response.ProtocolVersion != ProtocolVersion || response.AgentInstanceID != monitor.InstanceID() {
		t.Fatalf("envelope not stamped by the service: %+v", response)
	}
	if response.BootID != callEventsBootID || len(response.Events) != 2 {
		t.Fatalf("report = %+v", response)
	}
	// The cursor must reach the backend unchanged; it is what bounds the read.
	if backend.seen.After != 5 {
		t.Fatalf("backend saw cursor %d, want 5", backend.seen.After)
	}
}

func TestCallEventsServiceFailsClosedOnStaleAndUnknownTargets(t *testing.T) {
	monitor, generation := callEventsMonitor(t)
	base := callEventsRequest(monitor, generation)
	for name, mutate := range map[string]func(*CallEventsRequest){
		"stale agent":     func(r *CallEventsRequest) { r.AgentInstanceID = strings.Repeat("b", len(r.AgentInstanceID)) },
		"unknown device":  func(r *CallEventsRequest) { r.DeviceID = "bridge-z" },
		"stale device":    func(r *CallEventsRequest) { r.DeviceGeneration = 99 },
		"zero generation": func(r *CallEventsRequest) { r.DeviceGeneration = 0 },
		"blank device":    func(r *CallEventsRequest) { r.DeviceID = "   " },
		"long device":     func(r *CallEventsRequest) { r.DeviceID = strings.Repeat("d", 129) },
		"blank instance":  func(r *CallEventsRequest) { r.AgentInstanceID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			request := base
			mutate(&request)
			backend := &stubCallEventsBackend{response: callEventsReport()}
			service := NewCallEventsService(monitor, backend)
			if _, err := service.Read(context.Background(), request); err == nil {
				t.Fatal("the read was allowed")
			}
			if backend.seen.DeviceID != "" {
				t.Fatal("the backend was reached despite a failed check")
			}
		})
	}
}

func TestCallEventsServiceRefusesUnusableBackendReports(t *testing.T) {
	monitor, generation := callEventsMonitor(t)
	request := callEventsRequest(monitor, generation)
	oversize := callEventsReport()
	oversize.Events = make([]CallEvent, maximumCallEvents+1)
	for index := range oversize.Events {
		oversize.Events[index] = CallEvent{Sequence: uint32(index + 6), Number: "1", ObservedAt: time.Unix(1, 0)}
	}
	oversize.LatestSequence = 999
	for name, mutate := range map[string]func(*CallEventsResponse){
		"missing bootId":   func(r *CallEventsResponse) { r.BootID = "" },
		"uppercase bootId": func(r *CallEventsResponse) { r.BootID = strings.ToUpper(callEventsBootID) },
		"nil events":       func(r *CallEventsResponse) { r.Events = nil },
		"oldest beyond latest": func(r *CallEventsResponse) {
			r.OldestSequence, r.LatestSequence = 9, 1
		},
		// An event at or below the requested cursor would make the consumer
		// reprocess something it already recorded.
		"at the cursor": func(r *CallEventsResponse) { r.Events[0].Sequence = 5 },
		"before the cursor": func(r *CallEventsResponse) {
			r.Events = []CallEvent{{Sequence: 1, Number: "1", ObservedAt: time.Unix(1, 0)}}
		},
		"descending":      func(r *CallEventsResponse) { r.Events[0].Sequence, r.Events[1].Sequence = 7, 6 },
		"beyond latest":   func(r *CallEventsResponse) { r.Events[1].Sequence = 99 },
		"oversize number": func(r *CallEventsResponse) { r.Events[0].Number = strings.Repeat("9", 24) },
		// The backend substitutes its own receive time when the bridge's clock was
		// unset, so an ambiguous zero must never cross this boundary.
		"unknown time": func(r *CallEventsResponse) { r.Events[0].ObservedAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			report := callEventsReport()
			mutate(&report)
			service := NewCallEventsService(monitor, &stubCallEventsBackend{response: report})
			if _, err := service.Read(context.Background(), request); !errors.Is(err, ErrCallEventsBackendInvalid) {
				t.Fatalf("err = %v, want the report refused", err)
			}
		})
	}
	t.Run("more than the ring", func(t *testing.T) {
		service := NewCallEventsService(monitor, &stubCallEventsBackend{response: oversize})
		if _, err := service.Read(context.Background(), request); !errors.Is(err, ErrCallEventsBackendInvalid) {
			t.Fatalf("err = %v, want an overlong report refused rather than truncated", err)
		}
	})
}

func TestNewCallEventsServiceIsNilWithoutBothDependencies(t *testing.T) {
	// A deployment without a bridge must expose no route at all rather than one
	// that fails at request time.
	if NewCallEventsService(nil, &stubCallEventsBackend{}) != nil {
		t.Fatal("a service was built without a monitor")
	}
	monitor, _ := callEventsMonitor(t)
	if NewCallEventsService(monitor, nil) != nil {
		t.Fatal("a service was built without a backend")
	}
	var absent *CallEventsService
	if _, err := absent.Read(context.Background(), CallEventsRequest{}); !errors.Is(err, ErrCallEventsUnavailable) {
		t.Fatalf("err = %v, want unavailable", err)
	}
}

func TestCallEventsRouteAndFeatureAppearTogether(t *testing.T) {
	monitor, _ := callEventsMonitor(t)
	configured := NewCallEventsService(monitor, &stubCallEventsBackend{response: callEventsReport()})
	for name, tc := range map[string]struct {
		service     *CallEventsService
		wantFeature bool
	}{
		"configured": {service: configured, wantFeature: true},
		// Both switches must move together. Advertising without a route, or a route
		// without the advertisement, leaves a consumer unable to discover it.
		"absent": {service: nil, wantFeature: false},
	} {
		t.Run(name, func(t *testing.T) {
			handler := NewManagedHardwareHandler(monitor, nil, nil, tc.service, nil)
			helloRecorder := httptest.NewRecorder()
			handler.ServeHTTP(helloRecorder, httptest.NewRequest(http.MethodGet, "/v1/hello", nil))
			var hello Hello
			if err := json.Unmarshal(helloRecorder.Body.Bytes(), &hello); err != nil {
				t.Fatal(err)
			}
			if containsString(hello.Features, FeatureCallEvents) != tc.wantFeature {
				t.Fatalf("feature advertised = %v, want %v", !tc.wantFeature, tc.wantFeature)
			}
			probe := postCallEvents(t, handler, `{}`)
			if tc.wantFeature && probe.Code == http.StatusNotFound {
				t.Fatal("the route is absent while the feature is advertised")
			}
			if !tc.wantFeature && probe.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want the route absent", probe.Code)
			}
		})
	}
}

func TestCallEventsHandlerRejectsUnboundedAndUnknownBodies(t *testing.T) {
	monitor, _ := callEventsMonitor(t)
	service := NewCallEventsService(monitor, &stubCallEventsBackend{response: callEventsReport()})
	handler := NewManagedHardwareHandler(monitor, nil, nil, service, nil)
	for name, body := range map[string]string{
		"empty":           `{}`,
		"unknown field":   `{"agentInstanceId":"` + callEventsInstanceID + `","deviceId":"bridge-a","surprise":1}`,
		"trailing object": `{"deviceId":"bridge-a"}{"deviceId":"bridge-a"}`,
		"not an object":   `[]`,
		"oversize":        `{"deviceId":"` + strings.Repeat("x", 32<<10) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if recorder := postCallEvents(t, handler, body); recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want a refused request: %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestCallEventsErrorsMapToStableCodes(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{ErrCallEventsRequestInvalid, http.StatusBadRequest, "REQUEST_INVALID"},
		{ErrCallEventsAgentStale, http.StatusConflict, "AGENT_INSTANCE_STALE"},
		{ErrCallEventsDeviceNotFound, http.StatusNotFound, "CALL_EVENTS_DEVICE_NOT_FOUND"},
		{ErrCallEventsDeviceStale, http.StatusConflict, "CALL_EVENTS_DEVICE_STALE"},
		{ErrCallEventsUnsupported, http.StatusUnprocessableEntity, "CALL_EVENTS_UNSUPPORTED"},
		{ErrCallEventsBackendInvalid, http.StatusServiceUnavailable, "CALL_EVENTS_BACKEND_INVALID"},
		{errors.New("something else"), http.StatusServiceUnavailable, "CALL_EVENTS_UNAVAILABLE"},
	} {
		status, response := classifyCallEventsError(tc.err)
		if status != tc.status || response.Code != tc.code {
			t.Errorf("%v mapped to %d/%s, want %d/%s", tc.err, status, response.Code, tc.status, tc.code)
		}
	}
}

func TestCallEventsClientRevalidatesTheServerReply(t *testing.T) {
	// Neither end trusts the other. A server that skipped its own validation must
	// still not be able to advance a client's cursor incorrectly.
	monitor, generation := callEventsMonitor(t)
	request := callEventsRequest(monitor, generation)
	// A short path on purpose: this platform caps a unix socket address well below
	// what the default temporary directory produces.
	directory, err := os.MkdirTemp("/tmp", "ce")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "a.sock")
	listener, listenErr := net.Listen("unix", socket)
	if err := listenErr; err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, CallEventsResponse{
			ProtocolVersion: ProtocolVersion, AgentInstanceID: request.AgentInstanceID,
			BootID: callEventsBootID, LatestSequence: 9, OldestSequence: 1,
			// Below the requested cursor of 5.
			Events: []CallEvent{{Sequence: 2, Number: "1", ObservedAt: time.Unix(1, 0)}},
		})
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	client, err := NewClient(socket)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.ReadCallEvents(context.Background(), request); err == nil {
		t.Fatal("the client accepted a report that would rewind its own cursor")
	}
}
