package collector

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/net"
)

func TestInterfaceFiltering(t *testing.T) {
	c := Collector{}
	for _, name := range []string{"lo", "docker0", "veth123", "br-test", "wg0", "tailscale0"} {
		if c.includeInterface(name) {
			t.Errorf("default included %s", name)
		}
	}
	if !c.includeInterface("eth0") {
		t.Error("eth0 should be included")
	}
	c = Collector{Includes: []string{"wg*"}}
	if !c.includeInterface("wg0") || c.includeInterface("eth0") {
		t.Error("explicit include was not honored")
	}
	c = Collector{Includes: []string{"wg*"}, Excludes: []string{"wg-test"}}
	if c.includeInterface("wg-test") {
		t.Error("explicit exclude should win")
	}
}

func TestNetworkCounterAggregationFiltersAndSorts(t *testing.T) {
	c := Collector{Includes: []string{"eth*", "wg*"}, Excludes: []string{"wg-test"}}
	set, rx, tx, err := c.aggregateNetworkCounters([]net.IOCountersStat{
		{Name: "wg-test", BytesRecv: 100, BytesSent: 200},
		{Name: "eth1", BytesRecv: 30, BytesSent: 40},
		{Name: "eth0", BytesRecv: 10, BytesSent: 20},
		{Name: "lo", BytesRecv: 900, BytesSent: 800},
	})
	if err != nil {
		t.Fatal(err)
	}
	if set == nil || set.Version != 1 || len(set.Interfaces) != 2 || set.Interfaces[0].Name != "eth0" || set.Interfaces[1].Name != "eth1" {
		t.Fatalf("unexpected sorted counters: %+v", set)
	}
	if rx != 40 || tx != 60 {
		t.Fatalf("unexpected aggregates rx=%d tx=%d", rx, tx)
	}
}

func TestNetworkCounterAggregationIncludesEmptySetAndRejectsInvalidInput(t *testing.T) {
	c := Collector{Includes: []string{"eth*"}}
	set, rx, tx, err := c.aggregateNetworkCounters([]net.IOCountersStat{{Name: "lo", BytesRecv: 9}})
	if err != nil || set == nil || set.Interfaces == nil || len(set.Interfaces) != 0 || rx != 0 || tx != 0 {
		t.Fatalf("empty filtered set not represented explicitly: set=%+v rx=%d tx=%d err=%v", set, rx, tx, err)
	}
	for _, counters := range [][]net.IOCountersStat{
		{{Name: "eth0", BytesRecv: uint64(^uint64(0))}},
		{{Name: "eth0", BytesRecv: 1}, {Name: "eth0", BytesRecv: 2}},
		{{Name: "eth0", BytesRecv: uint64(1 << 63)}},
	} {
		if _, _, _, err := c.aggregateNetworkCounters(counters); err == nil {
			t.Fatalf("invalid network counters accepted: %+v", counters)
		}
	}
}

