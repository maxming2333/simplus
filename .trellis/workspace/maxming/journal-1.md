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
