# Design — Remote HTTP AT transport

## Seam choice

The narrowest existing seam that already means "one exclusive AT conversation"
is `attransport.Opener` / `attransport.Session`:

```go
type Session interface {
    Query(context.Context, string, time.Duration) ([]string, error)
    Close()
}
type Opener interface { Open(string) (Session, error) }
```

Everything above it — `standardat.ExecuteProbe`, every `modemadapter` method,
`hardwareprobe.executeATProbe` — consumes only `attransport.Query`. Implementing
`Opener` therefore reuses the whole probe/SIM-AKA orchestration unchanged. The
rejected alternative was implementing `hardwareprobe.ModemQuerier` directly,
which would require duplicating `executeATProbe`'s identity/presence/serial
ordering.

## Packages and ownership

| Path | Responsibility |
| --- | --- |
| `internal/atremote/locator.go` | The endpoint-locator scheme, key validation, `Locator(key)` and `ParseLocator` |
| `internal/atremote/opener.go` | `Target`, `Opener`, HTTP open/close, `OpenError` mapping |
| `internal/atremote/session.go` | `Query` bounds, wire codec, line normalization, terminal-status rule |
| `internal/atremote/routing.go` | `NewRoutingOpener(bridge, local)` — deterministic prefix routing |
| `internal/atremote/config.go` | Private JSON config file loading and strict validation |
| `internal/hardwareprobe/bridge_source.go` | Turns validated bridge specs into `agentapi.DeviceReport`s (inventory concern, so it stays in `hardwareprobe`) |
| `internal/hardwareprobe/at_runtime.go` | New `NewATQuerierWithOpener` constructor |
| `internal/hardwareprobe/scanner.go` | New `ExtraDevices` hook |
| `cmd/simplus-agent/main.go` | Reads the config file, assembles opener + querier + source |

`atremote` imports `attransport` only. `hardwareprobe/bridge_source.go` imports
`agentapi` + `modemadapter` only; the locator function is injected as
`func(string) string` so the scheme constant stays inside `atremote` and `cmd`.

## Endpoint locator

```
at-bridge:<key>       key matches ^[a-z0-9][a-z0-9-]{0,30}$
```

The locator travels in `agentapi.Endpoint.Node`, exactly where `/dev/ttyUSB2`
travels today. It carries no host, port, credential, or URL: those live only in
the opener's target table, keyed by `<key>`. `agentapi.Endpoint.Node` is already
a transport locator field, so this adds no new class of leak.

Routing is the only place the prefix is interpreted. A source-scan test asserts
the scheme constant is referenced only from `internal/atremote/**` and
`cmd/simplus-agent/**`, which keeps R3's "no layer above transport branches on
local vs bridged" checkable.

## Bridge wire contract

Three operations under a configured base URL. JSON in, JSON out, optional HTTP
Basic auth. The bridge owns exactly one modem UART.

```
POST   {base}/at/session
  ->  200 {"session":"<token>","expiresInMs":30000}
  ->  409 | 423  another session holds the UART
  ->  401 | 403  authentication or authorization failed
  ->  404 | 501  bridge does not implement the contract
  ->  400 | 422  bridge refused the request shape
  ->  5xx        bridge failed transiently

POST   {base}/at/command
  body {"session":"<token>","command":"<one AT line, no CR/LF>","timeoutMs":N}
  ->  200 {"lines":["...","OK"]}
  ->  404 | 410  session token is no longer valid
  ->  504        bridge timed out waiting for a terminal status

DELETE {base}/at/session
  body {"session":"<token>"}
  ->  200 | 204
```

`{base}/at/command` returning lines without a terminal status is an error on the
Simplus side, matching the tty session, which only returns once
`HasTerminalResponse` holds.

The session token exists solely for R2. Without it a second consumer could
interleave an APDU exchange between `logical-channel open` and `close`, which
silently corrupts SIM AKA. The bridge is expected to reject commands carrying a
stale token rather than serve them.

### Status to `OpenError` mapping

| Bridge condition | `Kind` | `Retryable` |
| --- | --- | --- |
| 409, 423 | `OpenBusy` | true |
| 401, 403 | `OpenPermission` | false |
| 400, 422 | `OpenConfigure` | true |
| 404, 501 | `OpenUnsupported` | false |
| 5xx, transport error, timeout, malformed body | `OpenUnavailable` | true |
| unknown key, malformed locator | `OpenUnavailable` | false |

This reuses the existing `probeOpenFailure` switch verbatim, so bridged failures
render as the same typed probe errors operators already see.

## Bounds parity with the tty session

| Rule | tty (`session_linux.go`) | bridge |
| --- | --- | --- |
| command empty / `>1024` / contains CR or LF | rejected before write | rejected before request |
| response size | `maximumResponseSize = 8192` | `io.LimitReader(body, 8192+1)`, over-limit is an error |
| terminal status required | yes | yes |
| line normalization | `splitLines` + `safeText` | identical logic, shared shape |
| context honored | `pollContext` | request context + timeout |
| secret hygiene | `zero(wirePayload)` | request buffer and decoded body zeroed |

`splitLines`/`safeText` are unexported in `attransport` and live behind
`//go:build linux`, so `atremote` carries its own copy rather than exporting
transport internals or dropping the build tag. The duplication is deliberate:
these are transport-local normalization rules, and the parity is pinned by a
test that feeds both shapes the same fixture expectations.

## Inventory synthesis

