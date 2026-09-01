package atremote

import (
	"errors"

	"github.com/leonfox28/simplus/internal/attransport"
)

// routingOpener sends every control-endpoint locator to exactly one transport,
// decided only by the locator itself.
//
// This is deterministic routing, not fallback. If the selected transport cannot
// open the endpoint, the error is returned unchanged; the other transport is
// never attempted. Silently switching transports would make the effective
// control path depend on transient availability, which
// .trellis/spec/core/backend/application-boundaries.md forbids.
type routingOpener struct {
	bridge attransport.Opener
	local  attransport.Opener
}

// NewRoutingOpener composes the bridge transport with the platform tty
// transport. Either side may be nil: a locator routed to a missing transport
// fails closed as unsupported instead of being served by the other one.
func NewRoutingOpener(bridge, local attransport.Opener) attransport.Opener {
	return routingOpener{bridge: bridge, local: local}
}

func (opener routingOpener) Open(endpoint string) (attransport.Session, error) {
	if IsLocator(endpoint) {
		if opener.bridge == nil {
			return nil, attransport.NewOpenError(attransport.OpenUnsupported, false, errors.New("remote AT transport is not configured"))
		}
		return opener.bridge.Open(endpoint)
	}
	if opener.local == nil {
		return nil, attransport.NewOpenError(attransport.OpenUnsupported, false, errors.New("local AT transport is not configured"))
	}
	return opener.local.Open(endpoint)
}
