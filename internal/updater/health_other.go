//go:build !linux

package updater

import (
	"404-probe/internal/protocol"
	"context"
)

type HealthProof struct {
	Nonce  string `json:"nonce"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

func InspectHealth(context.Context, string, uint32) protocol.UpdaterHealth {
	return protocol.UpdaterHealth{Status: "unsupported", Reason: "system_unsupported"}
}
