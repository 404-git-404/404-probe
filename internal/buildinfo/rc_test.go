package buildinfo

import "testing"

func TestRCVersionOrderAndStableIsolation(t *testing.T) {
	chain := []string{"v1.0.2", "v1.0.3-beta.1", "v1.0.3-beta.18446744073709551615", "v1.0.3-rc.1", "v1.0.3-rc.2", "v1.0.3-rc.10", "v1.0.3", "v1.0.4-beta.1"}
	for i, l := range chain {
		for j, r := range chain {
			cmp, ok := CompareReleaseVersions(l, r)
			if !ok || i < j && cmp != -1 || i == j && cmp != 0 || i > j && cmp != 1 {
				t.Fatal(l, r, cmp, ok)
			}
		}
	}
	for _, v := range []string{"v1.0.3-rc.0", "v1.0.3-rc.01", "v1.0.3-rc.18446744073709551616", "v1.0.3-rc.1+meta", "v1.0.3-RC.1", "v1.0.3-rc.1/asset"} {
		if IsReleaseVersion(v) {
			t.Fatal(v)
		}
	}
	i := Info{Version: FirstRCSupportVersion, Commit: "0123456789abcdef0123456789abcdef01234567"}
	if !i.ExplicitUpgradeEligible() || i.UpgradeEligible() || IsCanonicalVersion(i.Version) {
		t.Fatal(i)
	}
	i.Dirty = true
	if i.ExplicitUpgradeEligible() {
		t.Fatal("dirty RC accepted")
	}
}
