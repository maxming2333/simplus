# Remote HTTP AT transport for bridged modems

## Problem

`simplus-agent` can only reach a modem through a local Linux tty
(`internal/attransport/session_linux.go` hard-validates `filepath.IsAbs`). The
modem therefore has to be plugged into the same host that runs the Agent.

Operators exist who already have an ML307A wired to an ESP32-C3 that exposes the
module's AT UART over an authenticated HTTP endpoint on the LAN. Those modules
are Simplus-supported silicon (`internal/modemadapter/ml307a.go`), but they are
unreachable because there is no non-tty control transport.

## Goal

Let one Agent instance additionally drive reviewed remote AT bridges over HTTP,
without weakening any current boundary:

- the local USB/tty path keeps its exact current behavior when the feature is
  off;
- adapters keep owning every AT literal; the new transport contains none;
- nothing above `attransport` learns whether a given control endpoint is local
  or bridged, and nothing above it branches on the difference;
- capability evidence stays honest: bounded-HIL evidence collected on USB does
  not silently become evidence for a bridged path.

## Non-Goals

- MQTT or any broker-based transport. Rejected: `attransport.Session.Query` is
  request/response, the transport never consumes URCs (it flushes input before
  every write), and SIM AKA needs session stickiness that HTTP expresses
  natively. A broker would add a container and a hand-rolled session/ordering
  layer for no gain.
- A public API or Web surface for sending AT commands. `attransport.Query`
  remains a compiled-in adapter seam.
- Changing the base `compose.yaml` Agent isolation (`network_mode: none`).
- Making bridged devices reachable by the QDC507 SMS driver, which owns its own
  tty transport (`internal/modemadapter/qdc507sms/tty_transport.go`).
- Firmware work in the ESP32 repository. Only the wire contract is specified
  here; the firmware side is a separate deliverable.

## Requirements

### R1 — Bridged control transport

A new `internal/atremote` package implements `attransport.Opener` /
`attransport.Session` over HTTP against a bridge that owns one modem UART.

- Same bounds as the tty session: command non-empty, `<= 1024` bytes, no CR/LF;
  response body bounded to `8192` bytes.
- Same terminal-status rule: return lines only once
  `attransport.HasTerminalResponse` is satisfied, otherwise fail.
- Same line normalization: strip control characters, trim, drop empty lines and
  the echoed command.
- Failures map onto the existing `attransport.OpenError` kinds so
  `probeOpenFailure` keeps producing the current typed probe errors.
- Zero AT literals in the package.
- Secrets (credentials, request/response buffers) are zeroed or never retained.

### R2 — Session stickiness

SIM AKA runs `logical-channel open -> APDU exchange -> close` and requires all
three to reach the same UART session. The bridge protocol therefore has explicit
open/command/close operations with a server-issued session token, and one
`attransport.Session` maps 1:1 to one bridge session.

### R3 — Deterministic routing, never fallback

One Agent must serve local and bridged endpoints simultaneously. Routing is
decided only by the endpoint locator: a locator carrying the bridge scheme goes
to the HTTP opener, anything else goes to the platform tty opener. Neither
opener is ever tried as a fallback for the other.

### R4 — Bridged devices in inventory

`hardwareprobe.Scanner` gains one injected extra-device source so reviewed
bridges appear as normal `agentapi.DeviceReport` entries with a stable ID,
stable content (so the monitor does not churn generations), and a control
endpoint the model adapter accepts as its primary AT role.

The interface number the adapter expects is discovered by asking the adapter,
not hardcoded, so no model fact leaks into the source.

### R5 — Honest capability evidence

A bridge has no bounded-HIL evidence. By default every `observed` capability on
a bridged device is downgraded to `unverified` with evidence naming the reason.
Because `inventory/agent_source.go` maps only `observed` evidence to business
capabilities, the default is fail-closed: the device is visible but not
operable, and `hardwareprobe/simaka.go` refuses SIM AKA.

An operator may set one explicit per-bridge attestation flag to keep the
adapter's evidence. That is a recorded exception, must be logged at startup, and
must be documented as operator-attested rather than HIL-observed.

### R6 — Configuration

Bridges are configured through one private JSON file, not flags, because
credentials on a command line are world-readable via `ps`. The file must be a
regular file with no group/other permissions. Absent configuration means the
feature is off and the Agent behaves exactly as today.

### R7 — Deployment

The base `compose.yaml` keeps `network_mode: none` for the Agent. Reaching a LAN
bridge requires an explicit, separately reviewed Compose override that grants
the Agent a network. No new image or broker container is needed.

### R8 — Documentation

`docs/compatibility.md` records the bridged path at its real evidence level.
`docs/architecture.md` records the transport seam and the routing rule.

## Acceptance Criteria

| # | Criterion |
| --- | --- |
| A1 | With no bridge configuration, `simplus-agent` behavior, snapshot content, and probe results are byte-identical to before the change |
| A2 | `internal/atremote` contains no AT command literal; a source scan test asserts it |
| A3 | Bridge session rejects commands that are empty, over 1024 bytes, or contain CR/LF, without issuing a request |
| A4 | Bridge session rejects a response body over 8192 bytes and a response with no terminal status |
| A5 | Open failures map to `OpenBusy` / `OpenPermission` / `OpenUnavailable` / `OpenConfigure` / `OpenUnsupported` per an asserted status matrix |
| A6 | One `attransport.Session` uses exactly one bridge session token for every command and closes it on `Close()` |
| A7 | The routing opener sends bridge locators to the HTTP opener and every other locator to the tty opener, and returns an error instead of falling back when the selected opener fails |
| A8 | A configured bridge appears in `Scan()` output with a stable ID and an endpoint that `ML307A.Endpoint(device, EndpointPrimaryAT)` resolves; repeated scans produce identical reports |
| A9 | Without the attestation flag, every capability on a bridged device is at most `unverified`, and `Scanner.AuthenticateSIMAKA` returns `ErrSIMAKAUnsupported` |
| A10 | With the attestation flag, adapter evidence is preserved and marked operator-attested |
| A11 | Configuration parsing rejects a bad key, non-http(s) URL, URL with userinfo/query/fragment, unknown profile, duplicate key, group/other-readable file, and symlink |
| A12 | `go test ./...`, `make lint`, `make check-format` pass; `go test ./internal/containercontract` still asserts the unchanged base compose Agent isolation |
| A13 | `docs/compatibility.md` and `docs/architecture.md` describe the bridged path and its evidence level |
