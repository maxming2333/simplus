# Bridged inbound call notifications

## Problem

An operator wants Simplus to know **who called and when**. Nothing more: no
answering, no media, no call control.

Nothing feeds real inbound call events into Simplus today. `RING` and `+CLIP`
are real-time unsolicited reports with no storage anywhere — `EF_SMS` holds only
messages — so unlike SMS they cannot be re-read from the modem afterwards. The
bridge firmware is the only component that can observe them.

The bridge side is done and verified on hardware
(`esp32-sms-forwarding`, branch `feat/remote-at-bridge-endpoints`):

```
GET {base}/events/calls?after=<seq>&limit=<n>
 -> {"bootId":"b4c1bc19…","latestSequence":1,"dropped":0,"uptimeMs":39460,
     "events":[{"sequence":1,"number":"15817320262",
                "observedAt":1788366807,"observedMs":1015444}]}
```

The receiving plumbing also already exists and must be reused, not rebuilt:

- `internal/domain/call`: `DirectionInbound`, `StateIncoming`, `Record`
- `internal/application/realtime`: `TopicCalls`, `AttentionCallIncoming` — one of
  only two attention kinds the hub permits

What is missing is the path between them.

## Goal

Surface bridged inbound calls as ordinary Simplus observations, and make lost
events visible instead of silent.

## Non-Goals

- Answering, hanging up, call media, or any call state beyond "this call
  arrived". `internal/application/calls` and its Simulator-only transitions stay
  untouched.
- Promoting `CellularVoice` or `DigitalVoiceMedia`. Both stay hardcoded false in
  `mapAgentCapabilities`; this feature is notification, not voice capability.
- Reading call events over the AT relay. They are not AT traffic and must not be
  disguised as stored SMS — see `docs/decisions/0028` reasoning and
  `docs/remote-at-bridge.md`.

## Requirements

### R1 — Bounded Agent read operation

`internal/agentapi` gains one bounded read operation for call events, alongside
the existing hardware operations. It carries the bridge's `bootId`, cursor,
`latestSequence`, `dropped`, and the event list. Both client and server validate
the envelope, as every other agentapi operation does.

### R2 — Cursor safety across bridge restarts

The consumer persists `(bootId, lastSequence)` per device. When `bootId` changes
it **must** reset the cursor to zero. Events unread before a bridge restart are
gone with the bridge's RAM and cannot be re-fetched.

Getting this wrong produces the worst available failure mode: the endpoint
responds normally, both sides log normally, and call notifications simply stop
forever. `uptimeMs` may support the diagnosis but must not be the test, because
it wraps.

### R3 — Inbound call records

Each new event becomes one inbound call record through the existing domain
types, resolved to a Line the same way inbound SMS is. A call whose Line cannot
be resolved is not invented into existence.

The caller address arrives in national format on the verified hardware (eleven
digits, no country code — the same type-of-number behaviour already recorded for
SMS originating addresses). It is carried as observed; no country code is
inferred.

`observedAt` of zero means the bridge's clock was unsynchronised. The consumer
falls back to its own receive time and must not record 1970.

### R4 — Lost events are logged, not silent

Lost calls are derived, not reported: the bridge exposes `oldestSequence` and the
consumer computes `lost = max(0, oldestSequence - (lastSequence + 1))`. A bridge
cannot count overwrites itself, because it does not know what any consumer has
read and there may be several; such a count inflates as soon as the ring wraps
even when every entry was consumed.

A non-zero derived loss gets **one warning log line carrying the count**. That is
the whole requirement.

Rejected: a per-Line observable counter plus an operator alert. Real loss needs
the consumer to be unable to reach the bridge while more than a ring's worth of
calls arrive — with a two-second poll that means dozens of calls inside two
seconds, and the realistic path to it is Simplus being down long enough, which an
operator already knows about. Building a domain field, storage column, API field
and alert channel for that is disproportionate.

What is not acceptable is silence. Fixing the bridge's inflating counter removed
false positives; it did not make real loss impossible. A warning log keeps the
event discoverable at the cost of three lines, and the same applies to a `bootId`
change, which also means unread events were lost.

Loss must **not** become a synthetic call record: a record with no number and no
time is indistinguishable from a real missed call and would corrupt the very data
this feature exists to produce.

### R5 — Notification and realtime

A recorded inbound call publishes on `TopicCalls` with
`AttentionCallIncoming`, and reaches notification channels through the existing
Notification service. No new outbound provider protocol.

## Acceptance Criteria

| # | Criterion |
| --- | --- |
| A1 | With no bridge configured, no call polling occurs and behavior is unchanged |
| A2 | Client and server reject a malformed call-event envelope, including a missing or malformed `bootId` |
| A3 | A `bootId` change resets the persisted cursor to zero, proven by a test that replays a restart |
| A4 | A cursor is never advanced past an event that failed to persist |
| A5 | A replayed poll returning already-seen sequences creates no duplicate record |
| A6 | An event whose Line cannot be resolved produces no record and no alert |
| A7 | `observedAt` zero falls back to receive time; no record carries a 1970 timestamp |
| A8 | A derived loss emits exactly one warning log line carrying the count |
| A9 | A derived loss never produces a call record, and adds no domain, storage or API field |
| A11 | A wrapped ring whose entries were all consumed derives zero loss, so a busy line logs nothing |
| A12 | A `bootId` change emits one warning log line stating unread events were lost |
| A10 | `go test ./...`, `make lint`, `make check-format`, `make check-docs` pass |

## Resolved decision

Whether lost calls needed a per-Line counter and an operator alert: no. The
bridge counter's false positives were the actual problem; once loss is derived
correctly, real loss is rare enough that a warning log is proportionate. The
counter, its storage, its API field and the alert channel are all out of scope.
