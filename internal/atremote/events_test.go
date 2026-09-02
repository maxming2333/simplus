package atremote

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func bridgeOpener(t *testing.T, handler http.Handler) (*Opener, string) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	target, err := NewTarget("bridge-a", server.URL, "operator", "secret", 5*time.Second)
	if err != nil {
		t.Fatalf("NewTarget: %v", err)
	}
	opener, err := NewOpener([]Target{target})
	if err != nil {
		t.Fatalf("NewOpener: %v", err)
	}
	return opener, Locator("bridge-a")
}

func jsonHandler(body string, record *http.Request) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if record != nil {
			*record = *request
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(body))
	})
}

const validReport = `{"bootId":"0f3a1c2b4d5e6f70","latestSequence":7,"oldestSequence":6,` +
	`"uptimeMs":913442,"events":[{"sequence":6,"number":"15817320262","observedAt":1772505600,"observedMs":900001},` +
	`{"sequence":7,"number":"","observedAt":0,"observedMs":913001}]}`

func TestCallEventsReadsTheRingAndCarriesTheCursorAndBound(t *testing.T) {
	var seen http.Request
	opener, endpoint := bridgeOpener(t, jsonHandler(validReport, &seen))
	snapshot, err := opener.CallEvents(context.Background(), endpoint, 5)
	if err != nil {
		t.Fatalf("CallEvents: %v", err)
	}
	if snapshot.BootID != "0f3a1c2b4d5e6f70" || snapshot.LatestSequence != 7 || snapshot.OldestSequence != 6 {
		t.Fatalf("snapshot envelope = %+v", snapshot)
	}
	if len(snapshot.Events) != 2 || snapshot.Events[0].Sequence != 6 || snapshot.Events[1].Sequence != 7 {
		t.Fatalf("events = %+v", snapshot.Events)
	}
	// A zero wall clock must survive decoding as zero. Turning it into a time
	// here would hide that the bridge did not know when the call arrived.
	if snapshot.Events[1].ObservedAt != 0 {
		t.Fatalf("observedAt = %d, want the unsynchronized marker preserved", snapshot.Events[1].ObservedAt)
	}
	if seen.Method != http.MethodGet {
		t.Fatalf("method = %s, want a read", seen.Method)
	}
	if !strings.HasPrefix(seen.URL.Path, eventsCallsPath) {
		t.Fatalf("path = %s", seen.URL.Path)
	}
	query := seen.URL.Query()
	if query.Get("after") != "5" {
		t.Fatalf("after = %q, want the caller's cursor", query.Get("after"))
	}
	// The limit must be sent: without it a bridge whose ring grew in a later
	// firmware would return more than this build validates.
	if query.Get("limit") != "32" {
		t.Fatalf("limit = %q, want the validated ring size", query.Get("limit"))
	}
	if user, password, ok := seen.BasicAuth(); !ok || user != "operator" || password != "secret" {
		t.Fatal("the read did not carry the reviewed bridge credential")
	}
	// A read must not be framed as a zero-length write.
	if seen.ContentLength > 0 {
		t.Fatalf("ContentLength = %d, want no request body", seen.ContentLength)
	}
}

