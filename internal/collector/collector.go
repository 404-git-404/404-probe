package collector

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"404-probe/internal/protocol"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

var DefaultNetworkExcludes = []string{
	"lo", "docker*", "veth*", "br-*", "virbr*", "tun*", "tap*", "wg*",
	"tailscale*", "cni*", "flannel*",
}

type Collector struct {
	Includes     []string
	Excludes     []string
	bootIDReader func() (string, error)
}

func (c Collector) Collect(ctx context.Context) (protocol.Report, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return protocol.Report{}, fmt.Errorf("hostname: %w", err)
	}
	info, err := host.InfoWithContext(ctx)
	if err != nil {
		return protocol.Report{}, fmt.Errorf("host info: %w", err)
	}
	loads, err := load.AvgWithContext(ctx)
	if err != nil {
		return protocol.Report{}, fmt.Errorf("load: %w", err)
	}
	vm, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return protocol.Report{}, fmt.Errorf("memory: %w", err)
	}
	swap, err := mem.SwapMemoryWithContext(ctx)
	if err != nil {
		return protocol.Report{}, fmt.Errorf("swap: %w", err)
	}
	root, err := disk.UsageWithContext(ctx, "/")
	if err != nil {
		return protocol.Report{}, fmt.Errorf("root disk: %w", err)
	}
	percent, err := cpu.PercentWithContext(ctx, 0, false)
	if err != nil || len(percent) == 0 {
		if err == nil {
			err = fmt.Errorf("no CPU values")
		}
		return protocol.Report{}, fmt.Errorf("cpu: %w", err)
	}
	counters, err := net.IOCountersWithContext(ctx, true)
	if err != nil {
		return protocol.Report{}, fmt.Errorf("network: %w", err)
	}
	var rx, tx uint64
	for _, counter := range counters {
		if c.includeInterface(counter.Name) {
			rx += counter.BytesRecv
			tx += counter.BytesSent
		}
	}
	bootID, err := c.bootID()
	if err != nil {
		return protocol.Report{}, fmt.Errorf("boot ID: %w", err)
	}
	osName := strings.TrimSpace(strings.Join([]string{info.Platform, info.PlatformVersion}, " "))
	if osName == "" {
		osName = runtime.GOOS
	}
	return protocol.Report{
		Hostname: hostname, OS: osName, Arch: runtime.GOARCH, BootID: bootID, Uptime: info.Uptime,
		CPUPercent: percent[0], Load1: loads.Load1, Load5: loads.Load5, Load15: loads.Load15,
		RAMUsed: vm.Used, RAMTotal: vm.Total, RAMPercent: vm.UsedPercent,
		SwapUsed: swap.Used, SwapTotal: swap.Total, SwapPercent: swap.UsedPercent,
		DiskUsed: root.Used, DiskTotal: root.Total, DiskPercent: root.UsedPercent,
		RXBytes: rx, TXBytes: tx,
	}, nil
}

func (c Collector) includeInterface(name string) bool {
	if len(c.Includes) > 0 {
		for _, pattern := range c.Includes {
			if match(pattern, name) {
				return !matches(c.Excludes, name)
			}
		}
		return false
	}
	return !matches(append(append([]string{}, DefaultNetworkExcludes...), c.Excludes...), name)
}

func matches(patterns []string, name string) bool {
	for _, p := range patterns {
		if match(p, name) {
			return true
		}
	}
	return false
}

func match(pattern, name string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	ok, err := filepath.Match(pattern, name)
	return err == nil && ok
}

func (c Collector) bootID() (string, error) {
	reader := c.bootIDReader
	if reader == nil {
		reader = readBootID
	}
	bootID, err := reader()
	if err != nil {
		return "", err
	}
	bootID = strings.TrimSpace(bootID)
	if bootID == "" {
		return "", errors.New("boot ID is empty")
	}
	return bootID, nil
}

func readBootID() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
