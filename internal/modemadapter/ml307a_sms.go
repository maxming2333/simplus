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
// shared evidence: the ML307A's own inbound/outbound behaviour on a real network
// has not been accepted yet. sms-control therefore stays unverified, which keeps
// internal/application/inventory from advertising SMS for this model. Promote it
// only when ML307A cellular SMS HIL is actually recorded in
// docs/compatibility.md.
func (adapter ML307ASMS) Capabilities(device agentapi.DeviceReport) []agentapi.CapabilityEvidence {
	capabilities := ML307A{}.Capabilities(device)
	for index := range capabilities {
		if capabilities[index].Capability != "sms-control" {
			continue
		}
		if hasEndpoint(device, agentapi.EndpointTTY, 2) {
			capabilities[index].Status = agentapi.EvidenceUnverified
			capabilities[index].Evidence = []string{
				"standard 3GPP PDU-mode SMS driver is composed and fixture-verified; on-network cellular SMS HIL is pending",
			}
		}
		break
	}
	return capabilities
}
