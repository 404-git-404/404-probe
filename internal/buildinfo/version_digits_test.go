package buildinfo

import "testing"

func TestTwoDigitReleaseVersionMatrix(t *testing.T) {
	for _, pair := range [][2]string{{"v1.0.9", "v1.0.10"}, {"v1.9.9", "v1.10.0"}, {"v9.9.9", "v10.0.0"}} {
		for _, compare := range []func(string, string) (int, bool){CompareVersions, CompareReleaseVersions} {
			for _, row := range []struct {
				left, right string
				want        int
			}{{pair[0], pair[1], -1}, {pair[1], pair[0], 1}, {pair[1], pair[1], 0}} {
				if got, ok := compare(row.left, row.right); !ok || got != row.want {
					t.Fatalf("%v: %s / %s = %d %t", pair, row.left, row.right, got, ok)
				}
			}
		}
	}
	for _, row := range []struct {
		left, right string
		want        int
	}{{"v1.0.10-rc.9", "v1.0.10-rc.10", -1}, {"v1.0.10-rc.10", "v1.0.10-rc.9", 1}, {"v1.0.10-rc.10", "v1.0.10-rc.10", 0}} {
		if got, ok := CompareReleaseVersions(row.left, row.right); !ok || got != row.want {
			t.Fatal(row, got, ok)
		}
	}
	for _, invalid := range []string{"v1.0.010", "v01.0.10", "v1.0.10-dirty", "v1.0.10+dirty", "dev", "dirty", "v1.0.18446744073709551616", "v1.0.10-rc.01"} {
		for _, compare := range []func(string, string) (int, bool){CompareVersions, CompareReleaseVersions} {
			if _, ok := compare(invalid, "v1.0.10"); ok {
				t.Fatalf("accepted %q", invalid)
			}
			if _, ok := compare("v1.0.10", invalid); ok {
				t.Fatalf("accepted right %q", invalid)
			}
		}
	}
}
