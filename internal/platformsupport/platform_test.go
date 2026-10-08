package platformsupport

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeferredHostIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, goos, release   string
		releaseErr, markerErr error
		blocked               bool
	}{
		{"Alpine", "linux", "ID=alpine\n", nil, nil, true},
		{"Alpine without marker", "linux", "ID=\"alpine\"\n", nil, os.ErrNotExist, true},
		{"Debian12", "linux", "ID=debian\nVERSION_ID=12\n", nil, os.ErrNotExist, false},
		{"Debian13", "linux", "ID='debian'\nVERSION_ID=13\n", nil, os.ErrNotExist, false},
		{"contradiction", "linux", "ID=debian\n", nil, nil, true},
		{"unreadable marker", "linux", "ID=debian\n", nil, os.ErrPermission, true},
		{"unreadable release", "linux", "", os.ErrPermission, nil, true},
		{"missing release", "linux", "", os.ErrNotExist, nil, true},
		{"missing ID", "linux", "ID_LIKE=debian\n", nil, os.ErrNotExist, true},
		{"duplicate", "linux", "ID=debian\nID=alpine\n", nil, os.ErrNotExist, true},
		{"shell syntax", "linux", "ID=$(echo debian)\n", nil, os.ErrNotExist, true},
		{"Windows", "windows", "", os.ErrPermission, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			p := detect(tc.goos, func(path string) ([]byte, error) {
				calls++
				if path == "/etc/os-release" {
					return []byte(tc.release), tc.releaseErr
				}
				if path != "/etc/alpine-release" {
					t.Fatal(path)
				}
				return nil, tc.markerErr
			})
			if p.DeferredUnsupported != tc.blocked {
				t.Fatalf("policy=%+v", p)
			}
			if tc.goos != "linux" && calls != 0 {
				t.Fatal("nonlinux reads host files")
			}
		})
	}
}

func TestReleaseReadIsBounded(t *testing.T) {
	p := filepath.Join(t.TempDir(), "release")
	if err := os.WriteFile(p, make([]byte, maxReleaseBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRelease(p); err == nil {
		t.Fatal("oversized release accepted")
	}
}
