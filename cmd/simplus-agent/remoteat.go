package main

import (
	"log/slog"

	"github.com/leonfox28/simplus/internal/atremote"
	"github.com/leonfox28/simplus/internal/attransport"
	"github.com/leonfox28/simplus/internal/hardwareprobe"
	"github.com/leonfox28/simplus/internal/modemadapter"
)

// attachRemoteATBridges assembles the optional bridged control path.
//
// Transport selection belongs to the composition root, so this is the only place
// that knows both transports exist. Routing is decided by the control-endpoint
// locator alone and never falls back between transports: a bridged endpoint that
// cannot be opened stays a bridged failure.
//
// Failure here is fatal on purpose. A configured bridge that cannot be assembled
// must not degrade into a USB-only Agent that silently lacks the operator's
// expected device.
func attachRemoteATBridges(
	scanner *hardwareprobe.Scanner,
	registry *modemadapter.Registry,
	identities hardwareprobe.IdentityPseudonymizer,
	configPath string,
	logger *slog.Logger,
) error {
	config, err := atremote.LoadConfig(configPath)
	if err != nil {
		return err
	}
	bridgeOpener, err := atremote.NewOpener(config.Targets())
	if err != nil {
		return err
	}
	specs := make([]hardwareprobe.BridgeSpec, 0, len(config.Bridges))
	for _, bridge := range config.Bridges {
		specs = append(specs, hardwareprobe.BridgeSpec{
			Key: bridge.Target.Key, Profile: bridge.Profile, AttestCapabilities: bridge.AttestCapabilities,
		})
	}
	source, err := hardwareprobe.NewBridgeDeviceSource(registry, specs, atremote.Locator)
	if err != nil {
		return err
	}
	scanner.Querier = hardwareprobe.NewATQuerierWithOpener(
		atremote.NewRoutingOpener(bridgeOpener, attransport.NewOpener()), identities,
	)
	scanner.ExtraDevices = source.Devices
	for _, bridge := range config.Bridges {
		// Host only: the scheme, path and credential stay out of ordinary logs.
		logger.Info("remote AT bridge registered",
			"bridge", bridge.Target.Key, "profile", bridge.Profile, "host", bridge.Target.BaseURLHost())
		if bridge.Target.Plaintext() {
			logger.Warn("remote AT bridge uses plaintext HTTP; AT traffic and the bridge credential are not confidential in transit",
				"bridge", bridge.Target.Key)
		}
		if bridge.AttestCapabilities {
			logger.Warn("remote AT bridge capabilities are operator-attested, not Simplus bounded HIL evidence",
				"bridge", bridge.Target.Key, "profile", bridge.Profile)
		}
	}
	return nil
}
