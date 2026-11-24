package systemchecks

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func TestCertificateScannerLimitsAndPrivacy(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	makeCert := func(serial int64, domain string, expires time.Time, ca bool) []byte {
		t.Helper()
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: domain},
			NotBefore: now.AddDate(-1, 0, 0), NotAfter: expires, DNSNames: []string{domain}, IsCA: ca, BasicConstraintsValid: true}
		raw, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	write := func(name string, data []byte) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	chain := func(raw []byte) []byte { return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}) }
	warn := makeCert(1, "warning.example.test", now.Add(5*24*time.Hour), false)
	expired := makeCert(2, "expired.example.test", now.Add(-time.Hour), false)
	normal := makeCert(3, "normal.example.test", now.Add(30*24*time.Hour), false)
	ca := makeCert(4, "ca.example.test", now.Add(80*24*time.Hour), true)
	key, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})
	write("fullchain.pem", bytes.Join([][]byte{privatePEM, chain(warn), chain(ca)}, nil))
	write("private-only.pem", privatePEM)
	write("duplicate.crt", chain(warn))
	write("a/b/c/d/e/expired.cer", expired)
	write("a/b/c/d/e/f/too-deep.crt", chain(makeCert(5, "deep.example.test", now, false)))
	write("a/b/normal.crt", chain(normal))
	write("blocked/deeper/supplement.pem", chain(makeCert(6, "blocked.example.test", now.Add(10*24*time.Hour), false)))
	blocked := filepath.Join(root, "blocked")
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0700) })
	result := scanCertificates(t.Context(), []string{root})
	want := []string{"expired.example.test", "warning.example.test", "normal.example.test"}
	unreadable := 1
	if os.Geteuid() == 0 {
		want = []string{"expired.example.test", "warning.example.test", "blocked.example.test", "normal.example.test"}
		unreadable = 0
	}
	var domains []string
	for _, cert := range result.Certificates {
		domains = append(domains, cert.Domains)
	}
	if !reflect.DeepEqual(domains, want) || result.UnreadableCount != unreadable {
		t.Fatalf("certificates = %+v", result)
	}
	if unreadable > 0 && !reflect.DeepEqual(result.Unreadable, []string{blocked}) {
		t.Fatal(result.Unreadable)
	}
	if err := os.Chmod(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	supplement := scanCertificateSubtrees(t.Context(), []string{root}, []string{blocked})
	link := filepath.Join(t.TempDir(), "linked-root")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	resolved := scanCertificateSubtrees(t.Context(), []string{link}, []string{blocked})
	if len(resolved.Certificates) != 1 || resolved.Certificates[0].Domains != "blocked.example.test" {
		t.Fatal(resolved)
	}
	combined := mergeCertificates(result, supplement)
	if len(combined.Certificates) != 4 || combined.UnreadableCount != 0 || combined.Certificates[2].Domains != "blocked.example.test" {
		t.Fatal(combined)
	}
}

func TestFail2banLimitsAndOutcomes(t *testing.T) {
	names := make([]string, 66)
	for i := range names {
		names[i] = fmt.Sprintf("fixture%d", i)
	}
	var ips []string
	for i := 1; i <= 201; i++ {
		ips = append(ips, fmt.Sprintf("192.0.2.%d", i))
	}
	run := func(_ context.Context, _ string, args ...string) (string, error) {
		if len(args) == 1 {
			return "Number of jail: 66\nJail list: " + strings.Join(names, ", "), nil
		}
		return "Currently banned: 275\nBanned IP list: " + strings.Join(ips, " "), nil
	}
	out := fail2banStatus(t.Context(), run, "fail2ban-client", func() bool { return true })
	actualIPs := 0
	for _, jail := range out.Jails {
		actualIPs += len(jail.IPs)
		if len(jail.IPs) > 200 || jail.Total != 275 {
			t.Fatal(jail)
		}
	}
	if out.Status != "ok" || out.JailCount != 66 || len(out.Jails) != 64 || actualIPs != 2000 || len(out.Jails[10].IPs) != 0 {
		t.Fatal(out.Status, out.JailCount, len(out.Jails), actualIPs)
	}
	if err := (protocol.SystemChecks{Fail2ban: out}).Validate(); err != nil {
		t.Fatal(err)
	}
	if out := fail2banStatus(t.Context(), run, "fail2ban-client", func() bool { return false }); out.Status != protocol.CheckNotInstalled {
		t.Fatal(out)
	}
	for _, mode := range []struct{ state, want string }{{"inactive", protocol.CheckNotRunning}, {"active", protocol.CheckError}} {
		failing := func(_ context.Context, binary string, _ ...string) (string, error) {
			if binary == "/usr/bin/systemctl" {
				return mode.state, nil
			}
			return "", errors.New("fixture unavailable")
		}
		out := fail2banStatus(t.Context(), failing, "fail2ban-client", func() bool { return true })
		if out.Status != mode.want {
			t.Fatal(out)
		}
	}
}

func TestWatchedServicesAndFailedUnits(t *testing.T) {
	run := func(_ context.Context, binary string, args ...string) (string, error) {
		if binary != "/usr/bin/systemctl" {
			t.Fatalf("unexpected command %s", binary)
		}
		if args[0] != "show" {
			t.Fatal(args)
		}
		return "Id=fixture.timer\nActiveState=failed\n\nId=secondary.mount\nActiveState=failed\n\nId=caddy.service\nActiveState=active\n\nId=nginx.service\nActiveState=inactive", nil
	}
	var tracker serviceRestartTracker
	status, services, failed, _ := tracker.collect(t.Context(), time.Now(), []string{"caddy", "nginx"}, true, run)
	if status != protocol.CheckOK || len(services) != 2 || services[1].State != "inactive" || !reflect.DeepEqual(failed, []string{"fixture.timer", "secondary.mount"}) {
		t.Fatal(status, services, failed)
	}
	if status, _, _, _ := tracker.collect(t.Context(), time.Now(), nil, false, nil); status != protocol.CheckUnsupported {
		t.Fatal(status)
	}
}
