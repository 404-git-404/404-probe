package buildinfo

import (
	"os"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	for _, test := range []struct {
		left, right string
		want        int
		ok          bool
	}{
		{"v0.8.0", "v0.8.1", -1, true},
		{"v1.0.0", "v0.9.9", 1, true},
		{"v0.8.0", "v0.8.0", 0, true},
		{"dev", "v0.8.0", 0, false},
	} {
		got, ok := CompareVersions(test.left, test.right)
		if got != test.want || ok != test.ok {
			t.Fatalf("CompareVersions(%q,%q)=(%d,%t) want (%d,%t)", test.left, test.right, got, ok, test.want, test.ok)
		}
	}
}

func TestInspectExecutableRejectsNonGoFile(t *testing.T) {
	path := t.TempDir() + "/not-a-go-binary"
	if err := os.WriteFile(path, []byte("no"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectExecutable(path); err == nil {
		t.Fatal("non-Go file was accepted")
	}
}

func TestExplicitBetaDoesNotOpenStableUpgradeChannel(t *testing.T) {
	for _, value := range []string{"v1.0.1-beta.1", "v1.0.1-beta.2"} {
		if !IsReleaseVersion(value) || IsCanonicalVersion(value) {
			t.Fatalf("channel boundary: %s", value)
		}
		if (Info{Version: value, Commit: "0123456789abcdef0123456789abcdef01234567"}).UpgradeEligible() {
			t.Fatal("Beta enabled remote Agent upgrade")
		}
		if _, ok := CompareVersions("v1.0.0", value); ok {
			t.Fatal("stable comparator accepted Beta")
		}
	}
	for _, value := range []string{"v1.0.1-beta.0", "v1.0.1-beta.01", "v01.0.1-beta.1", "v1.0.1-rc.1", "v1.0.1-beta.1+build", "v1.0.1-beta.1/asset"} {
		if IsReleaseVersion(value) {
			t.Fatalf("invalid release accepted: %s", value)
		}
	}
	for _, pair := range [][2]string{{"v1.0.0", "v1.0.1-beta.1"}, {"v1.0.1-beta.1", "v1.0.1-beta.2"}, {"v1.0.1-beta.2", "v1.0.1"}} {
		if cmp, ok := CompareReleaseVersions(pair[0], pair[1]); !ok || cmp != -1 {
			t.Fatalf("explicit transition %v: %d %t", pair, cmp, ok)
		}
		if cmp, ok := CompareReleaseVersions(pair[1], pair[0]); !ok || cmp != 1 {
			t.Fatalf("downgrade ordering %v: %d %t", pair, cmp, ok)
		}
	}
}
