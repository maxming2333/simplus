// Package atremote implements the bounded AT control transport for reviewed
// remote bridges: a small HTTP peer that owns exactly one modem UART and
// exposes open/command/close operations for it.
//
// The package deliberately contains no AT command literal. Command selection
// stays in internal/modemadapter, exactly as it does for the local tty
// transport in internal/attransport. This package only moves already-chosen
// bounded command text to a bridge and returns bounded response lines.
package atremote

import (
	"regexp"
	"strings"
)

// EndpointScheme prefixes every bridge control-endpoint locator. The locator
// travels in agentapi.Endpoint.Node, the same field that carries /dev/ttyUSB2
// for a locally attached modem. It intentionally carries no host, port, path or
// credential: those live only in the opener's reviewed target table, keyed by
// the locator key.
//
// This constant is the single routing fact that distinguishes a bridged control
// endpoint from a local one. It must be referenced only by this package and by
// the executable that assembles the transport; no application, API, protocol or
// adapter layer may branch on it.
const EndpointScheme = "at-bridge:"

var keyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

// ValidKey reports whether a bridge key is usable in a locator.
func ValidKey(key string) bool { return keyPattern.MatchString(key) }

// Locator renders the control-endpoint locator for a bridge key. It returns an
// empty string for an invalid key so a caller cannot synthesize an endpoint
// that no opener will accept.
func Locator(key string) string {
	if !ValidKey(key) {
		return ""
	}
	return EndpointScheme + key
}

// IsLocator reports whether an endpoint string addresses a bridge. It is the
// routing predicate and performs no key validation, so a malformed bridge
// locator is rejected by the bridge opener instead of silently falling through
// to another transport.
func IsLocator(endpoint string) bool {
	return strings.HasPrefix(endpoint, EndpointScheme)
}

// ParseLocator extracts a validated bridge key from a control-endpoint locator.
func ParseLocator(endpoint string) (string, bool) {
	if !IsLocator(endpoint) {
		return "", false
	}
	key := strings.TrimPrefix(endpoint, EndpointScheme)
	if !ValidKey(key) {
		return "", false
	}
	return key, true
}
