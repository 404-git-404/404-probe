package storage

import (
	"encoding/json"
	"strings"
	"testing"

	"404-probe/internal/protocol"
)

// failQualityConfigWrites makes every update of quality_config fail until the returned function runs,
// the way a failing write transaction would.
func (k *v4Kit) failQualityConfigWrites() (restore func()) {
	k.t.Helper()
	if _, err := k.s.db.Exec(`CREATE TRIGGER inject_reconcile_failure BEFORE UPDATE ON quality_config BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		k.t.Fatal(err)
	}
	return func() {
		if _, err := k.s.db.Exec(`DROP TRIGGER inject_reconcile_failure`); err != nil {
			k.t.Fatal(err)
		}
	}
}

func (k *v4Kit) mustFailReconcile(r protocol.Report) {
	k.t.Helper()
	if err := k.s.ReconcileQualityAuthorization(k.ctx, k.id, QualityNegotiationFromReport(r), k.now); err == nil {
		k.t.Fatal("the injected failure did not reach the recalculation")
	}
}

func (k *v4Kit) assertNoUserTarget(version int, header bool, what string) {
	k.t.Helper()
	got, err := k.sessionConfig(version, header)
	if err != nil {
		return // refused outright is also nothing delivered
	}
	wire, _ := json.Marshal(got)
	if strings.Contains(string(wire), "example.com") || strings.Contains(string(wire), "443") {
		k.t.Fatalf("%s: a user target was delivered: %s", what, wire)
	}
}

// G1: a failed recalculation changes nothing, delivers nothing to an unauthorized session, and the
// next report that carries the evidence brings the authorization to the right state.
func TestV4ReconcileFailureRecovers(t *testing.T) {
	t.Run("grant fails, next report grants", func(t *testing.T) {
		k := newV4Kit(t)
		k.report(false, v4Caps(protocol.QualityIPv6Present)) // an old Agent saved before upgrade
		k.save(30000, v4Slot("telecom", "example.com", 443, "v4tcp", "v4icmp"))
		k.enable(true)
		k.restart() // upgraded and restarted
		k.reconcile(k.report(true, nil))
		restore := k.failQualityConfigWrites()
		step2 := k.report(true, v4Caps(protocol.QualityIPv6Present))
		k.mustFailReconcile(step2)
		if len(k.open()) != 0 {
			t.Fatal("a failed recalculation opened lanes")
		}
		cfg, err := k.sessionConfig(2, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, target := range cfg.Targets {
			if target.Status == "active" {
				t.Fatalf("an active target was delivered without authorization: %+v", target)
			}
		}
		k.assertNoUserTarget(2, false, "no header during the failure")
		k.assertNoUserTarget(1, true, "version 1 during the failure")
		k.invariants()
		restore()
		// The following report carries the same evidence and finishes the job.
		k.reconcile(k.report(true, v4Caps(protocol.QualityIPv6Present)))
		if len(k.open()) != 2 {
			t.Fatalf("authorization not restored: %s", k.summary())
		}
		cfg, _ = k.sessionConfig(2, true)
		active := 0
		for _, target := range cfg.Targets {
			if target.Status == "active" {
				active++
			}
		}
		if active != 2 || k.get().AgentSupport != protocol.QualitySupportUserTargets {
			t.Fatalf("active=%d support=%s", active, k.get().AgentSupport)
		}
		k.invariants()
	})
	t.Run("revoke fails, session is already cut off, next report revokes", func(t *testing.T) {
		k := v4Base(t, true)
		running := k.summary()
		restore := k.failQualityConfigWrites()
		revoke := k.report(false, v4Caps(protocol.QualityIPv6Present)) // header stripped
		k.mustFailReconcile(revoke)
		// The device-level state could not be taken back, but the session itself is no longer
		// negotiated: it gets the old format and its samples are refused.
		if k.summary() != running {
			t.Fatal("a failed recalculation moved the intervals")
		}
		k.assertNoUserTarget(2, true, "revoked session")
		k.assertNoUserTarget(2, false, "revoked session without header")
		cfg := k.get()
		at := k.now.Add(1)
		sample := k.sample(cfg, "telecom", "ipv4", "tcp", 1, at, "success")
		if acks := k.upload(at, sample); acks[0] != "unauthorized" {
			t.Fatalf("a revoked session's sample: %v", acks)
		}
		restore()
		k.reconcile(k.report(false, nil))
		if len(k.open()) != 0 {
			t.Fatalf("revocation not completed: %s", k.summary())
		}
		k.invariants()
	})
	t.Run("a late request cannot undo a newer decision", func(t *testing.T) {
		k := v4Base(t, true)
		old := k.report(true, v4Caps(protocol.QualityIPv6Present))
		revoke := k.report(false, nil)
		k.reconcile(revoke)
		k.reconcile(old)
		if len(k.open()) != 0 {
			t.Fatal("a stale grant arrived after the revocation")
		}
	})
}
