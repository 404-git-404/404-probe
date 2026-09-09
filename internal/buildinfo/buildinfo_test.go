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
