package platformsupport

import (
	"os"
	"testing"
)

func TestA1QualityPlatformPolicyIndependent(t *testing.T) {
	for _, tc := range []struct {
		goos, release string
		marker        error
		reason        string
	}{{"linux", "ID=alpine\n", os.ErrNotExist, "platform_excluded:alpine"}, {"linux", "ID=debian\n", os.ErrNotExist, ""}, {"linux", "ID=debian\n", nil, "platform_excluded:alpine"}, {"linux", "ID=debian\n", os.ErrPermission, "platform_identity_unknown"}, {"linux", "ID=debian\nID=alpine\n", os.ErrNotExist, "platform_identity_unknown"}, {"windows", "", os.ErrPermission, ""}} {
		calls := 0
		p := detectQuality(tc.goos, func(path string) ([]byte, error) {
			calls++
			if path == "/etc/os-release" {
				return []byte(tc.release), nil
			}
			return nil, tc.marker
		})
		if p.Reason != tc.reason || p.Excluded != (tc.reason != "") {
			t.Fatal(tc, p)
		}
		if tc.goos == "windows" && calls != 0 {
			t.Fatal("non-Linux identity read")
		}
	}
}
