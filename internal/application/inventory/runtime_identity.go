package inventory

import (
	"errors"
	"regexp"
	"strings"

	"github.com/leonfox28/simplus/internal/domain/hardware"
)

var runtimeFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// RuntimeIdentity is what a Line resolves to when an operation has to reach the
// actual hardware behind it: the transport's own name for the device, and the
// identities that pin which equipment and which subscription that operation is
// allowed to touch.
//
// It lives here rather than in any one operation's package because Line-to-
// hardware resolution is a property of the topology, and two definitions of it
// would eventually disagree about which subscription a Line belongs to.
type RuntimeIdentity struct {
	// TransportDeviceID is the device identity the agent protocol uses, which is
	// the Line's physical device with the discovery namespace prefix removed.
	TransportDeviceID string
	// EquipmentFingerprint identifies the modem hardware.
	EquipmentFingerprint string
	// SubscriptionFingerprint identifies the single active profile behind the Line.
	SubscriptionFingerprint string
	// Generation fences re-enumeration of the device.
	Generation uint64
}

// ErrRuntimeIdentityUnavailable means the Line cannot currently be tied to
// identifiable hardware. It is the ordinary state of a Line whose device or SIM is
// absent, not an error in the topology.
var ErrRuntimeIdentityUnavailable = errors.New("runtime Line identity is unavailable")

// ResolveRuntimeIdentity ties a Line to the hardware behind it, and fails closed
// when it cannot.
//
// The single-active-profile requirement is the important one: a Line with more
// than one candidate profile has no unambiguous subscription, and guessing would
// attribute traffic to the wrong one.
func ResolveRuntimeIdentity(topology Topology, line Line) (RuntimeIdentity, error) {
	var equipment string
	for _, device := range topology.Devices {
		if device.ID == line.PhysicalDeviceID {
			equipment = device.EquipmentIdentityFingerprint
			break
		}
	}
	profiles := 0
	var subscription string
	for _, profile := range topology.SubscriptionProfiles {
		if profile.ID != line.SubscriptionProfileID {
			continue
		}
		profiles++
		if profile.State == hardware.ProfileActive {
			subscription = profile.IdentityFingerprint
		}
	}
	if !runtimeFingerprintPattern.MatchString(equipment) || profiles != 1 ||
		!runtimeFingerprintPattern.MatchString(subscription) || line.Generation == 0 {
		return RuntimeIdentity{}, ErrRuntimeIdentityUnavailable
	}
	deviceID := strings.TrimPrefix(line.PhysicalDeviceID, "agent-")
	if deviceID == "" || len(deviceID) > 128 {
		return RuntimeIdentity{}, ErrRuntimeIdentityUnavailable
	}
	return RuntimeIdentity{
		TransportDeviceID:       deviceID,
		EquipmentFingerprint:    equipment,
		SubscriptionFingerprint: subscription,
		Generation:              line.Generation,
	}, nil
}
