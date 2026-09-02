package standardsms

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/leonfox28/simplus/internal/smscodec"
)

var ErrMultipartAmbiguous = errors.New("3GPP multipart SMS is ambiguous")

var (
	errInboundPDUDecode         = errors.New("3GPP inbound SMS PDU structure is invalid")
	errInboundContentValidation = errors.New("3GPP inbound SMS decoded content is invalid")
)

const maxMultipartAssemblySpan = 10 * time.Minute

// undecodableBodyPrefix marks a degraded record: one whose stored PDU could not
// be turned into message text.
//
// A degraded record exists because failing the batch is worse than degrading one
// message. assembleInbound is called for the whole storage listing, so a single
// unparseable PDU used to fail every operation on that subscription, and because
// an unacknowledged message is never deleted the failure repeated forever. One
// bad message blocked every later one.
//
// A degraded record is listable, readable and acknowledgeable like any other, so
// storage drains and the operator both sees that something arrived and can
// remove it. The body carries only diagnostic facts — storage index, payload
// size, coding scheme and a failure reason — never recovered content, because
// content that failed validation must not be presented as if it were the
// message.
const undecodableBodyPrefix = "[undecodable message]"

const undecodableSenderPlaceholder = "unknown"

// inboundMessageIDPrefix carries a legacy model name. It is retained on purpose:
// message identities are persisted and used for deduplication, so renaming it
// would orphan every stored message.
const inboundMessageIDPrefix = "qdc507-in-"

// InboundSegment binds an Agent message to the modem storage entry that must
// be acknowledged. PDUDigest protects a newly reused storage index from being
// deleted as though it still held the original message.
type InboundSegment struct {
	Index         int
	PDUDigest     [sha256.Size]byte
	DeleteStarted bool `json:",omitempty"`
	Deleted       bool
}

// InboundRecord is the minimum state needed to keep list/read stable while a
// multipart acknowledgement is partially complete.
type InboundRecord struct {
	MessageID       string
	SubscriptionKey string
	Sender          string
	Body            string
	ReceivedAt      time.Time
	Segments        []InboundSegment
	Acknowledged    bool
}

type decodedStoredPDU struct {
	stored    StoredPDU
	delivered smscodec.DeliverPDU
	digest    [sha256.Size]byte
}

type multipartKey struct {
	sender    string
	encoding  smscodec.Encoding
	dcs       byte
	reference uint16
	total     int
}

