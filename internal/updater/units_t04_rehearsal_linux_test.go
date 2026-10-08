//go:build linux && t04_rehearsal

package updater

import (
	"strings"
	"testing"
)

func TestT04ReceiptWorkerGetsFixtureCAThroughScopedEnvironment(t *testing.T) {
	const fixtureCA = "Environment=SSL_CERT_FILE=/usr/local/lib/404-probe-t04-control/t04-root-ca.pem"
	if strings.Count(removalWorkerUnitContents, fixtureCA) != 1 {
		t.Fatal("T04 receipt worker must receive exactly one fixture CA file override")
	}
	for _, forbidden := range []string{
		"EnvironmentFile=/etc/404-probe/",
		"EnvironmentFile=/etc/404-probe-t04-agent/agent.env",
		"/etc/ssl/certs",
		"update-ca-certificates",
	} {
		if strings.Contains(removalWorkerUnitContents, forbidden) {
			t.Fatalf("T04 worker unexpectedly changes or broadens system trust: %q", forbidden)
		}
	}
	if strings.Contains(removalFinalizerUnitContents, "SSL_CERT_FILE=") {
		t.Fatal("network trust override must not be copied to the private-network finalizer")
	}
}
