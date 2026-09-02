package sqlite

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/leonfox28/simplus/internal/domain/call"
)

const (
	cursorDeviceID   = "bridge-esp32-a"
	cursorBootID     = "0f3a1c2b4d5e6f70"
	cursorSubscriber = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func openTestSet(t *testing.T) *Set {
	t.Helper()
	set, err := OpenSet(t.Context(), filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("OpenSet: %v", err)
	}
	t.Cleanup(func() { _ = set.Close() })
	return set
}

func observedCall(id, operation, number string, at time.Time) call.Record {
	return call.Record{
		ID: id, OperationID: operation, LineID: "line_0123456789012345678901",
		RemoteAddress: number, EndReason: call.ReasonNotAnswered, CreatedAt: at, UpdatedAt: at,
	}
}

func TestCallEventCursorStartsAbsentAndRoundTrips(t *testing.T) {
	set := openTestSet(t)
	ctx := t.Context()
	// A device never polled before has no row, and that is not an error: it is the
	// correct starting state.
	if _, found, err := set.CallEventCursorFor(ctx, cursorDeviceID); err != nil || found {
		t.Fatalf("found = %v, err = %v, want an absent cursor", found, err)
	}
	stored := call.EventCursor{BootID: cursorBootID, SubscriptionFingerprint: cursorSubscriber, LastSequence: 7}
	if err := set.SaveCallEventCursor(ctx, cursorDeviceID, stored, time.Unix(1772505600, 0)); err != nil {
		t.Fatalf("SaveCallEventCursor: %v", err)
	}
	loaded, found, err := set.CallEventCursorFor(ctx, cursorDeviceID)
	if err != nil || !found || loaded != stored {
		t.Fatalf("loaded = %+v found = %v err = %v, want %+v", loaded, found, err, stored)
	}
	// Advancing overwrites in place rather than accumulating rows.
	advanced := call.EventCursor{BootID: cursorBootID, SubscriptionFingerprint: cursorSubscriber, LastSequence: 12}
	if err := set.SaveCallEventCursor(ctx, cursorDeviceID, advanced, time.Unix(1772505700, 0)); err != nil {
		t.Fatalf("advance: %v", err)
	}
	loaded, _, err = set.CallEventCursorFor(ctx, cursorDeviceID)
	if err != nil || loaded != advanced {
		t.Fatalf("loaded = %+v err = %v, want %+v", loaded, err, advanced)
	}
	var rows int
	if err := set.Calls.QueryRowContext(ctx, `SELECT COUNT(*) FROM call_event_cursors`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("cursor rows = %d, want one per device", rows)
	}
}

// TestCallEventCursorIsScopedByBootAndSubscription records why the position is
// stored with both facts rather than as a bare sequence.
func TestCallEventCursorIsScopedByBootAndSubscription(t *testing.T) {
	set := openTestSet(t)
	ctx := t.Context()
	if err := set.SaveCallEventCursor(ctx, cursorDeviceID,
		call.EventCursor{BootID: cursorBootID, SubscriptionFingerprint: cursorSubscriber, LastSequence: 7},
		time.Unix(1772505600, 0)); err != nil {
		t.Fatal(err)
	}
	// A restart resets the sequence to zero, so a stored position from a previous
	// boot must be recognizable as belonging to one.
	restarted := call.EventCursor{BootID: "aabbccddeeff0011", SubscriptionFingerprint: cursorSubscriber}
	if err := set.SaveCallEventCursor(ctx, cursorDeviceID, restarted, time.Unix(1772505700, 0)); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := set.CallEventCursorFor(ctx, cursorDeviceID)
	if err != nil || loaded != restarted {
		t.Fatalf("loaded = %+v, want the reset position %+v", loaded, restarted)
	}
	// A different subscription is likewise a different scope, even at the same boot.
	swapped := call.EventCursor{BootID: "aabbccddeeff0011", SubscriptionFingerprint: "ff" + cursorSubscriber[2:], LastSequence: 3}
	if err := set.SaveCallEventCursor(ctx, cursorDeviceID, swapped, time.Unix(1772505800, 0)); err != nil {
		t.Fatal(err)
	}
	loaded, _, err = set.CallEventCursorFor(ctx, cursorDeviceID)
	if err != nil || loaded != swapped {
		t.Fatalf("loaded = %+v, want %+v", loaded, swapped)
	}
}

func TestCallEventCursorRejectsMalformedScopes(t *testing.T) {
	set := openTestSet(t)
	ctx := t.Context()
	for name, cursor := range map[string]call.EventCursor{
		"short boot id":         {BootID: "0f3a", SubscriptionFingerprint: cursorSubscriber},
		"missing boot id":       {SubscriptionFingerprint: cursorSubscriber},
		"short fingerprint":     {BootID: cursorBootID, SubscriptionFingerprint: "abc"},
		"missing fingerprint":   {BootID: cursorBootID},
		"missing both":          {},
		"long boot id":          {BootID: cursorBootID + "00", SubscriptionFingerprint: cursorSubscriber},
		"overlong fingerprint":  {BootID: cursorBootID, SubscriptionFingerprint: cursorSubscriber + "00"},
		"fingerprint too short": {BootID: cursorBootID, SubscriptionFingerprint: cursorSubscriber[:63]},
	} {
		t.Run(name, func(t *testing.T) {
			if err := set.SaveCallEventCursor(ctx, cursorDeviceID, cursor, time.Unix(1772505600, 0)); err == nil {
				t.Fatal("the store accepted a cursor with no valid scope")
			}
		})
	}
	if err := set.SaveCallEventCursor(ctx, "", call.EventCursor{BootID: cursorBootID, SubscriptionFingerprint: cursorSubscriber}, time.Now()); err == nil {
		t.Fatal("the store accepted a cursor with no device")
	}
}

func TestRecordObservedInboundCallIsTerminalAndComplete(t *testing.T) {
	set := openTestSet(t)
	ctx := t.Context()
	at := time.Unix(1772505600, 0).UTC()
	stored, replayed, err := set.RecordObservedInboundCall(ctx,
		observedCall("call_observed_00000001", "in_call_observed_0000001", "15817320262", at))
	if err != nil || replayed {
		t.Fatalf("RecordObservedInboundCall: replayed = %v err = %v", replayed, err)
	}
	if stored.Direction != call.DirectionInbound {
		t.Fatalf("direction = %q", stored.Direction)
	}
	// Terminal, so the Line does not look busy and the boot reconciliation sweep
	// has nothing to rewrite.
	if stored.State != call.StateEnded {
		t.Fatalf("state = %q, want a terminal record", stored.State)
	}
	// Unlike CreateCall, the reason is written by the same statement rather than
	// left for a second write to fill in.
	if stored.EndReason != call.ReasonNotAnswered {
		t.Fatalf("end reason = %q, want it written with the record", stored.EndReason)
	}
	if stored.AnsweredAt != nil {
		t.Fatal("a call nobody answered was recorded with an answered time")
	}
	if stored.EndedAt == nil || !stored.EndedAt.Equal(at) {
		t.Fatalf("ended at = %v, want the observation so the ended-implies-ended-time invariant holds", stored.EndedAt)
	}
	if !stored.CreatedAt.Equal(at) {
		t.Fatalf("created at = %v, want the observation", stored.CreatedAt)
	}
}

// TestRecordObservedInboundCallAbsorbsARepeatButRefusesACollision is the property
// that makes persist-before-advance safe: a crash between the two re-reads the
// event, and the repeat must be absorbed rather than duplicated.
func TestRecordObservedInboundCallAbsorbsARepeatButRefusesACollision(t *testing.T) {
	set := openTestSet(t)
	ctx := t.Context()
	at := time.Unix(1772505600, 0).UTC()
	original := observedCall("call_observed_00000001", "in_call_observed_0000001", "15817320262", at)
	if _, replayed, err := set.RecordObservedInboundCall(ctx, original); err != nil || replayed {
		t.Fatalf("first write: replayed = %v err = %v", replayed, err)
	}
	repeat := original
	repeat.ID = "call_observed_00000002"
	stored, replayed, err := set.RecordObservedInboundCall(ctx, repeat)
	if err != nil || !replayed {
		t.Fatalf("repeat: replayed = %v err = %v, want it absorbed", replayed, err)
	}
	if stored.ID != original.ID {
		t.Fatalf("the repeat replaced the original record: %q", stored.ID)
	}
	var rows int
	if err := set.Calls.QueryRowContext(ctx, `SELECT COUNT(*) FROM call_records`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("call records = %d, want the repeat absorbed", rows)
	}
	// A repeat that disagrees about who called is an identity collision, not a
	// replay. Silently keeping either version would be worse than refusing.
	collision := original
	collision.RemoteAddress = "13334262200"
	if _, _, err := set.RecordObservedInboundCall(ctx, collision); !errors.Is(err, call.ErrStateConflict) {
		t.Fatalf("err = %v, want a conflict for a colliding identity", err)
	}
}

func TestRecordObservedInboundCallRefusesAnUnusableCaller(t *testing.T) {
	set := openTestSet(t)
	ctx := t.Context()
	at := time.Unix(1772505600, 0).UTC()
	// The column bounds a caller address, so a withheld or placeholder number
	// cannot be stored. The caller has to degrade rather than the store guess.
	for name, number := range map[string]string{
		"withheld":  "",
		"too short": "12",
		"too long":  "1234567890123456789012",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := set.RecordObservedInboundCall(ctx,
				observedCall("call_observed_0000000"+name[:1], "in_call_observed_000000"+name[:1], number, at)); err == nil {
				t.Fatal("the store accepted an unusable caller address")
			}
		})
	}
}

func TestObservedInboundCallsAppearInTheOrdinaryCallHistory(t *testing.T) {
	// The whole point is that the user sees who called and when, so the record has
	// to be visible through the same listing every other call uses.
	set := openTestSet(t)
	ctx := t.Context()
	at := time.Unix(1772505600, 0).UTC()
	if _, _, err := set.RecordObservedInboundCall(ctx,
		observedCall("call_observed_00000001", "in_call_observed_0000001", "15817320262", at)); err != nil {
		t.Fatal(err)
	}
	records, err := set.ListCalls(ctx, 10)
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %#v err = %v", records, err)
	}
	if records[0].RemoteAddress != "15817320262" || records[0].State != call.StateEnded {
		t.Fatalf("record = %+v", records[0])
	}
}
