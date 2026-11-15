package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"404-probe/internal/egress"
	"404-probe/internal/protocol"
)

// The production Checker uses the fixed ipify, icanhazip and Cloudflare sites in internal/egress.
type egressChecker interface {
	Check(ctx context.Context, family string) protocol.EgressObservation
}

// PublicEgressExecutor runs the Server-dispatched public IP check. It is the only code path in the
// Agent that contacts the echo sites: there is no timer, no startup probe and no telemetry
// attached to reports. The job has no parameters, so there is nothing a Server (or anything posing
// as one) could use to point the check elsewhere.
type PublicEgressExecutor struct{ checker egressChecker }

const publicEgressResultReserve = 5 * time.Second

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
	deadline := time.Now().Add(egress.MaxCheckTime)
	if taskDeadline, ok := ctx.Deadline(); ok {
		if checkDeadline := taskDeadline.Add(-publicEgressResultReserve); checkDeadline.Before(deadline) {
			deadline = checkDeadline
		}
	}
	check := func(family string) protocol.EgressObservation {
		familyCtx, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		return strip(e.checker.Check(familyCtx, family), family, true)
	}
	var result protocol.PublicEgressResult
	var checks sync.WaitGroup
	checks.Go(func() { result.IPv4 = check("ipv4") })
	checks.Go(func() { result.IPv6 = check("ipv6") })
	checks.Wait()
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

// strip keeps only result fields. The Server stamps times and validity itself.
func strip(o protocol.EgressObservation, family string, includeSites bool) protocol.EgressObservation {
	result := protocol.EgressObservation{Family: family, Status: o.Status, IP: o.IP, Reason: o.Reason}
	if includeSites {
		result.Site, result.Tried = o.Site, o.Tried
	}
	return result
}
