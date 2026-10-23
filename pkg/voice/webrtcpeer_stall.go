package voice

import (
	"log/slog"
	"net/netip"
	"time"

	"github.com/pion/webrtc/v4"
)

func interfaceClassOf(classes map[string]string, address string) string {
	if ip, err := netip.ParseAddr(address); err == nil {
		address = ip.Unmap().WithZone("").String()
	}
	if class := classes[address]; class != "" {
		return class
	}
	return "unknown"
}

// localDTLSRoleFromAnswer applies Pion's first-media-setup rule to the remote
// answer: a passive peer makes the local side the DTLS client. Anything else is
// reported as unknown rather than guessed.
func localDTLSRoleFromAnswer(desc webrtc.SessionDescription) webrtc.DTLSRole {
	parsed, err := desc.Unmarshal()
	if err != nil {
		return webrtc.DTLSRoleAuto
	}
	for _, media := range parsed.MediaDescriptions {
		for _, attr := range media.Attributes {
			if attr.Key != "setup" {
				continue
			}
			switch attr.Value {
			case "passive":
				return webrtc.DTLSRoleClient
			case "active":
				return webrtc.DTLSRoleServer
			}
			return webrtc.DTLSRoleAuto
		}
	}
	return webrtc.DTLSRoleAuto
}

func (p *Peer) dtlsRoleName() string {
	switch webrtc.DTLSRole(p.localDTLSRole.Load()) {
	case webrtc.DTLSRoleClient:
		return "client"
	case webrtc.DTLSRoleServer:
		return "server"
	default:
		return "unknown"
	}
}

// logDTLSStall records, once per stalled attempt and before the peer is
// closed, where the startup got stuck. It carries enums and counters only:
// never addresses, ports, interface names, credentials or SDP.
//
// It runs on the startup owner's goroutine, never from a Pion callback, because
// the stats calls take locks that callbacks (notably DTLS state changes) hold.
// The packet/byte counters Pion exposes for the selected pair exclude STUN, so
// after ICE connects they are DTLS records: sent counts the local flights and
// their retransmits, received is zero when nothing DTLS came back. They cannot
// prove a received packet was DTLS, only that it was not STUN.
func (p *Peer) logDTLSStall(provider ProviderID, attempt int, began time.Time) {
	iceMS := int64(-1)
	if first := p.startup.firstICEAt(); !first.IsZero() {
		iceMS = first.Sub(began).Milliseconds()
	}
	attrs := []any{"provider", provider, "attempt", attempt, "dtls_role", p.dtlsRoleName(), "ice_connected_ms", iceMS}
	slog.Info("voice startup dtls stall", append(attrs, p.stallPairAttrs(p.startup.firstICEAt())...)...)
}

func (p *Peer) stallPairAttrs(iceConnected time.Time) []any {
	unknown := []any{"pair_known", false}
	// Close takes statsMu around pc.Close, and sets closing first, so a
	// snapshot either runs entirely before Close or is skipped.
	p.statsMu.Lock()
	defer p.statsMu.Unlock()
	p.mu.Lock()
	closing := p.closing
	p.mu.Unlock()
	if closing {
		return unknown
	}
	ice := p.pc.SCTP().Transport().ICETransport()
	// Pion publishes the gatherer after ICE Start; its selected-pair getters
	// read it without locking.
	if ice.State() == webrtc.ICETransportStateNew {
		return unknown
	}
	pair, err := ice.GetSelectedCandidatePair()
	if err != nil || pair == nil || pair.Local == nil || pair.Remote == nil {
		return unknown
	}
	selected, ok := ice.GetSelectedCandidatePairStats()
	if !ok {
		return unknown
	}
	counters := selected
	for _, stat := range p.pc.GetStats() {
		if s, ok := stat.(webrtc.ICECandidatePairStats); ok && s.ID == selected.ID {
			counters = s // the full report adds the STUN timestamps
		}
	}
	local := pair.Local
	address := local.Address
	if local.Typ != webrtc.ICECandidateTypeHost && local.RelatedAddress != "" {
		address = local.RelatedAddress
	}
	return []any{
		"pair_known", true,
		"local_type", local.Typ.String(),
		"local_protocol", local.Protocol.String(),
		"local_interface_class", interfaceClassOf(localDiagnosticInterfaceClasses(), address),
		"remote_type", pair.Remote.Typ.String(),
		"remote_protocol", pair.Remote.Protocol.String(),
		"non_stun_packets_sent", counters.PacketsSent,
		"non_stun_bytes_sent", counters.BytesSent,
		"non_stun_packets_received", counters.PacketsReceived,
		"non_stun_bytes_received", counters.BytesReceived,
		"stun_responses_received", counters.ResponsesReceived,
		"stun_alive", stunAlive(counters, iceConnected),
	}
}

// stunAlive reports whether any STUN response or request arrived after ICE
// connected, i.e. during the stall. Pion pings the selected pair every 2s, well
// inside the 3s stall, so a live path shows at least one such packet. Earlier
// timestamps belong to connectivity checks and say nothing about the stall.
func stunAlive(s webrtc.ICECandidatePairStats, iceConnected time.Time) bool {
	if iceConnected.IsZero() {
		return false
	}
	after := func(n uint64, ts webrtc.StatsTimestamp) bool { return n > 0 && ts.Time().After(iceConnected) }
	return after(s.ResponsesReceived, s.LastResponseTimestamp) ||
		after(s.RequestsReceived, s.LastRequestReceivedTimestamp)
}