func TestCallEventsRejectsUnusableReports(t *testing.T) {
	for name, body := range map[string]string{
		"missing bootId":       `{"latestSequence":1,"oldestSequence":1,"uptimeMs":1,"events":[]}`,
		"short bootId":         `{"bootId":"0f3a","latestSequence":1,"oldestSequence":1,"uptimeMs":1,"events":[]}`,
		"uppercase bootId":     `{"bootId":"0F3A1C2B4D5E6F70","latestSequence":1,"oldestSequence":1,"uptimeMs":1,"events":[]}`,
		"unknown field":        `{"bootId":"0f3a1c2b4d5e6f70","latestSequence":1,"oldestSequence":1,"uptimeMs":1,"events":[],"extra":1}`,
		"trailing object":      validReport + `{"bootId":"0f3a1c2b4d5e6f70"}`,
		"oldest beyond latest": `{"bootId":"0f3a1c2b4d5e6f70","latestSequence":1,"oldestSequence":9,"uptimeMs":1,"events":[]}`,
		"sequence beyond latest": `{"bootId":"0f3a1c2b4d5e6f70","latestSequence":2,"oldestSequence":1,"uptimeMs":1,` +
			`"events":[{"sequence":9,"number":"1","observedAt":1,"observedMs":1}]}`,
		"zero sequence": `{"bootId":"0f3a1c2b4d5e6f70","latestSequence":2,"oldestSequence":1,"uptimeMs":1,` +
			`"events":[{"sequence":0,"number":"1","observedAt":1,"observedMs":1}]}`,
		"descending sequences": `{"bootId":"0f3a1c2b4d5e6f70","latestSequence":3,"oldestSequence":1,"uptimeMs":1,` +
			`"events":[{"sequence":3,"number":"1","observedAt":1,"observedMs":1},{"sequence":2,"number":"1","observedAt":1,"observedMs":1}]}`,
		"repeated sequences": `{"bootId":"0f3a1c2b4d5e6f70","latestSequence":3,"oldestSequence":1,"uptimeMs":1,` +
			`"events":[{"sequence":2,"number":"1","observedAt":1,"observedMs":1},{"sequence":2,"number":"1","observedAt":1,"observedMs":1}]}`,
		"oversize number": `{"bootId":"0f3a1c2b4d5e6f70","latestSequence":1,"oldestSequence":1,"uptimeMs":1,` +
			`"events":[{"sequence":1,"number":"` + strings.Repeat("9", 24) + `","observedAt":1,"observedMs":1}]}`,
		"not an object": `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeCallEvents([]byte(body)); !errors.Is(err, ErrCallEventsInvalid) {
				t.Fatalf("err = %v, want the report refused outright", err)
			}
		})
	}
}

func TestCallEventsRejectsMoreEntriesThanTheValidatedRing(t *testing.T) {
	// Truncating an overlong list would silently skip calls, so the whole report
	// is refused instead.
	entries := make([]string, 0, callEventsCapacity+1)
	for index := 1; index <= callEventsCapacity+1; index++ {
		entries = append(entries, `{"sequence":`+itoa(index)+`,"number":"1","observedAt":1,"observedMs":1}`)
	}
	body := `{"bootId":"0f3a1c2b4d5e6f70","latestSequence":99,"oldestSequence":1,"uptimeMs":1,"events":[` +
		strings.Join(entries, ",") + `]}`
	if _, err := decodeCallEvents([]byte(body)); !errors.Is(err, ErrCallEventsInvalid) {
		t.Fatalf("err = %v, want an overlong ring refused", err)
	}
}

func TestCallEventsRefusesUnknownEndpointsWithoutLeakingTheBridgeTable(t *testing.T) {
	opener, _ := bridgeOpener(t, jsonHandler(validReport, nil))
	for name, endpoint := range map[string]string{
		"local tty":     "/dev/ttyUSB0",
		"unknown key":   Locator("bridge-z"),
		"malformed key": EndpointScheme + "Not A Key",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := opener.CallEvents(context.Background(), endpoint, 0)
			if !errors.Is(err, ErrCallEventsUnavailable) {
				t.Fatalf("err = %v, want an unavailable bridge", err)
			}
			if err != nil && strings.Contains(err.Error(), "http") {
				t.Fatalf("error text leaked a bridge address: %v", err)
			}
		})
	}
}

func TestCallEventsTreatsNonSuccessAndOversizeAsAFailedRead(t *testing.T) {
	t.Run("status", func(t *testing.T) {
		opener, endpoint := bridgeOpener(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusServiceUnavailable)
		}))
		if _, err := opener.CallEvents(context.Background(), endpoint, 0); !errors.Is(err, ErrCallEventsFailed) {
			t.Fatalf("err = %v, want a failed read", err)
		}
	})
	t.Run("oversize", func(t *testing.T) {
		opener, endpoint := bridgeOpener(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(strings.Repeat("x", callEventsResponseSize+64)))
		}))
		if _, err := opener.CallEvents(context.Background(), endpoint, 0); !errors.Is(err, ErrCallEventsFailed) {
			t.Fatalf("err = %v, want a failed read", err)
		}
	})
}

// TestCallEventsBoundIsIndependentOfTheTranscriptCeiling guards the reason the
// bound is separate. The transcript ceiling is tuned for storage listings and is
// configurable from 8 KiB to 64 KiB; the call report must be bounded by what the
// call contract needs in both directions, so neither the smallest configured
// bridge fails to deliver a full ring nor the largest gets to return 64 KiB.
func TestCallEventsBoundIsIndependentOfTheTranscriptCeiling(t *testing.T) {
	for name, size := range map[string]int{"smallest": 8192, "largest": maximumBridgeResponseSize} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(jsonHandler(validReport, nil))
			t.Cleanup(server.Close)
			target, err := NewTargetWithOptions("bridge-a", server.URL, "", "", TargetOptions{ResponseSize: size})
			if err != nil {
				t.Fatalf("NewTargetWithOptions: %v", err)
			}
			opener, err := NewOpener([]Target{target})
			if err != nil {
				t.Fatalf("NewOpener: %v", err)
			}
			if _, err := opener.CallEvents(context.Background(), Locator("bridge-a"), 0); err != nil {
				t.Fatalf("a %d byte transcript ceiling changed the call read: %v", size, err)
			}
		})
	}
	// And the events bound applies regardless of how large the transcript
	// ceiling was configured.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(strings.Repeat("x", callEventsResponseSize+64)))
	}))
	t.Cleanup(server.Close)
	target, err := NewTargetWithOptions("bridge-a", server.URL, "", "",
		TargetOptions{ResponseSize: maximumBridgeResponseSize})
	if err != nil {
		t.Fatalf("NewTargetWithOptions: %v", err)
	}
	opener, err := NewOpener([]Target{target})
	if err != nil {
		t.Fatalf("NewOpener: %v", err)
	}
	if _, err := opener.CallEvents(context.Background(), Locator("bridge-a"), 0); !errors.Is(err, ErrCallEventsFailed) {
		t.Fatalf("err = %v, want the events bound applied despite a large transcript ceiling", err)
	}
}

func TestCallEventsFailsClosedWithoutAConfiguredBridge(t *testing.T) {
	var empty *Opener
	if _, err := empty.CallEvents(context.Background(), Locator("bridge-a"), 0); !errors.Is(err, ErrCallEventsUnavailable) {
		t.Fatalf("err = %v, want unavailable", err)
	}
}

func TestCallEventsPathIsAReadNotAnATSurface(t *testing.T) {
	// The events path must stay distinct from every session path: a notification
	// read must never claim the modem UART.
	for _, path := range []string{sessionPath, commandPath, exchangePath} {
		if eventsCallsPath == path {
			t.Fatalf("events path collides with an AT path: %s", path)
		}
	}
	if _, err := url.Parse(eventsCallsPath); err != nil {
		t.Fatalf("events path is not a valid URL path: %v", err)
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 8)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
