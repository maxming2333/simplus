# Implement — Remote HTTP AT transport

Ordered checklist. Each step ends with a runnable validation.

## 1. `internal/atremote` locator + config

- [ ] `locator.go`: `EndpointScheme`, `keyPattern`, `Locator(key) string`,
      `ParseLocator(string) (key string, ok bool)`.
- [ ] `config.go`: `File`, `Bridge`, `LoadConfig(path) (Config, error)` with the
      full validation matrix from `design.md`; `Config.Targets()`,
      `Config.Specs()`.
- [ ] `locator_test.go`, `config_test.go` covering A11 and locator round-trip.

Validate: `go test ./internal/atremote`

## 2. `internal/atremote` opener + session

- [ ] `session.go`: `session` type, `Query` bounds, wire request/response types,
      `normalizeLines` + `safeText`, terminal-status rule, buffer zeroing.
- [ ] `opener.go`: `Target`, `Options`, `NewOpener`, `Open` with the status
      matrix, `Close` best-effort DELETE.
- [ ] `opener_test.go` / `session_test.go` against `httptest.Server` covering
      A3, A4, A5, A6.
- [ ] `sourcescan_test.go`: assert no AT literal in the package (A2) and that
      `EndpointScheme` is referenced only from `internal/atremote` and
      `cmd/simplus-agent` (R3 checkability).

Validate: `go test ./internal/atremote`

## 3. `internal/atremote` routing

- [ ] `routing.go`: `NewRoutingOpener(bridge, local attransport.Opener)`.
      Bridge-scheme locator -> bridge opener; everything else -> local opener;
      nil member for the selected side -> `OpenUnsupported`; never fall back.
- [ ] `routing_test.go` covering A7 including the no-fallback assertion.

Validate: `go test ./internal/atremote`

## 4. `hardwareprobe` seams

- [ ] `at_runtime.go`: add `NewATQuerierWithOpener(attransport.Opener,
      IdentityPseudonymizer) ModemQuerier`.
- [ ] `scanner.go`: add `ExtraDevices func(context.Context)
      ([]agentapi.DeviceReport, error)`; `Scan()` appends, rejects duplicate
      IDs, re-sorts. Nil hook = unchanged path.
- [ ] `bridge_source.go`: `BridgeSpec`, `BridgeDeviceSource`,
      `NewBridgeDeviceSource(registry, specs, locator)`, `Devices(ctx)`, the
      adapter-driven interface-number search, and the evidence policy.
- [ ] `bridge_source_test.go` covering A8, A9, A10, determinism, unknown
      profile, and a profile that accepts no synthesized endpoint.
- [ ] `scanner_extra_test.go` covering duplicate-ID rejection and nil-hook
      parity.

Validate: `go test ./internal/hardwareprobe`

## 5. Agent assembly

- [ ] `cmd/simplus-agent/main.go`: `-remote-at-config` flag +
      `SIMPLUS_AGENT_REMOTE_AT_CONFIG`, absolute-path validation, load, assemble
      routing opener / querier / `ExtraDevices`, log one line per bridge and a
      warning per attested bridge and per plaintext bridge.
- [ ] Keep every existing early-exit and ordering intact; absent config must not
      change any existing statement's behavior.

Validate: `go build ./...` and `go test ./cmd/...`

## 6. Deployment + contract test

- [ ] `containers/compose.remote-at.yaml` override.
- [ ] `internal/containercontract/remote_at_test.go` asserting the override adds
      only a network plus a read-only config bind, and no `privileged`, host
      network, or new capability.

Validate: `go test ./internal/containercontract`

## 7. Documentation

- [ ] `docs/architecture.md`: transport seam + deterministic routing rule.
- [ ] `docs/compatibility.md`: bridged path at its true evidence level, with the
      operator-attestation exception stated explicitly.
- [ ] `docs/remote-at-bridge.md`: the wire contract for firmware authors.

Validate: `make check-docs`

## 8. Full verification

- [ ] `make check-format`
- [ ] `make lint`
- [ ] `make test`

## Review gates

- After step 3: the transport is self-contained and provably AT-literal-free.
- After step 4: fail-closed evidence policy is proven by test before any
  assembly can reach it.
- After step 6: base compose isolation is proven unchanged.

## Rollback points

Each step is a separate commit. Reverting step 5 alone disables the feature
without touching the local path; reverting steps 1-4 removes the packages
entirely because nothing else references them.
