package server

import (
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

type webEgressObservation struct {
	protocol.EgressObservation
	Stale bool `json:"stale"`
}

type webPublicEgress struct {
	Version   int                  `json:"version"`
	Supported bool                 `json:"supported"`
	Epoch     string               `json:"epoch"`
	Sequence  string               `json:"sequence"`
	ServerNow int64                `json:"server_now"`
	IPv4      webEgressObservation `json:"ipv4"`
	IPv6      webEgressObservation `json:"ipv6"`
}

func newWebPublicEgress(p storage.EgressState, offline bool, now int64) webPublicEgress {
	observation := func(o protocol.EgressObservation, family string) webEgressObservation {
		if o.Family == "" {
			o = protocol.UnknownEgress(family, "not_reported")
		}
		return webEgressObservation{EgressObservation: o, Stale: offline || o.CheckedAt <= 0 || o.ValidUntil <= now}
	}
	return webPublicEgress{Version: 1, Supported: p.Supported, Epoch: p.Epoch, Sequence: p.Sequence, ServerNow: now, IPv4: observation(p.IPv4, "ipv4"), IPv6: observation(p.IPv6, "ipv6")}
}
