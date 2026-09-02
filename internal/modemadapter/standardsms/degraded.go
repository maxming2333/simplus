package standardsms

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/leonfox28/simplus/internal/smscodec"
)

// degradedStoredPDU is one storage entry that arrived but could not be turned
// into message text. It keeps only what is needed to acknowledge the entry and
// to describe the failure; it never keeps recovered content.
type degradedStoredPDU struct {
	stored     StoredPDU
	sender     string
	receivedAt time.Time
	dcs        byte
	part       int
	total      int
	reason     string
}

// degradationClass names the layer a failure came from, so an operator reading
// an inbox can tell "the modem gave us something we cannot parse" from "we
// parsed it but the result is not a usable message".
const (
	degradationPDUStructure = "pdu-structure"
	degradationContent      = "content-validation"
)

// decodeFailureReason describes a PDU that could not be structurally decoded.
//
// The underlying error text is deliberately not embedded. It is written for
// developers, may change between versions, and this string reaches a durable
// record and the operator's inbox. A stable class plus the bounded facts below
// is what makes a later investigation reproducible.
func decodeFailureReason(err error) string {
	if err == nil {
		return degradationPDUStructure
	}
	if errors.Is(err, smscodec.ErrUnsupportedAlphabet) {
		return degradationPDUStructure + "/unsupported-alphabet"
	}
	if strings.Contains(err.Error(), "data coding scheme") {
		return degradationPDUStructure + "/unsupported-coding"
	}
	if strings.Contains(err.Error(), "user-data header") {
		return degradationPDUStructure + "/user-data-header"
	}
	return degradationPDUStructure
}

func contentFailureReason(err error) string {
	if err == nil {
		return degradationContent
	}
	if errors.Is(err, ErrMultipartAmbiguous) {
		return degradationContent + "/multipart"
	}
	return degradationContent
}

// degradedInboundRecord builds a listable, readable, acknowledgeable record for
// an entry that has no usable body.
//
// Fields are populated from whatever survived decoding. When the originating
// address never decoded, the sender is an explicit placeholder rather than an
// empty string: the record must satisfy the same validation as a normal one, and
// an empty sender would be indistinguishable from a bug elsewhere.
//
// When no service-centre timestamp survived, the observation time is used. That
// is not when the network delivered the message, so the body says which one it
// is; silently presenting an observation time as a delivery time would make a
// later investigation draw the wrong conclusion.
func degradedInboundRecord(subscriptionKey string, entry degradedStoredPDU) InboundRecord {
	sender := strings.TrimSpace(entry.sender)
	if sender == "" {
		sender = undecodableSenderPlaceholder
	}
	receivedAt := entry.receivedAt
	timeSource := "service-centre"
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
		timeSource = "observed"
	}
	return InboundRecord{
		MessageID:       degradedMessageID(subscriptionKey, entry),
		SubscriptionKey: subscriptionKey,
		Sender:          sender,
		Body:            degradedBody(entry, timeSource),
		ReceivedAt:      receivedAt.UTC(),
		Segments:        []InboundSegment{{Index: entry.stored.Index, PDUDigest: entry.stored.digestOrHash()}},
	}
}

// degradedBody renders the diagnostic facts. Every value here is metadata about
// the transfer, not payload: an operator can act on it and a developer can
// reproduce from it, without the record carrying content that failed validation.
func degradedBody(entry degradedStoredPDU, timeSource string) string {
	var body strings.Builder
	body.WriteString(undecodableBodyPrefix)
	fmt.Fprintf(&body, " reason=%s storage-index=%d pdu-bytes=%d coding-scheme=0x%02x",
		entry.reason, entry.stored.Index, len(entry.stored.PDU), entry.dcs)
	if entry.total > 1 {
		fmt.Fprintf(&body, " part=%d/%d", entry.part, entry.total)
	}
	fmt.Fprintf(&body, " time-source=%s", timeSource)
	if encoding, err := smscodec.EncodingForDataCodingScheme(entry.dcs); err == nil {
		fmt.Fprintf(&body, " encoding=%s", encoding)
	} else {
		body.WriteString(" encoding=unsupported")
	}
	return body.String()
}

// degradedMessageID is stable for the same storage entry and PDU, so repeated
// listings return the same identity and the application layer deduplicates it
// exactly as it would a normal message.
func degradedMessageID(subscriptionKey string, entry degradedStoredPDU) string {
	digest := sha256.New()
	writeHashField(digest, []byte("degraded-v1"))
	writeHashField(digest, []byte(subscriptionKey))
	var index [8]byte
	binary.BigEndian.PutUint64(index[:], uint64(entry.stored.Index))
	writeHashField(digest, index[:])
	writeHashField(digest, entry.stored.PDU)
	// The prefix matches ordinary inbound identities so both kinds share one
	// namespace and the application layer deduplicates them identically. Its
	// legacy model name is retained deliberately: changing it would change every
	// existing message identity and break durable deduplication.
	return inboundMessageIDPrefix + base64.RawURLEncoding.EncodeToString(digest.Sum(nil))
}

// digestOrHash returns the PDU digest used to fence acknowledgement against a
// reused storage index.
func (stored StoredPDU) digestOrHash() [sha256.Size]byte {
	return sha256.Sum256(stored.PDU)
}
