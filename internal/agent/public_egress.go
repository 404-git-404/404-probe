package agent

import (
	"context"
	"fmt"
	"time"

	"404-probe/internal/egress"
	"404-probe/internal/protocol"
)

// egressChecker is the one thing the executor needs. The production Checker talks to
// api.ipify.org / api6.ipify.org, addresses that are compiled into internal/egress.
type egressChecker interface {
	Check(ctx context.Context, family string) protocol.EgressObservation
}

// PublicEgressExecutor runs the Server-dispatched public IP check. It is the only code path in the
// Agent that contacts the echo service: there is no timer, no startup probe and no telemetry
// attached to reports. The job has no parameters, so there is nothing a Server (or anything posing
// as one) could use to point the check elsewhere.
type PublicEgressExecutor struct{ checker egressChecker }

func NewPublicEgressExecutor() *PublicEgressExecutor {
	return &PublicEgressExecutor{checker: egress.NewChecker()}
}

func (*PublicEgressExecutor) SupportedProbeTypes() []protocol.ProbeType {
	return []protocol.ProbeType{protocol.ProbeTypePublicEgress}
}

func (*PublicEgressExecutor) SupportsPublicEgress() bool { return true }

func (e *PublicEgressExecutor) Execute(ctx context.Context, job protocol.Job) (Execution, error) {
	// Anything but the exact parameterless job is refused before a single connection is made.
	if job.ProbeType != protocol.ProbeTypePublicEgress || job.Config.PublicEgress == nil || e == nil || e.checker == nil {
		return Execution{}, fmt.Errorf("%w: %s", ErrUnsupportedProbeType, job.ProbeType)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*egress.Timeout+time.Second)
	defer cancel()
	result := protocol.PublicEgressResult{IPv4: strip(e.checker.Check(ctx, "ipv4"), "ipv4"), IPv6: strip(e.checker.Check(ctx, "ipv6"), "ipv6")}
	execution := Execution{Success: result.Succeeded(), Result: protocol.ProbeResult{PublicEgress: &result}}
	if ctx.Err() != nil {
		execution.Success = false
		execution.ErrorCategory, execution.ErrorMessage = "canceled", "the public egress check was interrupted"
		execution.Result.PublicEgress = &protocol.PublicEgressResult{IPv4: protocol.UnknownEgress("ipv4", "timeout"), IPv6: protocol.UnknownEgress("ipv6", "timeout")}
		return execution, nil
	}
	if !execution.Success {
		execution.ErrorCategory, execution.ErrorMessage = "no_public_address", "no public address could be verified"
	}
	return execution, nil
}

// strip keeps only what the Server needs: it stamps times and validity itself.
func strip(o protocol.EgressObservation, family string) protocol.EgressObservation {
	return protocol.EgressObservation{Family: family, Status: o.Status, IP: o.IP, Reason: o.Reason}
}
