package messaging

import (
	"fmt"

	"github.com/leonfox28/simplus/internal/application/inventory"
)

type runtimeLine struct {
	line                    inventory.Line
	transportDeviceID       string
	equipmentFingerprint    string
	subscriptionFingerprint string
}

// resolveRuntimeLine ties a Line to the hardware behind it. The resolution itself
// lives in the inventory package, which owns the topology: two definitions of
// which subscription a Line belongs to would eventually disagree.
func resolveRuntimeLine(topology inventory.Topology, line inventory.Line) (runtimeLine, error) {
	identity, err := inventory.ResolveRuntimeIdentity(topology, line)
	if err != nil {
		return runtimeLine{}, fmt.Errorf("SMS %w", err)
	}
	return runtimeLine{
		line: line, transportDeviceID: identity.TransportDeviceID,
		equipmentFingerprint:    identity.EquipmentFingerprint,
		subscriptionFingerprint: identity.SubscriptionFingerprint,
	}, nil
}

func (target runtimeLine) sendCommand(command SendSMSCommand) SendSMSCommand {
	command.PhysicalDeviceID = target.transportDeviceID
	command.DeviceGeneration = target.line.Generation
	command.ExpectedEquipmentFingerprint = target.equipmentFingerprint
	command.ExpectedSubscriptionFingerprint = target.subscriptionFingerprint
	return command
}

func (target runtimeLine) inboxTarget() InboxTarget {
	deviceID := target.transportDeviceID
	if deviceID == "" {
		deviceID = target.line.PhysicalDeviceID
	}
	return InboxTarget{LineID: target.line.ID, PhysicalDeviceID: deviceID, DeviceGeneration: target.line.Generation,
		ExpectedEquipmentFingerprint: target.equipmentFingerprint, ExpectedSubscriptionFingerprint: target.subscriptionFingerprint}
}
