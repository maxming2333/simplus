# Journal - maxming (Part 1)

> AI development session journal
> Started: 2026-09-01

---



## Session 1: Remote HTTP AT transport for bridged modems

**Date**: 2026-09-01
**Task**: Remote HTTP AT transport for bridged modems
**Package**: core
**Branch**: `feat/remote-at-bridge`

### Summary

Added internal/atremote (HTTP implementation of attransport.Opener/Session with tty-parity bounds, OpenError classification, session stickiness for APDU sequences, deterministic no-fallback routing, private-file config), hardwareprobe seams (NewATQuerierWithOpener, Scanner.ExtraDevices, BridgeDeviceSource with adapter-driven interface discovery and fail-closed capability downgrade), simplus-agent assembly behind -remote-at-config, an opt-in Compose overlay that is the only place granting Agent a network, and docs/remote-at-bridge.md. Fixed EF_ICCID BCD pad handling in modemadapter identity parsing, found via real hardware. HIL-0 evidence: a real ML307A-DSLN-MTSH1S00 behind an ESP32 bridge probes to ProbeStateComplete with SIM identity, home operator and signal, and the CCHO/CGLA/CCHC sticky sequence works.

### Git Commits

| Hash | Message |
|------|---------|
| `a8f165b` | (see git log) |

### Status

[OK] **Completed**


## Session 2: Model-agnostic 3GPP cellular SMS + prompted-payload transport

**Date**: 2026-09-02
**Task**: Model-agnostic 3GPP cellular SMS + prompted-payload transport
**Package**: core
**Branch**: `feat/remote-at-bridge`

### Summary

Added attransport.PromptSession/Exchange (tty + bridge) so the 27.005 submit-prompt interaction works over any control transport, with a strict not-dispatched vs uncertain split. Renamed qdc507sms to standardsms and parameterized its model adapter (pure move, proven by the existing 1400-line QDC507 suite). Added standardsms.OpenerTransport over the shared AT seam, modemadapter.ML307ASMS with sms-control deliberately left unverified, and replaced the bridge source's SMSAdapter inference with an adapter-declared LocalTTYAdapter marker. simplus-agent now resolves the transport before composing adapters. Bridged HIL-0: 412/200 prompt classification, full AT+CMGW exchange, and PDU-mode/CPMS/CMGL driven by the production SMS driver.

### Git Commits

| Hash | Message |
|------|---------|
| `1d93a4f` | (see git log) |

### Status

[OK] **Completed**
