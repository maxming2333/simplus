package smscodec

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func deliverPDU(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return decoded
}

// TestEightBitDataIsCarriedNotRejected is the regression guard for the inbox
// stall: an 8-bit message that fails to decode leaves modem storage occupied
// forever and blocks every later message on the same subscription.
func TestEightBitDataIsCarriedNotRejected(t *testing.T) {
	for _, testCase := range []struct {
		name string
		dcs  string
	}{
		{name: "general 8-bit", dcs: "04"},
		{name: "class 0 8-bit", dcs: "14"},
		{name: "data coding group 8-bit", dcs: "F4"},
		{name: "class 1 8-bit", dcs: "15"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			// No SMSC, SMS-DELIVER with UDHI, sender 10086, PID 0, DCS, timestamp,
			// UDL 14: a 7-octet UDH carrying 16-bit ports 2948/9200, then a
			// 7-octet ASCII payload.
			pdu := deliverPDU(t, "00"+"44"+"05"+"81"+"0180F6"+"00"+testCase.dcs+
				"52907090000023"+"0E"+"06"+"05"+"04"+"0B84"+"23F0"+"48656C6C6F2041")
			delivered, err := DecodeDeliverPDU(pdu)
			if err != nil {
				t.Fatalf("8-bit SMS-DELIVER was rejected: %v", err)
			}
			if delivered.Segment.Encoding != EncodingData8Bit {
				t.Fatalf("encoding = %q", delivered.Segment.Encoding)
			}
			if delivered.Segment.DestinationPort != wapPushPort {
				t.Fatalf("destination port = %d", delivered.Segment.DestinationPort)
			}
			text, err := DecodeSegment(delivered.Segment)
			if err != nil {
				t.Fatalf("DecodeSegment: %v", err)
			}
			// The rendering must name the payload class rather than presenting
			// opaque bytes as what the sender wrote.
			if !strings.Contains(text, "WAP push message") {
				t.Fatalf("rendering does not name the payload class: %q", text)
			}
			if !strings.Contains(text, "Hello A") {
				t.Fatalf("readable fragment was not recovered: %q", text)
			}
		})
	}
}

func TestBinaryUserDataDescriptionNamesItsPayloadClass(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		data     BinaryUserData
		expected string
	}{
		{name: "wap push", data: BinaryUserData{DestinationPort: wapPushPort, Length: 40}, expected: "WAP push message"},
		{name: "secure wap push", data: BinaryUserData{DestinationPort: wapPushSecurePort, Length: 40}, expected: "WAP push message"},
		{name: "application port", data: BinaryUserData{DestinationPort: 16999, Length: 8}, expected: "application port 16999 message"},
		{name: "no port", data: BinaryUserData{Length: 8}, expected: "8-bit data message"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			rendered := DescribeBinaryUserData(testCase.data)
			if !strings.Contains(rendered, testCase.expected) {
				t.Fatalf("rendering = %q, want it to contain %q", rendered, testCase.expected)
			}
			if !strings.Contains(rendered, "no readable text") {
				t.Fatalf("rendering does not state that no text was recovered: %q", rendered)
			}
		})
	}
}

func TestPrintableRunScanIgnoresCoincidentalBytes(t *testing.T) {
	// Two-character runs in binary data are coincidence, not text.
	if recovered := scanPrintableRuns([]byte{0x01, 'a', 'b', 0x02, 0x03}); recovered != "" {
		t.Fatalf("short run was reported as text: %q", recovered)
	}
	if recovered := scanPrintableRuns([]byte("\x01\x02" + "订单已发货" + "\x00\x03")); recovered != "订单已发货" {
		t.Fatalf("UTF-8 run was not recovered: %q", recovered)
	}
	if recovered := scanPrintableRuns(nil); recovered != "" {
		t.Fatalf("empty payload = %q", recovered)
	}
	long := strings.Repeat("A", maximumScannedText+64)
	if recovered := scanPrintableRuns([]byte(long)); len(recovered) > maximumScannedText {
		t.Fatalf("recovered text is unbounded: %d bytes", len(recovered))
	}
}

// TestSinglePartHeaderIsNotDecodedAsText covers the operator behaviour the
// bridge firmware documents: a zero-length user-data header emitted purely to
// force UCS-2 alignment. Without header-aware payload slicing those octets are
// rendered as leading control characters in the message body.
func TestSinglePartHeaderIsNotDecodedAsText(t *testing.T) {
	// UDHI set, UDL 8: a zero-length header (one octet) plus one alignment octet,
	// then 6 octets of UCS-2 for three characters.
	pdu := deliverPDU(t, "00"+"44"+"05"+"81"+"0180F6"+"00"+"08"+
		"52907090000023"+"08"+"00"+"00"+"4F60597D554A")
	delivered, err := DecodeDeliverPDU(pdu)
	if err != nil {
		t.Fatalf("aligned UCS-2 SMS-DELIVER was rejected: %v", err)
	}
	text, err := DecodeSegment(delivered.Segment)
	if err != nil {
		t.Fatalf("DecodeSegment: %v", err)
	}
	if text != "你好啊" {
		t.Fatalf("decoded text = %q; header or alignment octets leaked into the body", text)
	}
}

// TestUnknownExtensionCodeBecomesASpace pins 3GPP TS 23.038 section 6.2.1.1: an
// unrecognized escape sequence is displayed as a space. Failing instead would
// degrade an otherwise fully readable message over one unknown code.
func TestUnknownExtensionCodeBecomesASpace(t *testing.T) {
	// Two septets: ESC (0x1b) then 0x30, which no extension table defines.
	// Packed little-endian septets 0x1b,0x30 -> 0x1B,0x18.
	pdu := deliverPDU(t, "00"+"04"+"05"+"81"+"0180F6"+"00"+"00"+"52907090000023"+"02"+"1B18")
	delivered, err := DecodeDeliverPDU(pdu)
	if err != nil {
		t.Fatalf("unknown extension code was rejected: %v", err)
	}
	text, err := DecodeSegment(delivered.Segment)
	if err != nil {
		t.Fatalf("DecodeSegment: %v", err)
	}
	if text != " " {
		t.Fatalf("decoded text = %q, want a single space", text)
	}
}

// TestNationalShiftTableIsRefusedNotMisdecoded covers the alphabets Simplus does
// not implement. Ignoring the header element would decode with the wrong table
// and yield plausible but incorrect text, which a reader cannot detect.
func TestNationalShiftTableIsRefusedNotMisdecoded(t *testing.T) {
	// UDHI set, UDL 6 septets: a 4-octet UDH (length 3) selecting national
	// locking shift table 1, three padding bits, then one packed septet.
	pdu := deliverPDU(t, "00"+"44"+"05"+"81"+"0180F6"+"00"+"00"+"52907090000023"+"06"+"03"+"250101"+"0800")
	if _, err := DecodeDeliverPDU(pdu); !errorsIs(err, ErrUnsupportedAlphabet) {
		t.Fatalf("national shift table error = %v, want ErrUnsupportedAlphabet", err)
	}
	// Identifier 0 is the default alphabet and must remain decodable.
	ok := deliverPDU(t, "00"+"44"+"05"+"81"+"0180F6"+"00"+"00"+"52907090000023"+"06"+"03"+"250100"+"0800")
	if _, err := DecodeDeliverPDU(ok); err != nil {
		t.Fatalf("default-alphabet shift element was rejected: %v", err)
	}
}

func errorsIs(err, target error) bool { return err != nil && errors.Is(err, target) }