func TestLinuxTelemetrySelectsHighestLogicalLayerAndCalculatesDeltas(t *testing.T) {
	root := t.TempDir()
	sysBlock := filepath.Join(root, "sys", "block")
	for _, device := range []string{"sda", "dm-0", "nvme0n1", "loop0"} {
		if err := os.MkdirAll(filepath.Join(sysBlock, device, "holders"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(sysBlock, "sda", "holders", "dm-0"), 0o755); err != nil {
		t.Fatal(err)
	}
	procStat := filepath.Join(root, "stat")
	diskstats := filepath.Join(root, "diskstats")
	writeMetricsFixture(t, procStat, "cpu 100 0 50 800 0 0 0 10 20 0\n")
	writeMetricsFixture(t, diskstats, "8 0 sda 0 0 900 0 0 0 900 0 0 900 0\n8 1 sda1 0 0 800 0 0 0 800 0 0 800 0\n253 0 dm-0 0 0 100 0 0 0 200 0 0 1000 0\n259 0 nvme0n1 0 0 300 0 0 0 400 0 0 2000 0\n7 0 loop0 0 0 999 0 0 0 999 0 0 999 0\n")
	now := time.Unix(100, 0)
	c := Collector{procStatPath: procStat, diskstatPath: diskstats, sysBlockPath: sysBlock, now: func() time.Time { return now }}
	first := c.collectLinuxSample()
	if first.cpuStealPercent != nil || first.diskBusyPercent != nil {
		t.Fatal("first sample must establish a baseline without publishing rates")
	}
	now = now.Add(2 * time.Second)
	writeMetricsFixture(t, procStat, "cpu 110 0 50 880 0 0 0 20 999 999\n")
	writeMetricsFixture(t, diskstats, "8 0 sda 0 0 9999 0 0 0 9999 0 0 9999 0\n8 1 sda1 0 0 9999 0 0 0 9999 0 0 9999 0\n253 0 dm-0 0 0 110 0 0 0 220 0 0 1500 0\n259 0 nvme0n1 0 0 330 0 0 0 410 0 0 2200 0\n7 0 loop0 0 0 9999 0 0 0 9999 0 0 9999 0\n")
	second := c.collectLinuxSample()
	assertMetric(t, "cpu steal", second.cpuStealPercent, 10)
	assertMetric(t, "disk read rate", second.diskReadRate, 10240)
	assertMetric(t, "disk write rate", second.diskWriteRate, 7680)
	assertMetric(t, "busiest disk", second.diskBusyPercent, 25)
}

func TestLinuxTelemetryInvalidatesBaselineOnDeviceChangeAndCounterRollback(t *testing.T) {
	root := t.TempDir()
	sysBlock := filepath.Join(root, "sys", "block")
	if err := os.MkdirAll(filepath.Join(sysBlock, "vda", "holders"), 0o755); err != nil {
		t.Fatal(err)
	}
	procStat := filepath.Join(root, "stat")
	diskstats := filepath.Join(root, "diskstats")
	writeMetricsFixture(t, procStat, "cpu 10 0 0 90 0 0 0 0\n")
	writeMetricsFixture(t, diskstats, "252 0 vda 0 0 100 0 0 0 100 0 0 100 0\n")
	now := time.Unix(200, 0)
	c := Collector{procStatPath: procStat, diskstatPath: diskstats, sysBlockPath: sysBlock, now: func() time.Time { return now }}
	_ = c.collectLinuxSample()
	if err := os.MkdirAll(filepath.Join(sysBlock, "vdb", "holders"), 0o755); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	writeMetricsFixture(t, procStat, "cpu 20 0 0 180 0 0 0 0\n")
	writeMetricsFixture(t, diskstats, "252 0 vda 0 0 110 0 0 0 110 0 0 200 0\n252 16 vdb 0 0 10 0 0 0 10 0 0 10 0\n")
	changed := c.collectLinuxSample()
	if changed.diskReadRate != nil {
		t.Fatal("device set changes must rebuild the disk baseline")
	}
	assertMetric(t, "CPU across device change", changed.cpuStealPercent, 0)
	now = now.Add(time.Second)
	writeMetricsFixture(t, procStat, "cpu 5 0 0 20 0 0 0 0\n")
	writeMetricsFixture(t, diskstats, "252 0 vda 0 0 1 0 0 0 1 0 0 1 0\n252 16 vdb 0 0 1 0 0 0 1 0 0 1 0\n")
	rollback := c.collectLinuxSample()
	if rollback.diskBusyPercent != nil || rollback.cpuStealPercent != nil {
		t.Fatal("counter rollback must be unknown, not zero")
	}
}

func TestTopLevelDevicesAvoidPartitionBackedAndNestedMapperDoubleCounting(t *testing.T) {
	root := t.TempDir()
	sysBlock := filepath.Join(root, "sys", "block")
	for _, device := range []string{"sda", "dm-0", "dm-1"} {
		if err := os.MkdirAll(filepath.Join(sysBlock, device, "holders"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, partition := range []string{"sda1", "sda2"} {
		path := filepath.Join(sysBlock, "sda", partition)
		if err := os.MkdirAll(filepath.Join(path, "holders"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeMetricsFixture(t, filepath.Join(path, "partition"), "1\n")
	}
	if err := os.Mkdir(filepath.Join(sysBlock, "sda", "sda1", "holders", "dm-0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(sysBlock, "dm-0", "holders", "dm-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	devices, ok := selectTopLevelBlockDevices(sysBlock)
	if !ok {
		t.Fatal("device topology should be readable")
	}
	if got, want := strings.Join(devices, ","), "dm-1,sda2"; got != want {
		t.Fatalf("selected devices=%q want %q", got, want)
	}
}

func TestLinuxTelemetryKeepsCPUAndDiskBaselinesIndependent(t *testing.T) {
	root := t.TempDir()
	sysBlock := filepath.Join(root, "sys", "block")
	if err := os.MkdirAll(filepath.Join(sysBlock, "vda", "holders"), 0o755); err != nil {
		t.Fatal(err)
	}
	procStat := filepath.Join(root, "stat")
	diskstats := filepath.Join(root, "diskstats")
	now := time.Unix(300, 0)
	c := Collector{procStatPath: procStat, diskstatPath: diskstats, sysBlockPath: sysBlock, now: func() time.Time { return now }}
	writeMetricsFixture(t, procStat, "cpu 10 0 0 90 0 0 0 0\n")
	writeMetricsFixture(t, diskstats, "252 0 vda 0 0 100 0 0 0 100 0 0 100 0\n")
	_ = c.collectLinuxSample()

	now = now.Add(time.Second)
	writeMetricsFixture(t, procStat, "cpu 20 0 0 170 0 0 0 10\n")
	writeMetricsFixture(t, diskstats, "invalid\n")
	diskFailure := c.collectLinuxSample()
	assertMetric(t, "cpu during disk failure", diskFailure.cpuStealPercent, 10)
	if diskFailure.diskReadRate != nil {
		t.Fatal("disk read failure must clear and suppress the disk baseline")
	}

	now = now.Add(time.Second)
	writeMetricsFixture(t, procStat, "cpu 30 0 0 250 0 0 0 20\n")
	writeMetricsFixture(t, diskstats, "252 0 vda 0 0 120 0 0 0 120 0 0 200 0\n")
	recoveredDisk := c.collectLinuxSample()
	assertMetric(t, "cpu after disk recovery", recoveredDisk.cpuStealPercent, 10)
	if recoveredDisk.diskReadRate != nil {
		t.Fatal("first disk sample after failure must only rebuild its baseline")
	}

	now = now.Add(time.Second)
	writeMetricsFixture(t, procStat, "invalid\n")
	writeMetricsFixture(t, diskstats, "252 0 vda 0 0 130 0 0 0 130 0 0 300 0\n")
	cpuFailure := c.collectLinuxSample()
	if cpuFailure.cpuStealPercent != nil {
		t.Fatal("CPU read failure must clear and suppress the CPU baseline")
	}
	assertMetric(t, "disk during CPU failure", cpuFailure.diskReadRate, 5120)

	now = now.Add(time.Second)
	writeMetricsFixture(t, procStat, "cpu 50 0 0 330 0 0 0 20\n")
	writeMetricsFixture(t, diskstats, "252 0 vda 0 0 140 0 0 0 140 0 0 400 0\n")
	recoveredCPU := c.collectLinuxSample()
	if recoveredCPU.cpuStealPercent != nil {
		t.Fatal("first CPU sample after failure must only rebuild its baseline")
	}
	assertMetric(t, "disk after CPU recovery", recoveredCPU.diskReadRate, 5120)
}

func writeMetricsFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertMetric(t *testing.T, name string, value *float64, want float64) {
	t.Helper()
	if value == nil || *value != want {
		t.Fatalf("%s=%v want %v", name, value, want)
	}
}

func TestBootIdentityDoesNotSwitchToFallback(t *testing.T) {
	responses := []struct {
		id  string
		err error
	}{{id: "boot-uuid"}, {err: errors.New("temporary read failure")}, {id: "boot-uuid"}}
	index := 0
	c := Collector{bootIDReader: func() (string, error) {
		response := responses[index]
		index++
		return response.id, response.err
	}}
	if id, err := c.bootID(); err != nil || id != "boot-uuid" {
		t.Fatalf("first identity=%q err=%v", id, err)
	}
	if _, err := c.bootID(); err == nil {
		t.Fatal("temporary boot ID failure should skip the sample")
	}
	if id, err := c.bootID(); err != nil || id != "boot-uuid" {
		t.Fatalf("recovered identity=%q err=%v", id, err)
	}
	restarted := Collector{bootIDReader: func() (string, error) { return "boot-uuid", nil }}
	if id, err := restarted.bootID(); err != nil || id != "boot-uuid" {
		t.Fatalf("restart identity=%q err=%v", id, err)
	}
}
