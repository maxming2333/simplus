# Design — Bridged inbound call notifications

## Where each concern lives

| Layer | Addition |
| --- | --- |
| bridge firmware | done: `GET /events/calls`, `bootId`, `oldestSequence` |
| `internal/atremote` | HTTP client for the events endpoint; bounded decode |
| `internal/modemadapter` | nothing — call events are not AT, so no adapter seam |
| `internal/agentapi` | one bounded read operation plus its client/server validation |
| `internal/hardwareprobe` | resolves which device serves the events, reusing the bridge device report |
| `internal/application/calls` | inbound recording path plus cursor state; existing Simulator transitions untouched |
| `internal/storage/sqlite` | cursor per device, and the accumulated lost-call total |
| `internal/api/httpapi` | Line view gains the lost-call observation |

`modemadapter` is deliberately absent. Call events do not pass through
`attransport`, so there is no model-specific command and no adapter to extend.
That also means this path cannot be reached by the AT relay, which is what keeps
`attransport.Query` unreachable from Web/API.

## Why not the AT relay

Rejected: appending synthetic `+CMGL` entries so calls arrive as SMS. It requires
the bridge to forge a valid SMS-DELIVER PDU, allocate a fake storage index that
cannot collide with modem-assigned ones, and intercept reads and deletes for that
index; the fake index also fights the PDU digest that exists to stop a reused
index being deleted as the original message. Fundamentally it turns a transport
into a data source, so upper layers receive data no modem produced.

## Cursor state machine

Persisted per device: `(bootId, lastSequence, lostTotal)`.

```
poll -> response{bootId, latestSequence, oldestSequence, events}

if bootId != stored.bootId:
        stored = {bootId, lastSequence: 0, lostTotal: stored.lostTotal}
        # pre-restart events are gone with the bridge's RAM; resetting is the
        # only correct action, and bootId is what says so explicitly
        raise operator alert "bridge restarted, unread call events were lost"

for event in events where event.sequence > stored.lastSequence, ascending:
        persist inbound call record          # before advancing the cursor
        stored.lastSequence = event.sequence # only after the record is durable

lost = max(0, oldestSequence - (stored.lastSequence + 1))
if lost > 0:
        stored.lostTotal += lost
        raise exactly one operator alert
        # derived from the consumer's own cursor, so a wrapped ring whose entries
        # were all consumed derives zero and a busy line raises no false alert
```

Persist-before-advance is the same ordering the messaging service uses: a crash
between the two re-reads an event and the record's stable identity deduplicates
it, whereas advancing first loses it permanently.

Event identity for deduplication: `(deviceID, bootId, sequence)`. It must include
`bootId`, otherwise sequence 1 from a new boot collides with sequence 1 from the
previous one.

## Lost-call visibility

A derived loss counts calls that really happened and can never be recovered.
The bridge cannot compute it: it does not know what any consumer has read, and
there may be several readers. It reports the oldest sequence it still holds and
each consumer subtracts its own cursor, which is exact per reader and is the same
contract a cursored log uses. A bridge-side overwrite counter would inflate as
soon as the ring wraps, even when every entry had been consumed.

Two consequences drive the shape:

- It must not become a call record. A record with no number and no time would be
  indistinguishable from a real missed call and would corrupt the very data this
  feature exists to produce.
- It must not live only in a log. "Some calls were lost" is an operational fact
  an operator has to act on, not a debugging detail.

So: a Line-level counter with an accumulated total plus the current-boot value,
and one alert per advance. Showing both means a reboot cannot hide history while
the current-boot value still matches what the bridge reports.

## Polling

Reuse the existing two-second `SyncCoordinator` cadence rather than adding a
timer. Its minimum interval already bounds the load, and inbound SMS and inbound
calls share the same "poll the bridge, persist, notify, publish" shape.

One poll is one bounded HTTP round trip through `atremote`, independent of the
AT relay's session machinery: no conversation to scope, because nothing is
serialized against the modem.

## Failure behavior

| Condition | Result |
| --- | --- |
| bridge unreachable | cycle reports the error, cursor unchanged, retried next tick |
| malformed envelope or `bootId` | rejected, cursor unchanged, no record |
| `bootId` changed | cursor reset to zero, one alert, `lostTotal` preserved |
| event Line unresolvable | no record, no alert, cursor still advances so it is not retried forever |
| record persist fails | cursor not advanced, event re-read next tick |
| derived loss non-zero | `lostTotal` increased, exactly one alert |
| `observedAt` zero | receive time used, never 1970 |

The unresolvable-Line row is the one judgement call: advancing the cursor drops
an event that cannot be attributed, which is preferable to blocking every later
event behind it — the same reasoning that made undecodable SMS degrade rather
than stall the batch.

## Testing

- agentapi: malformed envelope, missing `bootId`, oversize event list, both ends.
- cursor: restart replay, duplicate sequences, persist failure, out-of-order
  events, sequence-one collision across two boots.
- lost calls: one alert per advance, no record ever created, total survives a
  reboot while the current-boot value follows the bridge, and a fully consumed
  wrapped ring derives zero so a busy line raises no false alert.
- clock: `observedAt` zero falls back; no 1970 timestamps reach persistence.
- privacy: caller numbers absent from ordinary logs and from alert text.
- opt-in HIL: a real call reaches a Line record; requires the bridge in reach and
  someone to place the call.
