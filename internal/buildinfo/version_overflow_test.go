package buildinfo

import "testing"

func TestReleaseVersionsRejectOverflowAndOrderBeta(t *testing.T) {
	for _, v := range []string{"v18446744073709551616.0.0", "v1.0.2-beta.18446744073709551616", "v1.00.1", "v1.0.2-beta.0", "v1.0.2-rc.1"} {
		if IsReleaseVersion(v) {
			t.Fatal("accepted", v)
		}
	}
	chain := []string{"v1.0.1-beta.1", "v1.0.1", "v1.0.2-beta.1", "v1.0.2-beta.2", "v1.0.2"}
	for i := 1; i < len(chain); i++ {
		if comparison, ok := CompareReleaseVersions(chain[i-1], chain[i]); !ok || comparison >= 0 {
			t.Fatal(chain[i-1], chain[i], comparison, ok)
		}
	}
}
