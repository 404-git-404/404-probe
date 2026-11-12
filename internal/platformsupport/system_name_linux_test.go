package platformsupport

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/shirou/gopsutil/v4/common"
	"github.com/shirou/gopsutil/v4/host"
)

func TestSystemNameMatchesV112(t *testing.T) {
	for _, tc := range []struct {
		name  string
		want  string
		files map[string]string
	}{
		{"Debian 12", "debian 12.15", map[string]string{
			"os-release": "ID=debian\nVERSION_ID=12\n", "debian_version": "12.15\n",
			"lsb-release": "DISTRIB_ID=Debian\nDISTRIB_RELEASE=12\n",
		}},
		{"Debian 13", "debian 13.2", map[string]string{
			"os-release": "ID=debian\nVERSION_ID=13\n", "debian_version": "13.2\n",
			"lsb-release": "DISTRIB_ID=Debian\nDISTRIB_RELEASE=13\n",
		}},
		{"Ubuntu", "ubuntu 24.04", map[string]string{
			"os-release": "ID=ubuntu\nVERSION_ID=24.04\n", "debian_version": "trixie/sid\n",
			"lsb-release": "DISTRIB_ID=Ubuntu\nDISTRIB_RELEASE=24.04\n",
		}},
		{"Alpine", "alpine 3.24.3", map[string]string{
			"os-release": "ID=alpine\nVERSION_ID=3.24\n", "alpine-release": "3.24.3\n",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for file, data := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, file), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.WithValue(context.Background(), common.EnvKey, common.EnvMap{common.HostEtcEnvKey: dir})
			info, err := host.InfoWithContext(ctx)
			if err != nil {
				t.Fatal(err)
			}
			legacy := strings.TrimSpace(strings.Join([]string{info.Platform, info.PlatformVersion}, " "))
			if legacy == "" {
				legacy = runtime.GOOS
			}
			if got := systemName(ctx, nil); got != legacy || got != tc.want {
				t.Fatalf("system name = %q, legacy = %q, want %q", got, legacy, tc.want)
			}
		})
	}
}
