package agentapi

import (
	"errors"
	"log/slog"
	"net/http"
)

// registerCallEventsHandler exposes one bounded read. There is no write, no
// answer, no reject and no hangup here: the bridge cannot place or accept calls,
// and this operation exists only so the system can know who called and when.
func registerCallEventsHandler(mux *http.ServeMux, service *CallEventsService, logger *slog.Logger) {
	mux.HandleFunc("POST /v1/calls/events", func(w http.ResponseWriter, r *http.Request) {
		var request CallEventsRequest
		// A read is a POST because it carries identity fingerprints. Those must not
		// travel in a URL, where they would end up in request logs.
		if !decodeSMSRequest(w, r, &request) || validateCallEventsRequest(request) != nil {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Code: "REQUEST_INVALID", Detail: "invalid call events request"})
			return
		}
		response, err := service.Read(r.Context(), request)
		if err != nil {
			writeCallEventsError(w, r, logger, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	})
}

func writeCallEventsError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	status, response := classifyCallEventsError(err)
	// Caller numbers never appear in a log line. Only the device and the outcome
	// do: this feature's whole purpose is to record who called, and a log is not
	// where that record belongs.
	logger.Warn("call events read rejected", "code", response.Code, "path", r.URL.Path)
	writeJSON(w, status, response)
}

func classifyCallEventsError(err error) (int, ErrorResponse) {
	switch {
	case errors.Is(err, ErrCallEventsRequestInvalid):
		return http.StatusBadRequest, ErrorResponse{Code: "REQUEST_INVALID", Detail: "invalid call events request"}
	case errors.Is(err, ErrCallEventsAgentStale):
		return http.StatusConflict, ErrorResponse{Code: "AGENT_INSTANCE_STALE", Detail: "Agent instance changed; refresh before retrying", Retryable: true}
	case errors.Is(err, ErrCallEventsDeviceNotFound):
		return http.StatusNotFound, ErrorResponse{Code: "CALL_EVENTS_DEVICE_NOT_FOUND", Detail: "call events device is not present", Retryable: true}
	case errors.Is(err, ErrCallEventsDeviceStale):
		return http.StatusConflict, ErrorResponse{Code: "CALL_EVENTS_DEVICE_STALE", Detail: "call events device generation changed", Retryable: true}
	case errors.Is(err, ErrCallEventsUnsupported):
		return http.StatusUnprocessableEntity, ErrorResponse{Code: "CALL_EVENTS_UNSUPPORTED", Detail: "call events are unsupported for this device"}
	case errors.Is(err, ErrCallEventsIdentity):
		// A changed identity is not retryable at this cursor: the events still in
		// the bridge's ring arrived under the previous subscription, and attributing
		// them to the current one would invent history.
		return http.StatusConflict, ErrorResponse{Code: "CALL_EVENTS_IDENTITY_CHANGED", Detail: "call events identity changed"}
	case errors.Is(err, ErrCallEventsBackendInvalid):
		return http.StatusServiceUnavailable, ErrorResponse{Code: "CALL_EVENTS_BACKEND_INVALID", Detail: "call events backend returned an invalid report", Retryable: true}
	default:
		return http.StatusServiceUnavailable, ErrorResponse{Code: "CALL_EVENTS_UNAVAILABLE", Detail: "call events are unavailable", Retryable: true}
	}
}
