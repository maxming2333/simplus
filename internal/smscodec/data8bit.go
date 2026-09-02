package smscodec

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// EncodingData8Bit is the 3GPP TS 23.038 8-bit data alphabet. It is not text:
// operators use it for WAP Push, MMS notifications, application-port messages
// and OTA provisioning, all of which are common on ordinary consumer SIMs.
//
// Rejecting it is not an option. An undecodable message that stays in modem
// storage blocks every later message on the same subscription, because a batch
// listing fails as a whole and an unacknowledged message is never removed.
// Carrying it as an explicit non-text observation keeps the inbox draining.
const EncodingData8Bit Encoding = "data8bit"

const (
	// wapPushPort and wapPushSecurePort are the WDP ports 3GPP TS 23.040 Annex D
	// assigns to WAP connectionless push, which is how MMS notifications arrive.
	wapPushPort       = 2948
	wapPushSecurePort = 2949

	minimumPrintableRun = 3
	maximumScannedText  = 512
)

// EncodingForDataCodingScheme reports which alphabet a TP-DCS selects. It is
// exported so a caller that must describe an undecodable message can name the
// coding scheme without re-implementing 3GPP TS 23.038.
func EncodingForDataCodingScheme(dcs byte) (Encoding, error) { return encodingForDCS(dcs) }

// BinaryUserData is what can honestly be said about an 8-bit message without
// implementing a WAP stack: which application port it addressed, and whichever
// human-readable fragments its payload contains.
type BinaryUserData struct {
	// DestinationPort is the 16-bit WDP destination port from the user-data
	// header, or zero when the message carried no port addressing.
	DestinationPort uint16
	// Text holds printable fragments recovered from the payload. It is a lossy
	// best effort and never claims to be the message body.
	Text string
	// Length is the payload size in octets.
	Length int
}

// DecodeBinaryUserData interprets one 8-bit segment payload.
//
// The payload is opaque application data, so this deliberately does not parse
// WSP or MMS: it reports the addressed port and recovers printable fragments.
// A caller renders that as an explicit non-text observation rather than
// presenting it as message text.
func DecodeBinaryUserData(segment Segment) (BinaryUserData, error) {
	if segment.Encoding != EncodingData8Bit {
		return BinaryUserData{}, fmt.Errorf("segment encoding %q is not 8-bit data", segment.Encoding)
	}
	payload := segment.UserData
	result := BinaryUserData{Length: len(payload), DestinationPort: segment.DestinationPort}
	result.Text = scanPrintableRuns(payload)
	return result, nil
}

// DescribeBinaryUserData renders a bounded, stable description of an 8-bit
// message for a body field that must contain text.
//
// It names the payload class instead of pretending the bytes are a message,
// because an operator reading an inbox must be able to tell "this is a WAP push
// I cannot render" from "this is what the sender wrote".
func DescribeBinaryUserData(data BinaryUserData) string {
	label := "8-bit data message"
	switch data.DestinationPort {
	case wapPushPort, wapPushSecurePort:
		label = "WAP push message"
	case 0:
	default:
		label = fmt.Sprintf("application port %d message", data.DestinationPort)
	}
	if data.Text == "" {
		return fmt.Sprintf("[%s, %d bytes, no readable text]", label, data.Length)
	}
	return fmt.Sprintf("[%s, %d bytes] %s", label, data.Length, data.Text)
}

// scanPrintableRuns extracts runs of printable characters from binary data.
//
// Both UTF-8 and single-byte runs are recovered because operator payloads mix
// ASCII headers with UTF-8 content. Runs shorter than a few characters are
// dropped: in binary data they are coincidence, not text.
func scanPrintableRuns(payload []byte) string {
	runs := make([]string, 0, 4)
	var current strings.Builder
	flush := func() {
		if utf8.RuneCountInString(current.String()) >= minimumPrintableRun {
			runs = append(runs, strings.TrimSpace(current.String()))
		}
		current.Reset()
	}
	for index := 0; index < len(payload); {
		character, size := utf8.DecodeRune(payload[index:])
		if character == utf8.RuneError && size <= 1 {
			flush()
			index++
			continue
		}
		if character == ' ' || (!unicode.IsControl(character) && unicode.IsPrint(character)) {
			current.WriteRune(character)
			index += size
			continue
		}
		flush()
		index += size
	}
	flush()
	joined := strings.Join(runs, " ")
	if len(joined) > maximumScannedText {
		joined = strings.ToValidUTF8(joined[:maximumScannedText], "")
	}
	return strings.TrimSpace(joined)
}