`Scanner.ExtraDevices func(context.Context) ([]agentapi.DeviceReport, error)`.
`Scan()` runs `scanHardware` first, appends extras, rejects duplicate IDs, and
re-sorts by ID. Nil hook keeps current behavior exactly (A1).

`BridgeDeviceSource` builds each report:

- `ID` = `"bridge-" + key` (distinct namespace from `stableDeviceID`'s `usb-`).
- `PhysicalPath` = `key`. `inventory/agent_source.go` maps this to
  `USBAddress`; a stable opaque address is the closest honest value.
- `Profile` = configured profile, which must resolve in the registry.
- `DisplayName` = adapter's `DisplayName()`.
- `USB` = zero value except `InterfaceCount`. No fabricated VID/PID: the
  registry `Match` path is not used here, `ForProfile` is.
- `Interfaces` = one interface holding one `EndpointTTY` endpoint whose `Node`
  is the locator. The interface number is found by probing candidate numbers
  `0..15` and keeping the first one for which
  `adapter.Endpoint(candidate, EndpointPrimaryAT)` returns that endpoint. If
  none does, the source fails closed with a stable error. This keeps ML307A's
  "interface 2" fact inside `ml307a.go`.
- `Capabilities` = `adapter.Capabilities(device)`, then the evidence policy.

`Generation` stays zero; `agentapi.Monitor` assigns it and hashes reports with
`Generation = 0`, so deterministic synthesis means no generation churn (A8).

### Evidence policy

Default (`attested = false`): every entry whose `Status` is
`agentapi.EvidenceObserved` becomes `agentapi.EvidenceUnverified` with evidence
`["control endpoint is a remote bridge without bounded HIL evidence"]`.
Consequences, all desirable and all pre-existing behavior:

- `inventory/agent_source.go` computes `controlObserved = false`, so the device
  is listed as a physical device with no modem function, SIM slot, or Line;
- `hardwareprobe/simaka.go` `simAKATarget` returns `ErrSIMAKAUnsupported`;
- probing still works, because `probeLocked` does not consult capabilities. An
  operator can therefore verify reachability before attesting anything.

With `attested = true` the adapter's statuses are preserved and each observed
entry gains `"operator-attested remote bridge; not Simplus bounded HIL"`. The
Agent logs one warning per attested bridge at startup. This is the recorded
exception required by `application-boundaries.md`'s model-isolation rule.

## Configuration

`-remote-at-config` / `SIMPLUS_AGENT_REMOTE_AT_CONFIG`, absolute cleaned path.

```json
{
  "bridges": [
    {
      "key": "esp32-a",
      "baseUrl": "http://192.168.10.11",
      "profile": "ml307a",
      "username": "admin",
      "password": "...",
      "requestTimeoutMs": 20000,
      "attestCapabilities": false
    }
  ]
}
```

Validation, all fail-closed with stable errors:

- file must be a regular file, not a symlink, with `mode & 0o077 == 0`;
- file size bounded (64 KiB) before decode; unknown JSON fields rejected;
- `key` matches the locator pattern and is unique;
- `baseUrl` parses, scheme is `http` or `https`, host non-empty, no userinfo, no
  query, no fragment; a plain-`http` bridge is accepted with a startup warning
  because LAN-only deployment is the intended shape;
- `profile` resolves through `registry.ForProfile`;
- `requestTimeoutMs` in `[1000, 120000]`, default 20000;
- at least one bridge if the file is present.

Absent flag/env: the Agent skips the whole assembly, `Scanner.ExtraDevices`
stays nil, and `Scanner.Querier` stays `NewATQuerierWithIdentity` (A1).

## Assembly in `cmd/simplus-agent`

```go
if remoteConfigPath != "" {
    config := atremote.LoadConfig(remoteConfigPath)            // strict
    bridgeOpener := atremote.NewOpener(config.Targets(), ...)
    scanner.Querier = hardwareprobe.NewATQuerierWithOpener(
        atremote.NewRoutingOpener(bridgeOpener, attransport.NewOpener()),
        identityKeyring,
    )
    source := hardwareprobe.NewBridgeDeviceSource(registry, config.Specs(), atremote.Locator)
    scanner.ExtraDevices = source.Devices
}
```

`runtime.GOOS != "linux"` early-exit is unchanged: the Agent stays Linux-only,
because it still owns local USB discovery and the privileged socket contract.

## Deployment

Base `compose.yaml` is untouched, so
`TestComposePreservesThreeProcessPrivilegeBoundaries` keeps passing. A new
`containers/compose.remote-at.yaml` override adds a network and the config bind
to the Agent, used as:

```
docker compose -f compose.yaml -f containers/compose.remote-at.yaml up -d
```

A contract test asserts the override only adds a network plus a read-only config
bind and never introduces `privileged`, host networking, or new capabilities.

## Rejected alternatives

- **MQTT.** See PRD non-goals: request/response seam, no URC consumption, and
  session stickiness all favor HTTP; MQTT adds a broker container plus a
  hand-rolled correlation and ordering layer.
- **Raw TCP AT stream.** `attransport` is not a byte-stream abstraction; a stream
  would reimplement terminal-status framing with no session primitive.
- **Relaxing `filepath.IsAbs` in `session_linux.go`.** That would make one
  transport silently serve two mechanisms, which is exactly the "silently
  switches transports" pattern the spec forbids.
- **Faking `2ecc:3012` on synthetic reports.** Would make `Match` and
  `USBSerialIDs` lie about physical hardware; `ForProfile` needs no descriptor.
- **Hardcoding interface number 2 in the source.** Model fact leak; solved by
  asking the adapter.
