//go:build linux

package agent

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func TestSelectorOrderRootDirectoryIsReadableButNotReplaceableByAgent(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to exercise the service-user boundary")
	}
	account, err := user.Lookup("nobody")
	if err != nil {
		t.Skipf("nobody account unavailable: %v", err)
	}
	uidValue, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	gidValue, err := strconv.ParseUint(account.Gid, 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	credential := &syscall.Credential{Uid: uint32(uidValue), Gid: uint32(gidValue)}

	// Keep the fixture below /tmp so the unprivileged process can traverse the
	// parent. testing.T.TempDir's private parent is commonly mode 0700, which
	// would otherwise make this test pass for the wrong reason.
	root, err := os.MkdirTemp("/tmp", "404-probe-selector-order-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove fixture: %v", err)
		}
	})
	if err := os.Chown(root, 0, int(gidValue)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o750); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "config")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(directory, 0, int(gidValue)); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "sing-box.json")
	if err := os.WriteFile(config, []byte(`{"outbounds":[{"type":"selector","tag":"safe"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(directory, "selector-order.json")
	if err := ExtractSelectorOrder(config, output); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(output, 0, int(gidValue)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(output, 0o640); err != nil {
		t.Fatal(err)
	}

	read := exec.Command("sh", "-c", `test -r "$1" && test ! -w "$1" && test ! -w "$(dirname "$1")"`, "sh", output)
	read.SysProcAttr = &syscall.SysProcAttr{Credential: credential}
	if outputBytes, err := read.CombinedOutput(); err != nil {
		t.Fatalf("service-user access check failed: %v: %s", err, outputBytes)
	}

	victim := filepath.Join(root, "victim")
	if err := os.WriteFile(victim, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	attack := exec.Command("sh", "-c", `rm -f -- "$1" && ln -s -- "$2" "$1"`, "sh", output, victim)
	attack.SysProcAttr = &syscall.SysProcAttr{Credential: credential}
	if err := attack.Run(); err == nil {
		t.Fatal("service user replaced metadata in the root-owned directory")
	}
	info, err := os.Lstat(output)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("metadata type changed: info=%v err=%v", info, err)
	}
	body, err := os.ReadFile(victim)
	if err != nil || string(body) != "unchanged" {
		t.Fatalf("victim=%q err=%v", body, err)
	}
}
