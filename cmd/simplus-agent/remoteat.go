package main

import (
	"log/slog"

	"github.com/leonfox28/simplus/internal/atremote"
	"github.com/leonfox28/simplus/internal/attransport"
	"github.com/leonfox28/simplus/internal/hardwareprobe"
	"github.com/leonfox28/simplus/internal/modemadapter"
)

// atTransportPlan is the resolved control-transport decision for this process.
//
// Transport selection belongs to the composition root, so this is the only place
// that knows more than one transport exists. It is resolved before the adapter
// registry is built, because a model whose drivers run over the shared seam has
// to be composed with the same opener the prober uses.
type atTransportPlan struct {
	opener  attransport.Opener
	bridges atremote.Config
}

// planATTransport resolves the control transport. With no bridge configuration
// it returns exactly the platform tty opener, so nothing about the existing path
// changes.
//
// Routing is decided by the control-endpoint locator alone and never falls back
// between transports: a bridged endpoint that cannot be opened stays a bridged
// failure.
func planATTransport(configPath string) (atTransportPlan, error) {
	local := attransport.NewOpener()
	if configPath == "" {
		return atTransportPlan{opener: local}, nil
	}
	config, err := atremote.LoadConfig(configPath)
	if err != nil {
		return atTransportPlan{}, err
	}
	bridgeOpener, err := atremote.NewOpener(config.Targets())
	if err != nil {
		return atTransportPlan{}, err
	}
	return atTransportPlan{
		opener:  atremote.NewRoutingOpener(bridgeOpener, local),
		bridges: config,
	}, nil
}

// attachBridgeDevices publishes the configured bridges as ordinary devices.
//
// Failure here is fatal on purpose. A configured bridge that cannot be assembled
// must not degrade into a USB-only Agent that silently lacks the operator's
// expected device.
func (plan atTransportPlan) attachBridgeDevices(
	scanner *hardwareprobe.Scanner,
	registry *modemadapter.Registry,
	logger *slog.Logger,
) error {
	if len(plan.bridges.Bridges) == 0 {
		return nil
	}
	specs := make([]hardwareprobe.BridgeSpec, 0, len(plan.bridges.Bridges))
	for _, bridge := range plan.bridges.Bridges {
		specs = append(specs, hardwareprobe.BridgeSpec{
			Key: bridge.Target.Key, Profile: bridge.Profile, AttestCapabilities: bridge.AttestCapabilities,
		})
	}
	source, err := hardwareprobe.NewBridgeDeviceSource(registry, specs, atremote.Locator)
	if err != nil {
		return err
	}
	scanner.ExtraDevices = source.Devices
	for _, bridge := range plan.bridges.Bridges {
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
