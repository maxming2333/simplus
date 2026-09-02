package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/leonfox28/simplus/internal/domain/call"
)

// CallEventCursorFor reads a device's stored position. A missing row is not an
// error: it is the correct starting state for a device never polled before.
func (set *Set) CallEventCursorFor(ctx context.Context, deviceID string) (call.EventCursor, bool, error) {
	if set == nil || set.Calls == nil || deviceID == "" {
		return call.EventCursor{}, false, errors.New("invalid call event cursor request")
	}
	var cursor call.EventCursor
	var sequence int64
	err := set.Calls.QueryRowContext(ctx, `
SELECT boot_id, subscription_fingerprint, last_sequence FROM call_event_cursors WHERE device_id = ?
`, deviceID).Scan(&cursor.BootID, &cursor.SubscriptionFingerprint, &sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return call.EventCursor{}, false, nil
	}
	if err != nil {
		return call.EventCursor{}, false, fmt.Errorf("read call event cursor: %w", err)
	}
	cursor.LastSequence = uint32(sequence)
	return cursor, true, nil
}

// SaveCallEventCursor advances or resets a device's position.
//
// Callers must only reach here after the records for those events are durable.
// A crash between the two re-reads the events, and their stable identity absorbs
// the repeat; saving first would lose them with no way to notice.
func (set *Set) SaveCallEventCursor(ctx context.Context, deviceID string, cursor call.EventCursor, at time.Time) error {
	if set == nil || set.Calls == nil || deviceID == "" {
		return errors.New("invalid call event cursor")
	}
	_, err := set.Calls.ExecContext(ctx, `
INSERT INTO call_event_cursors (device_id, boot_id, subscription_fingerprint, last_sequence, updated_at_unix_ms)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(device_id) DO UPDATE SET
 boot_id = excluded.boot_id,
 subscription_fingerprint = excluded.subscription_fingerprint,
 last_sequence = excluded.last_sequence,
 updated_at_unix_ms = excluded.updated_at_unix_ms
`, deviceID, cursor.BootID, cursor.SubscriptionFingerprint, int64(cursor.LastSequence), at.UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("save call event cursor: %w", err)
	}
	return nil
}

// RecordObservedInboundCall stores one call the modem observed and nobody
// answered.
//
// It does not reuse CreateCall, which deliberately writes an empty end reason and
// leaves the answered and ended times unset: that method begins a call whose
// outcome is still ahead of it. This is the opposite shape — a complete record
// written once, for a call that was never connected and never will be — so it
// gets its own statement rather than a second write to correct the first.
//
// The record is terminal on purpose. Storing it as an active call would make the
// Line look busy, so the next caller would be rejected as a replay conflict, and
// the boot-time reconciliation sweep would rewrite it as a failure on the next
// restart. Neither is true of a notification.
//
// EndedAt is set to the observation. The bridge reports arrival only and nothing
// here ever answers, so this is a point event; setting it preserves the table's
// invariant that an ended call has an ended time, which the state transitions
// establish everywhere else.
func (set *Set) RecordObservedInboundCall(ctx context.Context, value call.Record) (call.Record, bool, error) {
	if set == nil || set.Calls == nil {
		return call.Record{}, false, errors.New("invalid observed inbound call")
	}
	observed := value.CreatedAt.UTC().UnixMilli()
	result, err := set.Calls.ExecContext(ctx, `
INSERT INTO call_records (call_id, operation_id, line_id, remote_address, direction, state, end_reason,
 created_at_unix_ms, updated_at_unix_ms, answered_at_unix_ms, ended_at_unix_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?) ON CONFLICT(operation_id) DO NOTHING
`, value.ID, value.OperationID, value.LineID, value.RemoteAddress, call.DirectionInbound,
		call.StateEnded, value.EndReason, observed, observed, observed)
	if err != nil {
		return call.Record{}, false, fmt.Errorf("record observed inbound call: %w", err)
	}
	changed, _ := result.RowsAffected()
	stored, found, err := set.callByOperation(ctx, value.OperationID)
	if err != nil || !found {
		return call.Record{}, false, fmt.Errorf("read recorded inbound call: %w", err)
	}
	// A repeat of the same event is expected after a crash between persisting and
	// saving the cursor. A repeat that disagrees on the Line, the caller or the
	// direction is not: that is an identity collision, and silently keeping either
	// version would be worse than refusing.
	if changed == 0 && (stored.LineID != value.LineID || stored.RemoteAddress != value.RemoteAddress ||
		stored.Direction != call.DirectionInbound) {
		return call.Record{}, false, call.ErrStateConflict
	}
	return stored, changed == 0, nil
}
