package smscodec

import (
	"encoding/binary"
	"errors"
)

// userDataHeader is the tolerant view of a TP-UDH needed on the receive path.
//
// parseConcatenationHeader exists for the send path, where Simplus wrote the
// header and a missing concatenation element is a defect. Inbound headers are
// written by the network and legitimately contain other things: a single-part
// message may carry only application-port addressing, and some operators emit a
// zero-length header purely to force the even byte alignment that UCS-2 content
// needs. Treating either as a decode failure leaves the message in modem storage
// forever, which blocks every later message on the same subscription.
type userDataHeader struct {
	bytes            int
	hasConcatenation bool
	reference        uint16
	total            int
	part             int
	hasPorts         bool
	destinationPort  uint16
	sourcePort       uint16
	shiftTable       bool
}

const (
	udhConcatenation8Bit  = 0x00
	udhPorts8Bit          = 0x04
	udhPorts16Bit         = 0x05
	udhConcatenation16Bit = 0x08

	// National language shift tables (3GPP TS 23.038 Annex A) replace or extend
	// the default alphabet for Turkish, Spanish, Portuguese and ten Indic
	// languages. Simplus implements only the default alphabet and its single
	// extension table.
	//
	// These are detected rather than ignored. Ignoring them would silently decode
	// with the wrong table and produce plausible-looking but incorrect text,
	// which is worse than an explicit failure: a reader cannot tell it happened.
	udhNationalSingleShift  = 0x24
	udhNationalLockingShift = 0x25
)

// ErrUnsupportedAlphabet reports a segment whose user-data header selects a
// national language shift table. Callers surface it as an explicit degraded
// observation instead of presenting mis-decoded text as the message.
var ErrUnsupportedAlphabet = errors.New("SMS uses an unsupported national language alphabet")

// parseUserDataHeader reads every information element it recognizes and ignores
// the rest. It fails only on a structurally impossible header, never on an
// unfamiliar or absent element.
func parseUserDataHeader(data []byte) (userDataHeader, error) {
	if len(data) < 1 {
		return userDataHeader{}, errors.New("SMS user-data header is missing")
	}
	headerBytes := int(data[0]) + 1
	if headerBytes > len(data) {
		return userDataHeader{}, errors.New("SMS user-data header length exceeds the user data")
	}
	header := userDataHeader{bytes: headerBytes, total: 1, part: 1}
	for cursor := 1; cursor < headerBytes; {
		if cursor+2 > headerBytes {
			return userDataHeader{}, errors.New("SMS user-data header has a truncated element")
		}
		identifier, length := data[cursor], int(data[cursor+1])
		cursor += 2
		if cursor+length > headerBytes {
			return userDataHeader{}, errors.New("SMS user-data header has an invalid element length")
		}
		element := data[cursor : cursor+length]
		cursor += length
		switch identifier {
		case udhConcatenation8Bit, udhConcatenation16Bit:
			reference, total, part, err := decodeConcatenationElement(identifier, element)
			if err != nil {
				return userDataHeader{}, err
			}
			if header.hasConcatenation {
				return userDataHeader{}, errors.New("SMS user-data header has more than one concatenation element")
			}
			header.hasConcatenation = true
			header.reference, header.total, header.part = reference, total, part
		case udhPorts8Bit:
			if length != 2 {
				return userDataHeader{}, errors.New("SMS user-data header has an invalid 8-bit port element")
			}
			header.hasPorts = true
			header.destinationPort, header.sourcePort = uint16(element[0]), uint16(element[1])
		case udhNationalSingleShift, udhNationalLockingShift:
			if length != 1 {
				return userDataHeader{}, errors.New("SMS user-data header has an invalid national language element")
			}
			// Identifier 0 selects the default alphabet, which needs no table.
			if element[0] != 0x00 {
				header.shiftTable = true
			}
		case udhPorts16Bit:
			if length != 4 {
				return userDataHeader{}, errors.New("SMS user-data header has an invalid 16-bit port element")
			}
			header.hasPorts = true
			header.destinationPort = binary.BigEndian.Uint16(element[0:2])
			header.sourcePort = binary.BigEndian.Uint16(element[2:4])
		}
	}
	return header, nil
}

func decodeConcatenationElement(identifier byte, element []byte) (uint16, int, int, error) {
	var reference uint16
	var total, part int
	switch identifier {
	case udhConcatenation8Bit:
		if len(element) != 3 {
			return 0, 0, 0, errors.New("SMS user-data header has an invalid 8-bit reference element")
		}
		reference, total, part = uint16(element[0]), int(element[1]), int(element[2])
	case udhConcatenation16Bit:
		if len(element) != 4 {
			return 0, 0, 0, errors.New("SMS user-data header has an invalid 16-bit reference element")
		}
		reference = binary.BigEndian.Uint16(element[0:2])
		total, part = int(element[2]), int(element[3])
	}
	if total < 2 || part < 1 || part > total {
		return 0, 0, 0, errors.New("SMS user-data header has an ambiguous concatenation element")
	}
	return reference, total, part, nil
}
