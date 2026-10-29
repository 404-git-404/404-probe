package protocol

// UpdaterHealth is a negotiated, additive observation, never an authorization.
// An absent field retains the legacy capability decision.
type UpdaterHealth struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

func (h *UpdaterHealth) Valid() bool {
	if h == nil {
		return true
	}
	switch h.Status {
	case "healthy", "missing", "unreachable", "identity_mismatch", "unsupported", "unknown":
		return len(h.Reason) <= 64
	}
	return false
}

// Normalize keeps future observations from rejecting an otherwise valid report.
// Only established reason enums may cross the Server API/SSE boundary.
func (h *UpdaterHealth) Normalize() {
	if h == nil {
		return
	}
	switch h.Status {
	case "healthy", "missing", "unreachable", "identity_mismatch", "unsupported":
	default:
		h.Status = "unknown"
	}
	switch h.Reason {
	case "", "runtime_directory_missing", "socket_missing", "directory_unsafe", "socket_unsafe", "socket_replaced", "authentication_failed", "connect_failed", "probe_failed", "system_unsupported":
	default:
		h.Reason = ""
	}
}

func (h *UpdaterHealth) ReasonCode() string {
	if h == nil || h.Status == "healthy" {
		return ""
	}
	switch h.Status {
	case "missing":
		if h.Reason == "runtime_directory_missing" {
			return "updater_directory_missing"
		}
		return "updater_socket_missing"
	case "unreachable":
		return "updater_unreachable"
	case "identity_mismatch":
		return "updater_identity_mismatch"
	case "unsupported":
		return "updater_unsupported"
	default:
		return "updater_unavailable"
	}
}
