package modemadapter

import (
	"github.com/leonfox28/simplus/internal/agentapi"
)

// ML307ASMS is the SMS-composed ML307A model surface, mirroring how QDC507SMS
// relates to QDC507: one registry entry keeps discovery, identity/RF probing and
// cellular SMS resolving through the same profile, while the safe default
// registry stays SMS-closed.
//
// It is registered only by a composition root that has actually built the SMS
// driver, durable state and transport. Being registered is not evidence; the
// capability status below is.
type ML307ASMS struct{ ML307A }

var (
	_ SIMAuthAdapter           = ML307ASMS{}
	_ ATProbeAdapter           = ML307ASMS{}
	_ SIMPresenceAdapter       = ML307ASMS{}
	_ SIMIdentityAdapter       = ML307ASMS{}
	_ RFControlAdapter         = ML307ASMS{}
	_ EquipmentIdentityAdapter = ML307ASMS{}
	_ USBSerialBindingAdapter  = ML307ASMS{}
)

func (ML307ASMS) Profile() string { return agentapi.ProfileML307A }

func (ML307ASMS) DisplayName() string { return "China Mobile IoT ML307A" }

func (ML307ASMS) Matches(descriptor USBDescriptor) bool { return ML307A{}.Matches(descriptor) }

// Capabilities re-states the base ML307A evidence and adjusts only sms-control.
//
// The command set is standard 3GPP TS 27.005 and its Go implementation is shared
// with the model that already passed cellular SMS HIL, but shared code is not
// shared evidence. This model's own promotion rests on its own accepted HIL: a
// designated registered SIM completed one full loopback through the composed
// adapter — outbound submit-prompt submission, inbound storage delivery,
// listing, PDU read with a matching unique body, and acknowledgement that
// removed the message from storage.
//
// That HIL ran over a bridged control endpoint, so the evidence text says so.
// A bridged device additionally has its own evidence policy in
// internal/hardwareprobe: without an explicit operator attestation every
// observed status there is downgraded, so this promotion does not by itself make
// a bridged modem operable.
func (adapter ML307ASMS) Capabilities(device agentapi.DeviceReport) []agentapi.CapabilityEvidence {
	capabilities := ML307A{}.Capabilities(device)
	for index := range capabilities {
		if capabilities[index].Capability != "sms-control" {
			continue
		}
		if hasEndpoint(device, agentapi.EndpointTTY, 2) {
			capabilities[index].Status = agentapi.EvidenceObserved
			capabilities[index].Evidence = []string{
				"designated-SIM cellular SMS loopback accepted: submit-prompt submission, storage delivery, PDU read and acknowledged deletion",
			}
		}
		break
	}
	return capabilities
}
