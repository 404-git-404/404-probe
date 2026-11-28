package systemchecks

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"404-probe/internal/protocol"
	"golang.org/x/crypto/ssh"
)

func inventoryFixtureFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func unavailableInventoryCommand(context.Context, string, ...string) (string, error) {
	return "", os.ErrNotExist
}

func TestSecurityBaselineBoundsSlowInput(t *testing.T) {
	blocked := make(chan struct{})
	defer close(blocked)
	if _, err := boundedRootInventory(t.Context(), 10*time.Millisecond, func(context.Context) (int, error) {
		<-blocked
		return 1, nil
	}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestSecurityBaselineCollection(t *testing.T) {
	work := t.TempDir()
	etc, proc, home := filepath.Join(work, "etc"), filepath.Join(work, "proc"), filepath.Join(work, "home")
	inventoryFixtureFile(t, filepath.Join(etc, "passwd"), fmt.Sprintf("root:x:0:0::/nonexistent:/bin/sh\nfixture:x:1000:27::%s:/bin/sh\n", home))
	inventoryFixtureFile(t, filepath.Join(etc, "group"), "sudo:x:27:fixture,another\nwheel:x:42:other\nadmin:x:43:\n")
	inventoryFixtureFile(t, filepath.Join(etc, "sudoers"), "fixture ALL=(ALL) ALL\n")
	inventoryFixtureFile(t, filepath.Join(etc, "sudoers.d/extra"), "# fixture only\n")
	keyLine := func(n byte) string {
		seed := make([]byte, ed25519.SeedSize)
		seed[0] = n
		key, err := ssh.NewPublicKey(ed25519.NewKeyFromSeed(seed).Public())
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	}
	keyPath := filepath.Join(home, "fixture%", "keys")
	key := keyLine(0)
	inventoryFixtureFile(t, keyPath, "bad line\n"+`from="10.0.0.0/24",command="echo \"quoted\" with spaces" `+key+" key comment\n")
	keyPathFallback := filepath.Join(home, ".ssh", "authorized_keys")
	inventoryFixtureFile(t, keyPathFallback, key+" key comment\n")
	inventoryFixtureFile(t, filepath.Join(proc, "sys/net/ipv4/ip_local_port_range"), "32768\t60999\n")
	socketRow := func(address, state, inode string) string {
		return " 0: " + address + " 00000000:0000 " + state + " 0 0 0 0 0 " + inode + "\n"
	}
	inventoryFixtureFile(t, filepath.Join(proc, "net/tcp"), "sl local_address\n"+socketRow("00000000:1F90", "0A", "17")+socketRow("0100007F:1F91", "0A", "18")+socketRow("00000000:1F92", "01", "19"))
	inventoryFixtureFile(t, filepath.Join(proc, "net/tcp6"), socketRow("00000000000000000000000000000000:1F90", "0A", "20")+socketRow("00000000000000000000000001000000:1F93", "0A", "21"))
	inventoryFixtureFile(t, filepath.Join(proc, "net/udp"), socketRow("00000000:0035", "07", "22")+socketRow("00000000:9001", "07", "23"))
	inventoryFixtureFile(t, filepath.Join(proc, "14/comm"), "fixture-listener\n")
	if err := os.MkdirAll(filepath.Join(proc, "14/fd"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("socket:[17]", filepath.Join(proc, "14/fd/2")); err != nil {
		t.Fatal(err)
	}
	runner := func(context.Context, string, ...string) (string, error) {
		return "authorizedkeysfile %h/%u%%/keys", nil
	}
	now := time.Now()
	got := collectSecurityBaseline(t.Context(), etc, proc, now, runner)
	if err := got.Validate(); err != nil || got.Status != "ok" {
		t.Fatalf("snapshot %v, validation %v", got, err)
	}
	s := got.Snapshot
	if len(s.SSHKeys) != 1 || s.SSHKeys[0].User != "fixture" || s.SSHKeys[0].Type != "ssh-ed25519" || s.SSHKeys[0].Comment != "key comment" {
		t.Fatal(s.SSHKeys)
	}
	if binary, err := exec.LookPath("ssh-keygen"); err == nil {
		out, err := exec.Command(binary, "-lf", keyPathFallback).Output()
		if err != nil || strings.Fields(string(out))[1] != s.SSHKeys[0].Fingerprint {
			t.Fatalf("ssh-keygen fingerprint %q: %v", out, err)
		}
	}
	if len(s.Ports) != 2 || s.Ports[0].Port != 8080 || !slices.Equal(s.Ports[0].Processes, []string{"fixture-listener"}) || s.Ports[1].Port != 53 {
		t.Fatal(s.Ports)
	}
	if !slices.Equal(s.UID0, []string{"root"}) || len(s.SudoMembers) != 3 || len(s.SudoersFiles) != 2 ||
		s.SudoersFiles[0].SHA256 != protocol.SHA256Hex([]byte("fixture ALL=(ALL) ALL\n")) {
		t.Fatal(s)
	}
	if fallback := collectSecurityBaseline(t.Context(), etc, proc, now, unavailableInventoryCommand); fallback.Digest != got.Digest {
		t.Fatal("default authorized_keys paths did not produce the same snapshot", fallback)
	}
	var many strings.Builder
	for i := range 65 {
		fmt.Fprintln(&many, keyLine(byte(i)))
	}
	inventoryFixtureFile(t, keyPath, many.String())
	inventoryFixtureFile(t, filepath.Join(etc, "group"), "sudo:broken\n")
	inventoryFixtureFile(t, filepath.Join(proc, "net/tcp"), "malformed\n")
	if err := os.Remove(filepath.Join(etc, "sudoers.d/extra")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nonexistent", filepath.Join(etc, "sudoers.d/extra")); err != nil {
		t.Fatal(err)
	}
	bad := collectSecurityBaseline(t.Context(), etc, proc, now, runner)
	if bad.Status != "error" || bad.Snapshot != nil || !strings.Contains(bad.Reason, "ssh_keys:too_large") ||
		!strings.Contains(bad.Reason, "ports:read_failed") || !strings.Contains(bad.Reason, "sudo_members:read_failed") ||
		!strings.Contains(bad.Reason, "sudoers_files:not_found") {
		t.Fatal(bad)
	}
	if err := os.Remove(filepath.Join(etc, "passwd")); err != nil {
		t.Fatal(err)
	}
	if missing := collectSecurityBaseline(t.Context(), etc, proc, now, runner); !strings.Contains(missing.Reason, "passwd:not_found") {
		t.Fatal(missing)
	}
}

func TestSingboxConfigPathsAndCache(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "10 first.json")
	second := filepath.Join(dir, "20.json")
	inventoryFixtureFile(t, second, `{"outbounds":[{"type":"selector","tag":"second"}]}`)
	inventoryFixtureFile(t, first, `{"outbounds":[{"type":"selector","tag":"first"}]}`)
	inventoryFixtureFile(t, filepath.Join(dir, "30.txt"), "bad")
	wrap := func(value string) string {
		return "ExecStart={ path=/usr/bin/sing-box ; argv[]=/usr/bin/sing-box run " + value + " ; ignore_errors=no ; pid=1 }"
	}
	for _, tc := range []struct {
		name, exec string
		want       []string
	}{
		{"single", wrap(`-c "` + first + `"`), []string{"first"}},
		{"multiple", wrap(`-c=` + second + ` -c ` + strings.ReplaceAll(first, " ", `\x20`)), []string{"second", "first"}},
		{"directory", wrap(`-C ` + dir), []string{"first", "second"}},
		{"relative", wrap(`-C . -D ` + dir), []string{"first", "second"}},
		{"default", "ExecStart=", []string{"second"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := t.TempDir()
			run := func(context.Context, string, ...string) (string, error) { return tc.exec, nil }
			now := time.Now()
			got := collectSingboxStructure(t.Context(), cache, second, now, run)
			if got.Status != "ok" || got.Validate() != nil {
				t.Fatal(got)
			}
			var names []string
			for _, item := range got.Structure.Outbounds {
				names = append(names, item.Tag)
			}
			if !slices.Equal(names, tc.want) {
				t.Fatal(names)
			}
			cacheFile := filepath.Join(cache, "singbox-structure-cache.json")
			before, _ := os.ReadFile(cacheFile)
			repeat := collectSingboxStructure(t.Context(), cache, second, now.Add(time.Minute), run)
			after, _ := os.ReadFile(cacheFile)
			if len(before) == 0 || repeat.Digest != got.Digest || repeat.CollectedAt == got.CollectedAt || !bytes.Equal(before, after) {
				t.Fatal("unchanged configuration was re-extracted", repeat)
			}
		})
	}
	if out := collectSingboxStructure(t.Context(), t.TempDir(), second, time.Now(), func(_ context.Context, name string, _ ...string) (string, error) {
		if strings.HasSuffix(name, "systemctl") {
			return wrap("-c " + first), nil // systemctl renders spaces without quoting them.
		}
		return fmt.Sprintf(`{"type":"a(sasbttttuii)","data":[["/usr/bin/sing-box",["/usr/bin/sing-box","run","-c",%q],false,0,0,0,0,0,0,0]]}`, first), nil
	}); out.Status != "ok" || out.Structure.Outbounds[0].Tag != "first" {
		t.Fatal(out)
	}
	inventoryFixtureFile(t, second, `{"outbounds":[}`)
	if bad := collectSingboxStructure(t.Context(), t.TempDir(), second, time.Now(), unavailableInventoryCommand); bad.Reason != "parse_failed" || bad.Structure != nil {
		t.Fatal(bad)
	}
}

func TestSingboxStructureWhitelistAndLimits(t *testing.T) {
	input := `{
		"inbounds":[{"type":"vless","tag":"entry","listen_port":443,"listen":"inbound-address.invalid","users":[{"uuid":"uuid-inbound"}],"tls":{"reality":{"private_key":"private-reality","handshake":{"server":"reality-server.invalid"}}}}],
		"outbounds":[{"type":"vless","tag":"v","uuid":"uuid-vless","server":"vless-server.invalid","tls":{"reality":{"public_key":"public-reality","short_id":"secret-short-id"}}},
		{"type":"hysteria2","tag":"h","server":"hysteria-server.invalid","password":"hysteria-password","obfs":{"password":"obfs-password"}},
		{"type":"shadowsocks","tag":"ss","server":"ss-server.invalid","password":"ss-password"},
		{"type":"selector","tag":"choose","outbounds":["ss","h","v"],"default":"h","detour":"direct"}],
		"endpoints":[{"type":"wireguard","tag":"wg","private_key":"private-wireguard","peers":[{"address":"wireguard-server.invalid","public_key":"wireguard-public","pre_shared_key":"wireguard-preshared"}]}],
		"route":{"rules":[{"type":"logical","mode":"and","rules":[{"inbound":"entry","domain_suffix":["one-secret.invalid","two-secret.invalid"],"auth_user":"private-user","rule_set":["reference"]},{"network":"tcp","outbound":"choose","action":"route","override_address":"override-secret.invalid"}]}],
		"final":"choose","rule_set":[{"tag":"reference","type":"remote","url":"https://remote-rule.invalid/a"}]}}
	`
	got, truncated, err := extractSingboxStructure(t.Context(), [][]byte{[]byte(input)})
	if err != nil || truncated || len(got.Outbounds) != 5 || len(got.Route.Rules[0].Rules) != 2 {
		t.Fatal(got, truncated, err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"inbound-address.invalid", "uuid-inbound", "private-reality", "reality-server.invalid", "uuid-vless", "vless-server.invalid", "public-reality", "secret-short-id", "hysteria-server.invalid", "hysteria-password", "obfs-password", "ss-server.invalid", "ss-password", "private-wireguard", "wireguard-server.invalid", "wireguard-public", "wireguard-preshared", "one-secret.invalid", "two-secret.invalid", "private-user", "override-secret.invalid", "remote-rule.invalid"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("sensitive source value made it to the output: %q", forbidden)
		}
	}
	if got.Route.Rules[0].Rules[0].Conditions["domain_suffix"] != 2 || got.Route.Rules[0].Rules[0].Inbound[0] != "entry" ||
		got.Route.Rules[0].Rules[1].Outbound[0] != "choose" || got.Route.RuleSet[0].Tag != "reference" || got.Inbounds[0].ListenPort != 443 {
		t.Fatal(string(encoded))
	}
	outbounds := make([]protocol.SingboxOutbound, 520)
	choices := make([]string, 520)
	for i := range choices {
		choices[i] = strings.Repeat("出", 50)
	}
	for i := range outbounds {
		outbounds[i] = protocol.SingboxOutbound{Tag: strings.Repeat("长", 50), Type: "selector", Outbounds: choices}
	}
	raw, _ := json.Marshal(protocol.SingboxStructure{Outbounds: outbounds[:4]})
	got, truncated, err = extractSingboxStructure(t.Context(), [][]byte{raw})
	encoded, _ = json.Marshal(got)
	if err != nil || !truncated || len(encoded) > protocol.MaxSingboxStructureBytes || len(got.Outbounds) >= 4 ||
		len(got.Outbounds[0].Tag) > 128 || len(got.Outbounds[0].Outbounds) != 512 {
		t.Fatal(len(encoded), truncated, err)
	}
	small := []byte(`{"outbounds":[` + strings.Repeat(`{"tag":"a","type":"direct"},`, 512) + `{"tag":"last","type":"direct"}]}`)
	got, truncated, err = extractSingboxStructure(t.Context(), [][]byte{small})
	if err != nil || !truncated || len(got.Outbounds) != 512 {
		t.Fatal(got, truncated, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := extractSingboxStructure(ctx, [][]byte{small}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
