package call

// ReasonNotAnswered is the end reason of a call the modem observed and nothing
// answered. Every observed inbound call has it: the bridge reports arrival only
// and this system never answers, so a notification is by construction a call that
// was not taken.
const ReasonNotAnswered = "CALL_NOT_ANSWERED"

// EventCursor is a durable read position in one bridge's ring of observed calls.
//
// It is stored with the two facts that scope it rather than as a bare sequence.
// Sequences restart at zero when a bridge restarts, so a position carried across
// one would never match another event again. And the ring outlives a SIM change,
// so entries still held arrived under the previous subscription and must be
// skipped rather than attributed to the current one.
type EventCursor struct {
	BootID                  string
	SubscriptionFingerprint string
	LastSequence            uint32
}

// Scopes reports whether this position was recorded under the same boot and
// subscription as the ones given, and so whether its sequence still means
// anything.
func (cursor EventCursor) Scopes(bootID, subscriptionFingerprint string) bool {
	return cursor.BootID == bootID && cursor.SubscriptionFingerprint == subscriptionFingerprint
}
