package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func installerFixtureSkip(t *testing.T, reason string) {
	t.Helper()
	message := "INSTALLER-FIXTURE-SKIPPED: " + reason
	fmt.Fprintln(os.Stderr, message)
	if os.Getenv("INSTALLER_FIXTURE_REQUIRED") == "1" {
		t.Fatal(message)
	}
	t.Skip(message)
}

// All executable installer harnesses use the same fail-closed Linux boundary.
// PATH mocks alone cannot contain /bin/rm, redirects, or replaced parents.
func installerFixtureCommand(t *testing.T, bash string, args ...string) *exec.Cmd {
	t.Helper()
	if runtime.GOOS != "linux" {
		installerFixtureSkip(t, "installer execution requires verified Linux bubblewrap + strace isolation; PATH mocks are insufficient")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		installerFixtureSkip(t, "installer isolation requires Python3")
	}
	_, file, _, _ := runtime.Caller(0)
	runner := filepath.Join(filepath.Dir(file), "..", "..", "scripts", "installer-fixture.py")
	root := filepath.Dir(args[0])
	probe := exec.Command(python, runner, "--root", root, "--probe")
	output, err := probe.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 77 {
		installerFixtureSkip(t, strings.TrimSpace(string(output)))
	}
	if err != nil {
		t.Fatalf("installer isolation proof: %v\n%s", err, output)
	}
	t.Log(strings.TrimSpace(string(output)))
	return exec.Command(python, append([]string{runner, "--root", root, "--", bash}, args...)...)
}

// Central one-pass mapping handles both uninstall copies, their helper heredoc,
// all roles, runtime, candidate/previous, helper, state and lock prefixes.
func installerFixtureSource(source, root string) string {
	paths := regexp.MustCompile(`/etc|/usr/local/bin|/usr/local/sbin|/var/lib|/var/log|/run|/root`)
	defaultValue := regexp.MustCompile(`(?:\$\{[A-Za-z_][A-Za-z0-9_]*:?|[\s=])-$`)
	var result strings.Builder
	last := 0
	for _, match := range paths.FindAllStringIndex(source, -1) {
		start, end := match[0], match[1]
		if start > 0 {
			previous := source[start-1]
			if (previous >= 'a' && previous <= 'z' || previous >= 'A' && previous <= 'Z' || previous >= '0' && previous <= '9' || strings.ContainsRune("_}/.-", rune(previous))) && !defaultValue.MatchString(source[:start]) {
				continue
			}
		}
		result.WriteString(source[last:start])
		result.WriteString(root + "/system" + source[start:end])
		last = end
	}
	result.WriteString(source[last:])
	return result.String()
}

func TestInstallerFixtureShellDefaultsAndAssignmentsAreIdempotent(t *testing.T) {
	for _, operator := range []string{":-", "-", ":=", "=", ":+", "+", ":?", "?"} {
		for _, quote := range []string{"", `"`, "'"} {
			source := "${VAR" + operator + quote + "/etc/404-probe/item" + quote + "}"
			want := strings.ReplaceAll(source, "/etc/404-probe", "/fixture/system/etc/404-probe")
			if got := installerFixtureSource(source, "/fixture"); got != want || installerFixtureSource(got, "/fixture") != want {
				t.Fatal(source, got, want)
			}
		}
	}
}

func TestInstallerFixtureMapsBothUninstallCopies(t *testing.T) {
	data, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	if strings.Count(source, "remove_owned_tree /run/404-probe") != 2 {
		t.Fatal("update fixture coverage for changed uninstall copies")
	}
	redirected := installerFixtureSource(source, "/fixture")
	for _, prefix := range []string{"/run/404-probe", "/etc/404-probe", "/var/lib/404-probe", "/usr/local/bin/"} {
		if strings.Contains(strings.ReplaceAll(redirected, "/fixture/system"+prefix, "MAPPED"), prefix) {
			t.Fatalf("unmapped %s", prefix)
		}
	}
}
