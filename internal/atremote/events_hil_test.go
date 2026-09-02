package atremote

import (
	"context"
	"os"
	"testing"
)

// TestCallEventsAgainstRealBridge reads a real bridge's call ring through the
// production client.
//
// It exists because the client was written against the firmware's source rather
// than its output, and one detail cannot be checked any other way: decoding
// rejects unknown fields, so a bridge that emits a field this build does not model
// would refuse every report — and it would do so silently from the operator's point
// of view, since the symptom is only that call notifications never appear.
//
// Opt-in. Set SIMPLUS_REMOTE_AT_HIL_CONFIG to a private bridge configuration file.
func TestCallEventsAgainstRealBridge(t *testing.T) {
	configPath := os.Getenv("SIMPLUS_REMOTE_AT_HIL_CONFIG")
	if configPath == "" {
		t.Skip("set SIMPLUS_REMOTE_AT_HIL_CONFIG to run the bridge call events check")
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	opener, err := NewOpener(config.Targets())
	if err != nil {
		t.Fatalf("NewOpener: %v", err)
	}
	for _, bridge := range config.Bridges {
		t.Run(bridge.Target.Key, func(t *testing.T) {
			endpoint := Locator(bridge.Target.Key)
			report, err := opener.CallEvents(context.Background(), endpoint, 0)
			if err != nil {
				t.Fatalf("CallEvents: %v", err)
			}
			// A boot identifier of the expected shape proves the envelope decoded and
			// validated, which is the part that would fail on an unmodelled field.
			if !bootIDPattern.MatchString(report.BootID) {
				t.Fatalf("bootId = %q, want 16 lowercase hexadecimal characters", report.BootID)
			}
			if report.OldestSequence > report.LatestSequence {
				t.Fatalf("oldest %d is beyond latest %d", report.OldestSequence, report.LatestSequence)
			}
			t.Logf("bootId=%s oldest=%d latest=%d uptimeMs=%d events=%d",
				report.BootID, report.OldestSequence, report.LatestSequence, report.UptimeMs, len(report.Events))

			// Reading past everything the bridge holds must be empty rather than an
			// error: that is the steady state between calls, and the sweep performs it
			// every two seconds.
			drained, err := opener.CallEvents(context.Background(), endpoint, report.LatestSequence)
			if err != nil {
				t.Fatalf("read past the newest entry: %v", err)
			}
			if len(drained.Events) != 0 {
				t.Fatalf("reading past sequence %d returned %d events", report.LatestSequence, len(drained.Events))
			}
			if drained.BootID != report.BootID {
				t.Fatalf("boot identifier changed between reads: %q then %q", report.BootID, drained.BootID)
			}
			// The identifier must be stable within a run, since the consumer resets its
			// cursor whenever it changes. A per-request value would reset it forever.
			t.Logf("boot identifier stable across reads: %s", drained.BootID)
		})
	}
}
