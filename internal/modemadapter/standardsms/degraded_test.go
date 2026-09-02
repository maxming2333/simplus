package standardsms

import (
	"encoding/hex"
	"strings"
	"testing"
)

func decodeHexFixture(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return decoded
}

func validDeliverPDU(t *testing.T) []byte {
	t.Helper()
	// Sender 10086, GSM7, one septet of content.
	return decodeHexFixture(t, "00"+"04"+"05"+"81"+"0180F6"+"00"+"00"+"52907090000023"+"01"+"41")
}

// TestUndecodablePDUDegradesInsteadOfFailingTheBatch is the regression guard for
// the inbox stall. One unparseable storage entry must not stop the messages
// beside it from being assembled, because a batch failure repeats forever: the
// entry is never acknowledged, so it is never removed.
func TestUndecodablePDUDegradesInsteadOfFailingTheBatch(t *testing.T) {
	stored := []StoredPDU{
		{Index: 1, Status: 0, TPDULength: 10, PDU: []byte{0x00, 0x04, 0xFF, 0xFF}},
		{Index: 2, Status: 0, TPDULength: 12, PDU: validDeliverPDU(t)},
	}
	records, err := assembleInbound(strings.Repeat("a", 64), stored)
	if err != nil {
		t.Fatalf("assembleInbound failed the whole batch: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want the readable message plus one degraded record", len(records))
	}
	var degraded, readable *InboundRecord
	for index := range records {
		if strings.HasPrefix(records[index].Body, undecodableBodyPrefix) {
			degraded = &records[index]
			continue
		}
		readable = &records[index]
	}
	if readable == nil {
		t.Fatal("the readable message beside the bad entry was lost")
	}
	if degraded == nil {
		t.Fatal("the unparseable entry produced no degraded record")
	}
	// A degraded record must be acknowledgeable, which is what drains storage.
	if len(degraded.Segments) != 1 || degraded.Segments[0].Index != 1 {
		t.Fatalf("degraded record does not reference its storage entry: %#v", degraded.Segments)
	}
	if degraded.MessageID == "" || degraded.Sender == "" || degraded.ReceivedAt.IsZero() {
		t.Fatalf("degraded record does not satisfy record validation: %+v", degraded)
	}
	if err := validInboundRecord(*degraded); err != nil {
		t.Fatalf("degraded record is not storable: %v", err)
	}
}

// TestDegradedRecordCarriesDiagnosticsAndNoContent pins what a degraded body may
// and may not contain: enough to reproduce an investigation, and nothing that
// failed validation.
func TestDegradedRecordCarriesDiagnosticsAndNoContent(t *testing.T) {
	stored := []StoredPDU{{Index: 7, Status: 1, TPDULength: 9, PDU: []byte{0x00, 0x04, 0xFF, 0xFF, 0xFF}}}
	records, err := assembleInbound(strings.Repeat("b", 64), stored)
	if err != nil {
		t.Fatalf("assembleInbound: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d", len(records))
	}
	body := records[0].Body
	for _, required := range []string{
		undecodableBodyPrefix, "reason=", "storage-index=7", "pdu-bytes=5", "coding-scheme=0x", "time-source=",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("degraded body is missing %q: %q", required, body)
		}
	}
	// The unknown originating address must be explicit, never empty: an empty
	// sender would be indistinguishable from a defect elsewhere.
	if records[0].Sender != undecodableSenderPlaceholder {
		t.Fatalf("sender = %q, want the explicit placeholder", records[0].Sender)
	}
	if strings.Contains(body, "time-source=service-centre") {
		t.Fatalf("body claims a delivery time it never decoded: %q", body)
	}
}

// TestDegradedRecordIdentityIsStable keeps repeated listings deduplicating, and
// keeps a degraded record distinct from a readable one over the same bytes.
func TestDegradedRecordIdentityIsStable(t *testing.T) {
	key := strings.Repeat("c", 64)
	stored := []StoredPDU{{Index: 3, Status: 0, TPDULength: 9, PDU: []byte{0x00, 0x04, 0xFF}}}
	first, err := assembleInbound(key, stored)
	if err != nil {
		t.Fatalf("assembleInbound: %v", err)
	}
	second, err := assembleInbound(key, stored)
	if err != nil {
		t.Fatalf("assembleInbound: %v", err)
	}
	if first[0].MessageID != second[0].MessageID {
		t.Fatalf("degraded identity is not stable: %q vs %q", first[0].MessageID, second[0].MessageID)
	}
	moved := []StoredPDU{{Index: 4, Status: 0, TPDULength: 9, PDU: []byte{0x00, 0x04, 0xFF}}}
	relocated, err := assembleInbound(key, moved)
	if err != nil {
		t.Fatalf("assembleInbound: %v", err)
	}
	if relocated[0].MessageID == first[0].MessageID {
		t.Fatal("degraded identity ignores the storage index it must acknowledge")
	}
	other, err := assembleInbound(strings.Repeat("d", 64), stored)
	if err != nil {
		t.Fatalf("assembleInbound: %v", err)
	}
	if other[0].MessageID == first[0].MessageID {
		t.Fatal("degraded identity is shared across subscriptions")
	}
}

func TestDegradationReasonNamesItsLayer(t *testing.T) {
	if reason := decodeFailureReason(errUnsupportedCodingFixture{}); !strings.HasPrefix(reason, degradationPDUStructure) {
		t.Fatalf("reason = %q", reason)
	}
	if reason := contentFailureReason(ErrMultipartAmbiguous); !strings.Contains(reason, "multipart") {
		t.Fatalf("reason = %q", reason)
	}
	if reason := decodeFailureReason(nil); reason != degradationPDUStructure {
		t.Fatalf("reason = %q", reason)
	}
}

type errUnsupportedCodingFixture struct{}

func (errUnsupportedCodingFixture) Error() string {
	return "unsupported SMS data coding scheme 0x24"
}