func assembleInbound(subscriptionKey string, stored []StoredPDU) ([]InboundRecord, error) {
	if !subscriptionKeyPattern.MatchString(subscriptionKey) {
		return nil, errors.New("3GPP inbound SMS requires a subscription key")
	}
	seenIndexes := make(map[int]struct{}, len(stored))
	undecodable := make([]degradedStoredPDU, 0)
	singles := make([]decodedStoredPDU, 0, len(stored))
	groups := make(map[multipartKey][]decodedStoredPDU)
	for _, candidate := range stored {
		// CMGL=4 also returns outgoing drafts and submitted messages. Only
		// received-unread and received-read entries belong in the inbox.
		if candidate.Status != 0 && candidate.Status != 1 {
			continue
		}
		if _, duplicate := seenIndexes[candidate.Index]; duplicate {
			return nil, fmt.Errorf("%w: duplicate 3GPP SMS storage index %d", errInboundPDUDecode, candidate.Index)
		}
		seenIndexes[candidate.Index] = struct{}{}
		delivered, err := smscodec.DecodeDeliverPDU(candidate.PDU)
		if err != nil {
			// Degrade instead of failing the batch. See undecodableBodyPrefix.
			undecodable = append(undecodable, degradedStoredPDU{stored: candidate, reason: decodeFailureReason(err)})
			continue
		}
		decoded := decodedStoredPDU{stored: candidate, delivered: delivered, digest: sha256.Sum256(candidate.PDU)}
		if delivered.Segment.Total == 1 {
			singles = append(singles, decoded)
			continue
		}
		key := multipartKey{
			sender: delivered.Sender, encoding: delivered.Segment.Encoding,
			dcs: delivered.DCS, reference: delivered.Segment.Reference, total: delivered.Segment.Total,
		}
		groups[key] = append(groups[key], decoded)
	}

	records := make([]InboundRecord, 0, len(singles)+len(groups)+len(undecodable))
	for _, single := range singles {
		record, err := inboundRecord(subscriptionKey, []decodedStoredPDU{single})
		if err != nil {
			// The PDU parsed but its assembled content failed validation. That is
			// still a message that arrived, so degrade it rather than blocking the
			// batch, and keep the reason for diagnosis.
			undecodable = append(undecodable, degradedStoredPDU{
				stored: single.stored, sender: single.delivered.Sender,
				receivedAt: single.delivered.ReceivedAt, dcs: single.delivered.DCS,
				reason: contentFailureReason(err),
			})
			continue
		}
		records = append(records, record)
	}
	for _, group := range groups {
		parts := make(map[int]struct{}, len(group))
		ambiguous := len(group) != group[0].delivered.Segment.Total
		earliest := group[0].delivered.ReceivedAt
		latest := earliest
		for _, segment := range group {
			if _, duplicate := parts[segment.delivered.Segment.Part]; duplicate {
				ambiguous = true
			}
			parts[segment.delivered.Segment.Part] = struct{}{}
			if segment.delivered.ReceivedAt.Before(earliest) {
				earliest = segment.delivered.ReceivedAt
			}
			if segment.delivered.ReceivedAt.After(latest) {
				latest = segment.delivered.ReceivedAt
			}
		}
		// An incomplete group is expected while segments arrive or after a
		// partial ACK. A duplicate part means the 8-bit reference was reused;
		// both cases are held back rather than risking a mixed message.
		if ambiguous || latest.Sub(earliest) > maxMultipartAssemblySpan {
			continue
		}
		record, err := inboundRecord(subscriptionKey, group)
		if err != nil {
			// A complete group whose assembled content is invalid will never
			// become valid, so holding it back would block the batch forever.
			for _, part := range group {
				undecodable = append(undecodable, degradedStoredPDU{
					stored: part.stored, sender: part.delivered.Sender,
					receivedAt: part.delivered.ReceivedAt, dcs: part.delivered.DCS,
					part: part.delivered.Segment.Part, total: part.delivered.Segment.Total,
					reason: contentFailureReason(err),
				})
			}
			continue
		}
		records = append(records, record)
	}
	for _, entry := range undecodable {
		records = append(records, degradedInboundRecord(subscriptionKey, entry))
	}
	sort.Slice(records, func(left, right int) bool {
		if records[left].ReceivedAt.Equal(records[right].ReceivedAt) {
			return records[left].MessageID < records[right].MessageID
		}
		return records[left].ReceivedAt.Before(records[right].ReceivedAt)
	})
	return records, nil
}

func inboundRecord(subscriptionKey string, decoded []decodedStoredPDU) (InboundRecord, error) {
	sort.Slice(decoded, func(left, right int) bool {
		return decoded[left].delivered.Segment.Part < decoded[right].delivered.Segment.Part
	})
	segments := make([]smscodec.Segment, 0, len(decoded))
	storedSegments := make([]InboundSegment, 0, len(decoded))
	receivedAt := decoded[0].delivered.ReceivedAt
	for _, part := range decoded {
		segments = append(segments, part.delivered.Segment)
		storedSegments = append(storedSegments, InboundSegment{Index: part.stored.Index, PDUDigest: part.digest})
		if part.delivered.ReceivedAt.Before(receivedAt) {
			receivedAt = part.delivered.ReceivedAt
		}
	}
	body, err := smscodec.Decode(segments)
	if err != nil {
		return InboundRecord{}, fmt.Errorf("assemble 3GPP SMS: %w", err)
	}
	if strings.TrimSpace(body) == "" || !utf8.ValidString(body) || utf8.RuneCountInString(body) > 1600 || len(body) > 6400 {
		return InboundRecord{}, errors.New("assembled 3GPP SMS is outside the Agent message limits")
	}
	return InboundRecord{
		MessageID: stableInboundMessageID(subscriptionKey, decoded), SubscriptionKey: subscriptionKey,
		Sender: decoded[0].delivered.Sender, Body: body, ReceivedAt: receivedAt.UTC(), Segments: storedSegments,
	}, nil
}

func stableInboundMessageID(subscriptionKey string, decoded []decodedStoredPDU) string {
	digest := sha256.New()
	writeHashField(digest, []byte(subscriptionKey))
	for _, part := range decoded {
		var index [8]byte
		binary.BigEndian.PutUint64(index[:], uint64(part.stored.Index))
		writeHashField(digest, index[:])
		writeHashField(digest, part.stored.PDU)
	}
	return inboundMessageIDPrefix + base64.RawURLEncoding.EncodeToString(digest.Sum(nil))
}

type hashWriter interface {
	Write([]byte) (int, error)
}

func writeHashField(destination hashWriter, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = destination.Write(length[:])
	_, _ = destination.Write(value)
}
