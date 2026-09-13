package collector

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const diskSectorBytes = 512

type cpuCounters struct {
	total uint64
	steal uint64
}

type diskCounters struct {
	readSectors  uint64
	writeSectors uint64
	ioMillis     uint64
}

type linuxSampleState struct {
	mu            sync.Mutex
	previousCPU   *cpuCounters
	cpuAt         time.Time
	previousDisks map[string]diskCounters
	devices       []string
	diskAt        time.Time
}

type linuxSample struct {
	cpuStealPercent *float64
	diskReadRate    *float64
	diskWriteRate   *float64
	diskBusyPercent *float64
}

func (c *Collector) collectLinuxSample() linuxSample {
	if runtime.GOOS != "linux" && c.procStatPath == "" && c.diskstatPath == "" && c.sysBlockPath == "" {
		return linuxSample{}
	}
	procStat := c.procStatPath
	if procStat == "" {
		procStat = "/proc/stat"
	}
	diskstats := c.diskstatPath
	if diskstats == "" {
		diskstats = "/proc/diskstats"
	}
	sysBlock := c.sysBlockPath
	if sysBlock == "" {
		sysBlock = "/sys/block"
	}
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	currentCPU, cpuOK := readCPUCounters(procStat)
	devices, devicesOK := selectTopLevelBlockDevices(sysBlock)
	currentDisks, disksOK := readDiskCounters(diskstats, devices)
	diskOK := devicesOK && disksOK
	c.linuxState.mu.Lock()
	defer c.linuxState.mu.Unlock()
	result := linuxSample{}
	if cpuOK {
		previous, previousAt := c.linuxState.previousCPU, c.linuxState.cpuAt
		c.linuxState.previousCPU, c.linuxState.cpuAt = &currentCPU, now
		if previous != nil {
			seconds := now.Sub(previousAt).Seconds()
			cpuTotal, okTotal := counterDelta(previous.total, currentCPU.total)
			cpuSteal, okSteal := counterDelta(previous.steal, currentCPU.steal)
			if seconds > 0 && okTotal && okSteal && cpuTotal > 0 && cpuSteal <= cpuTotal {
				steal := float64(cpuSteal) / float64(cpuTotal) * 100
				result.cpuStealPercent = &steal
			}
		}
	} else {
		c.linuxState.previousCPU, c.linuxState.cpuAt = nil, time.Time{}
	}
	if !diskOK {
		c.linuxState.previousDisks, c.linuxState.devices, c.linuxState.diskAt = nil, nil, time.Time{}
		return result
	}
	previous, previousDevices, previousAt := c.linuxState.previousDisks, c.linuxState.devices, c.linuxState.diskAt
	c.linuxState.previousDisks, c.linuxState.devices, c.linuxState.diskAt = currentDisks, devices, now
	if previous == nil || !sameStrings(previousDevices, devices) {
		return result
	}
	seconds := now.Sub(previousAt).Seconds()
	if seconds <= 0 {
		return result
	}
	var readSectors, writeSectors uint64
	busy := float64(0)
	for _, name := range devices {
		old, next := previous[name], currentDisks[name]
		readDelta, okRead := counterDelta(old.readSectors, next.readSectors)
		writeDelta, okWrite := counterDelta(old.writeSectors, next.writeSectors)
		busyDelta, okBusy := counterDelta(old.ioMillis, next.ioMillis)
		if !okRead || !okWrite || !okBusy {
			return result
		}
		readSectors += readDelta
		writeSectors += writeDelta
		deviceBusy := float64(busyDelta) / (seconds * 1000) * 100
		if deviceBusy > busy {
			busy = deviceBusy
		}
	}
	if busy > 100 {
		busy = 100
	}
	readRate := float64(readSectors*diskSectorBytes) / seconds
	writeRate := float64(writeSectors*diskSectorBytes) / seconds
	result.diskReadRate, result.diskWriteRate, result.diskBusyPercent = &readRate, &writeRate, &busy
	return result
}

func readCPUCounters(path string) (cpuCounters, bool) {
	file, err := os.Open(path)
	if err != nil {
		return cpuCounters{}, false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		return cpuCounters{}, false
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) < 9 || fields[0] != "cpu" {
		return cpuCounters{}, false
	}
	values := make([]uint64, 8)
	for index := range values {
		value, err := strconv.ParseUint(fields[index+1], 10, 64)
		if err != nil {
			return cpuCounters{}, false
		}
		values[index] = value
	}
	var total uint64
	for _, value := range values {
		total += value
	}
	// guest and guest_nice are already included in user and nice, so fields 9+
	// must not be included in the total a second time.
	return cpuCounters{total: total, steal: values[7]}, true
}

func selectTopLevelBlockDevices(sysBlock string) ([]string, bool) {
	entries, err := os.ReadDir(sysBlock)
	if err != nil {
		return nil, false
	}
	devices := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if isPseudoBlockDevice(name) {
			continue
		}
		devicePath := filepath.Join(sysBlock, name)
		holders, err := os.ReadDir(filepath.Join(devicePath, "holders"))
		if err != nil {
			return nil, false
		}
		if len(holders) != 0 {
			continue
		}
		children, err := os.ReadDir(devicePath)
		if err != nil {
			return nil, false
		}
		partitions := make([]string, 0)
		partitionIsMapped := false
		for _, child := range children {
			partitionPath := filepath.Join(devicePath, child.Name())
			if _, err := os.Stat(filepath.Join(partitionPath, "partition")); err != nil {
				continue
			}
			partitionHolders, err := os.ReadDir(filepath.Join(partitionPath, "holders"))
			if err != nil {
				return nil, false
			}
			if len(partitionHolders) != 0 {
				partitionIsMapped = true
				continue
			}
			partitions = append(partitions, child.Name())
		}
		if partitionIsMapped {
			// The whole-disk counter includes mapped partitions and would double
			// count them with dm/md. Count only unmapped sibling partitions.
			devices = append(devices, partitions...)
		} else {
			devices = append(devices, name)
		}
	}
	sort.Strings(devices)
	return devices, len(devices) > 0
}

func isPseudoBlockDevice(name string) bool {
	for _, prefix := range []string{"loop", "ram", "fd", "sr", "zram"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func readDiskCounters(path string, devices []string) (map[string]diskCounters, bool) {
	wanted := make(map[string]struct{}, len(devices))
	for _, name := range devices {
		wanted[name] = struct{}{}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	result := make(map[string]diskCounters, len(devices))
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 14 {
			continue
		}
		name := fields[2]
		if _, ok := wanted[name]; !ok {
			continue
		}
		readSectors, errRead := strconv.ParseUint(fields[5], 10, 64)
		writeSectors, errWrite := strconv.ParseUint(fields[9], 10, 64)
		ioMillis, errBusy := strconv.ParseUint(fields[12], 10, 64)
		if errRead != nil || errWrite != nil || errBusy != nil {
			return nil, false
		}
		result[name] = diskCounters{readSectors, writeSectors, ioMillis}
	}
	if scanner.Err() != nil || len(result) != len(devices) {
		return nil, false
	}
	return result, true
}

func counterDelta(previous, current uint64) (uint64, bool) {
	if current < previous {
		return 0, false
	}
	return current - previous, true
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
