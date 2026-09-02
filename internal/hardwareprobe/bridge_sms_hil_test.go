package hardwareprobe

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/leonfox28/simplus/internal/agentapi"
	"github.com/leonfox28/simplus/internal/atremote"
	"github.com/leonfox28/simplus/internal/attransport"
	"github.com/leonfox28/simplus/internal/modemadapter"
	"github.com/leonfox28/simplus/internal/modemadapter/standardsms"
)

// TestRemoteATBridgeCellularSMSLoopback is the only side-effecting test in this
// package. It submits one real cellular SMS and therefore requires two explicit
// environment variables, not one: a bridge configuration plus the subscriber's
// own number as the destination.
//
// The destination is deliberately the card's own number. A self-loop is the
// smallest real action that proves both directions at once — outbound submission
// and inbound delivery — while reaching no third party. Never point this at
// someone else's number.
//
// It cleans up after itself by acknowledging the delivered message, which is
// what deletes it from modem storage.
//
// Requires the bridge in thin mode: in fat mode the bridge firmware consumes the
// inbound message as an unsolicited report and it never reaches storage.
func TestRemoteATBridgeCellularSMSLoopback(t *testing.T) {
	configPath := os.Getenv("SIMPLUS_REMOTE_AT_HIL_CONFIG")
	destination := os.Getenv("SIMPLUS_REMOTE_AT_HIL_SMS_LOOPBACK")
	if configPath == "" || destination == "" {
		t.Skip("set SIMPLUS_REMOTE_AT_HIL_CONFIG and SIMPLUS_REMOTE_AT_HIL_SMS_LOOPBACK to run the side-effecting SMS loopback")
	}
	if !strings.HasPrefix(destination, "+") {
		t.Fatalf("loopback destination must be explicit E.164")
	}

	config, err := atremote.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load bridge configuration: %v", err)
	}
	bridgeOpener, err := atremote.NewOpener(config.Targets())
	if err != nil {
		t.Fatalf("build bridge opener: %v", err)
	}
	base, err := standardsms.NewOpenerTransport(
		atremote.NewRoutingOpener(bridgeOpener, attransport.NewOpener()),
	)
	if err != nil {
		t.Fatalf("build SMS transport: %v", err)
	}
	// Trace the transcript so a failure reports what the modem actually said
	// instead of only a stable sentinel. Never log a PDU body: it carries sender
	// identity and message text.
	transport := &tracingTransport{inner: base, t: t}
	model := modemadapter.ML307ASMS{}
	driver, err := standardsms.NewDriver(model, transport)
	if err != nil {
		t.Fatalf("build SMS driver: %v", err)
	}
	// A memory store is enough here: durable restart/replay behaviour is proven by
	// the SQLite fixtures. This test exists for the modem and network path.
	adapter, err := standardsms.NewAdapter(model, driver, standardsms.NewMemoryStateStore())
	if err != nil {
		t.Fatalf("build SMS adapter: %v", err)
	}
	registry, err := modemadapter.NewRegistry(model)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	source, err := NewBridgeDeviceSource(registry, bridgeSpecsFor(config), atremote.Locator)
	if err != nil {
		t.Fatalf("build bridge device source: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	devices, err := source.Devices(ctx)
	if err != nil || len(devices) == 0 {
		t.Fatalf("contributed devices: %v", err)
	}
	target := modemadapter.SMSRuntimeTarget{
		Device: devices[0],
		// Any stable 64-hex value satisfies the subscription namespace. The real
		// SIM fingerprint is produced by the Agent, which is not assembled here.
		SubscriptionKey: strings.Repeat("a", 64),
	}

	marker := fmt.Sprintf("simplus bridge loopback %d", time.Now().UnixNano())
	submission, err := adapter.SendSMS(ctx, target, agentapi.SMSSendRequest{
		OperationID: fmt.Sprintf("hil-loopback-%d", time.Now().UnixNano()),
		DeviceID:    target.Device.ID, Destination: destination, Body: marker,
	})
	if err != nil {
		t.Fatalf("outbound submission failed: %v", err)
	}
	t.Logf("outbound submission accepted: submitted_at=%s", submission.SubmittedAt.UTC().Format(time.RFC3339))

	deadline := time.Now().Add(3 * time.Minute)
	for {
		if time.Now().After(deadline) {
			t.Fatal("inbound loopback message did not arrive before the deadline")
		}
		references, listErr := adapter.ListSMS(ctx, target)
		if listErr != nil {
			t.Fatalf("inbound listing failed: %v", listErr)
		}
		for _, reference := range references {
			message, readErr := adapter.ReadSMS(ctx, target, reference.MessageID)
			if readErr != nil {
				t.Fatalf("inbound read failed: %v", readErr)
			}
			if message.Body != marker {
				continue
			}
			// Sender identity is real subscriber data; assert its shape only, never
			// its value. The originating address may legitimately arrive in
			// national format: 3GPP TS 23.040 type-of-number 2 carries no country
			// code, and the codec correctly does not invent one.
			format := "national"
			digits := message.Sender
			if strings.HasPrefix(digits, "+") {
				format, digits = "international", digits[1:]
			}
			if digits == "" || strings.TrimLeft(digits, "0123456789") != "" {
				t.Fatalf("inbound sender is not a plain dialable address")
			}
			t.Logf("inbound delivery confirmed: body matched, sender is a %s-format address of %d digits, received_at=%s",
				format, len(digits), message.ReceivedAt.UTC().Format(time.RFC3339))
			acknowledged, ackErr := adapter.AcknowledgeSMS(ctx, target, agentapi.SMSAcknowledgeRequest{
				OperationID: fmt.Sprintf("hil-loopback-ack-%d", time.Now().UnixNano()),
				DeviceID:    target.Device.ID, MessageID: reference.MessageID,
			})
			if ackErr != nil || !acknowledged {
				t.Fatalf("acknowledge failed: acknowledged=%v err=%v", acknowledged, ackErr)
			}
			remaining, listErr := adapter.ListSMS(ctx, target)
			if listErr != nil {
				t.Fatalf("post-acknowledge listing failed: %v", listErr)
			}
			for _, leftover := range remaining {
				if leftover.MessageID == reference.MessageID {
					t.Fatal("acknowledged message is still pending")
				}
			}
			t.Logf("acknowledge deleted the message from modem storage; %d message(s) still pending", len(remaining))
			return
		}
		time.Sleep(3 * time.Second)
	}
}

// tracingTransport records the AT transcript shape for HIL diagnosis. It
// preserves operation scoping so it exercises the same path as production.
type tracingTransport struct {
	inner standardsms.ScopedTransport
	t     *testing.T
}

func (transport *tracingTransport) log(kind, command string, lines []string, err error) {
	shapes := make([]string, 0, len(lines))
	for _, line := range lines {
		if index := strings.IndexByte(line, ':'); index > 0 {
			shapes = append(shapes, line[:index+1]+"<omitted>")
			continue
		}
		if len(line) > 12 {
			shapes = append(shapes, fmt.Sprintf("<%d-char line>", len(line)))
			continue
		}
		shapes = append(shapes, line)
	}
	transport.t.Logf("  %s %-24s -> %v err=%v", kind, command, shapes, err)
}

func (transport *tracingTransport) Command(ctx context.Context, endpoint, command string, timeout time.Duration) ([]string, error) {
	lines, err := transport.inner.Command(ctx, endpoint, command, timeout)
	transport.log("cmd", command, lines, err)
	return lines, err
}

func (transport *tracingTransport) Prompt(ctx context.Context, endpoint, command string, payload []byte, timeout time.Duration) ([]string, error) {
	lines, err := transport.inner.Prompt(ctx, endpoint, command, payload, timeout)
	transport.log("prompt", command, lines, err)
	return lines, err
}

func (transport *tracingTransport) Begin(endpoint string) (standardsms.Transport, func(), error) {
	bound, release, err := transport.inner.Begin(endpoint)
	if err != nil {
		transport.t.Logf("  begin -> err=%v", err)
		return nil, nil, err
	}
	transport.t.Logf("  begin conversation")
	return &boundTracer{inner: bound, parent: transport}, func() {
		transport.t.Logf("  end conversation")
		release()
	}, nil
}

type boundTracer struct {
	inner  standardsms.Transport
	parent *tracingTransport
}

func (tracer *boundTracer) Command(ctx context.Context, endpoint, command string, timeout time.Duration) ([]string, error) {
	lines, err := tracer.inner.Command(ctx, endpoint, command, timeout)
	tracer.parent.log("cmd", command, lines, err)
	return lines, err
}

func (tracer *boundTracer) Prompt(ctx context.Context, endpoint, command string, payload []byte, timeout time.Duration) ([]string, error) {
	lines, err := tracer.inner.Prompt(ctx, endpoint, command, payload, timeout)
	tracer.parent.log("prompt", command, lines, err)
	return lines, err
}
