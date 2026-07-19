package metrics

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/model"
)

type HostSnapshot struct {
	System  model.SystemStats
	Disks   []model.DiskStats
	Network model.NetworkStats
}

type HostCollector interface {
	Collect(context.Context) (HostSnapshot, error)
}

type CollectorOptions struct {
	ProcPath         string
	SysPath          string
	RootPath         string
	NetworkInterface string
	Now              func() time.Time
}

type LinuxCollector struct {
	options CollectorOptions

	mu       sync.Mutex
	previous *rawSample
}

type rawSample struct {
	at        time.Time
	cpuTotal  uint64
	cpuIdle   uint64
	diskRead  uint64
	diskWrite uint64
	networkRX uint64
	networkTX uint64
}

func NewLinuxCollector(options CollectorOptions) (*LinuxCollector, error) {
	if options.ProcPath == "" || options.SysPath == "" || options.RootPath == "" {
		return nil, fmt.Errorf("proc, sys and root paths are required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &LinuxCollector{options: options}, nil
}

func (c *LinuxCollector) Collect(ctx context.Context) (HostSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return HostSnapshot{}, err
	}
	cpuTotal, cpuIdle, cores, err := readCPU(filepath.Join(c.options.ProcPath, "stat"))
	if err != nil {
		return HostSnapshot{}, err
	}
	memory, err := readMemory(filepath.Join(c.options.ProcPath, "meminfo"))
	if err != nil {
		return HostSnapshot{}, err
	}
	memory.ZRAMUsedBytes = readZRAM(c.options.SysPath)
	uptime, err := readUptime(filepath.Join(c.options.ProcPath, "uptime"))
	if err != nil {
		return HostSnapshot{}, err
	}
	load, err := readLoad(filepath.Join(c.options.ProcPath, "loadavg"))
	if err != nil {
		return HostSnapshot{}, err
	}
	device := hostRootDevice(c.options.ProcPath)
	disk, err := readDisk(c.options.RootPath, device)
	if err != nil {
		return HostSnapshot{}, err
	}
	diskRead, diskWrite := readDiskCounters(filepath.Join(c.options.ProcPath, "diskstats"), device)
	// /proc/net follows the dashboard process namespace. PID 1 under the host
	// proc mount represents the host network namespace when it is accessible.
	networkRoot := filepath.Join(c.options.ProcPath, "1", "net")
	if _, err := os.Stat(filepath.Join(networkRoot, "dev")); err != nil {
		networkRoot = filepath.Join(c.options.ProcPath, "net")
	}
	iface := c.options.NetworkInterface
	if iface == "" {
		iface = defaultInterface(filepath.Join(networkRoot, "route"))
		if iface == "" {
			iface = firstNetworkInterface(filepath.Join(networkRoot, "dev"))
		}
	}
	rx, tx, err := readNetworkCounters(filepath.Join(networkRoot, "dev"), iface)
	if err != nil {
		return HostSnapshot{}, err
	}
	now := c.options.Now().UTC()
	current := rawSample{
		at: now, cpuTotal: cpuTotal, cpuIdle: cpuIdle, diskRead: diskRead,
		diskWrite: diskWrite, networkRX: rx, networkTX: tx,
	}

	c.mu.Lock()
	previous := c.previous
	c.previous = &current
	c.mu.Unlock()

	var cpuUsage, readRate, writeRate, rxRate, txRate float64
	if previous != nil {
		totalDelta := counterDelta(cpuTotal, previous.cpuTotal)
		idleDelta := counterDelta(cpuIdle, previous.cpuIdle)
		if totalDelta > 0 {
			activeDelta := uint64(0)
			if totalDelta > idleDelta {
				activeDelta = totalDelta - idleDelta
			}
			cpuUsage = clamp(100*float64(activeDelta)/float64(totalDelta), 0, 100)
		}
		seconds := now.Sub(previous.at).Seconds()
		if seconds > 0 {
			readRate = float64(counterDelta(diskRead, previous.diskRead)) / seconds
			writeRate = float64(counterDelta(diskWrite, previous.diskWrite)) / seconds
			rxRate = float64(counterDelta(rx, previous.networkRX)) / seconds
			txRate = float64(counterDelta(tx, previous.networkTX)) / seconds
		}
	}
	disk.ReadBytesPerSecond = readRate
	disk.WriteBytesPerSecond = writeRate

	hostname := firstLine(filepath.Join(c.options.RootPath, "etc", "hostname"))
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	temperature := readTemperature(c.options.SysPath)
	return HostSnapshot{
		System: model.SystemStats{
			Hostname:      hostname,
			OS:            firstOSName(filepath.Join(c.options.RootPath, "etc", "os-release")),
			Kernel:        firstLine(filepath.Join(c.options.ProcPath, "sys", "kernel", "osrelease")),
			UptimeSeconds: uptime,
			ProcessCount:  processCount(c.options.ProcPath),
			LoadAverages:  load,
			CPU: model.CPUStats{
				UsagePercent: cpuUsage, Cores: cores, FrequencyMHz: readFrequency(c.options.ProcPath, c.options.SysPath),
				TemperatureCelsius: temperature,
			},
			Memory: memory,
		},
		Disks:   []model.DiskStats{disk},
		Network: model.NetworkStats{Interface: iface, RXBytesPerSecond: rxRate, TXBytesPerSecond: txRate},
	}, nil
}

func readCPU(path string) (total, idle uint64, cores int, err error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("read cpu stats: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "cpu" {
			for index, field := range fields[1:] {
				value, parseErr := strconv.ParseUint(field, 10, 64)
				if parseErr != nil {
					return 0, 0, 0, fmt.Errorf("parse cpu stats: %w", parseErr)
				}
				total += value
				if index == 3 || index == 4 {
					idle += value
				}
			}
		} else if len(fields[0]) > 3 && strings.HasPrefix(fields[0], "cpu") {
			if _, parseErr := strconv.Atoi(fields[0][3:]); parseErr == nil {
				cores++
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, 0, fmt.Errorf("read cpu stats: %w", err)
	}
	if total == 0 {
		return 0, 0, 0, fmt.Errorf("cpu aggregate is missing from %s", path)
	}
	if cores == 0 {
		cores = runtime.NumCPU()
	}
	return total, idle, cores, nil
}

func readMemory(path string) (model.MemoryStats, error) {
	values, err := readKeyValues(path)
	if err != nil {
		return model.MemoryStats{}, fmt.Errorf("read memory stats: %w", err)
	}
	kib := func(key string) uint64 { return values[key] * 1024 }
	total := kib("MemTotal")
	available := kib("MemAvailable")
	if available == 0 {
		available = kib("MemFree") + kib("Buffers") + kib("Cached")
	}
	used := uint64(0)
	if total > available {
		used = total - available
	}
	swapTotal, swapFree := kib("SwapTotal"), kib("SwapFree")
	swapUsed := uint64(0)
	if swapTotal > swapFree {
		swapUsed = swapTotal - swapFree
	}
	return model.MemoryStats{
		TotalBytes: total, UsedBytes: used, AvailableBytes: available,
		SwapTotalBytes: swapTotal, SwapUsedBytes: swapUsed,
	}, nil
}

func readKeyValues(path string) (map[string]uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	values := make(map[string]uint64)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err == nil {
			values[strings.TrimSuffix(fields[0], ":")] = value
		}
	}
	return values, scanner.Err()
}

func readUptime(path string) (uint64, error) {
	fields := strings.Fields(firstLine(path))
	if len(fields) == 0 {
		return 0, fmt.Errorf("read uptime from %s", path)
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("parse uptime: %w", err)
	}
	return uint64(math.Max(seconds, 0)), nil
}

func readLoad(path string) ([3]float64, error) {
	fields := strings.Fields(firstLine(path))
	if len(fields) < 3 {
		return [3]float64{}, fmt.Errorf("read load averages from %s", path)
	}
	var load [3]float64
	for index := range load {
		value, err := strconv.ParseFloat(fields[index], 64)
		if err != nil {
			return [3]float64{}, fmt.Errorf("parse load average: %w", err)
		}
		load[index] = value
	}
	return load, nil
}

func readDisk(rootPath, device string) (model.DiskStats, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(rootPath, &stat); err != nil {
		return model.DiskStats{}, fmt.Errorf("read root filesystem: %w", err)
	}
	total := stat.Blocks * uint64(stat.Bsize)
	available := stat.Bavail * uint64(stat.Bsize)
	used := uint64(0)
	if total > available {
		used = total - available
	}
	percent := float64(0)
	if total > 0 {
		percent = 100 * float64(used) / float64(total)
	}
	return model.DiskStats{MountPoint: "/", Device: device, TotalBytes: total, UsedBytes: used, UsagePercent: percent}, nil
}

func rootDevice(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return "unknown"
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[1] == "/" {
			return strings.ReplaceAll(fields[0], `\040`, " ")
		}
	}
	return "unknown"
}

func hostRootDevice(procPath string) string {
	if device := rootDevice(filepath.Join(procPath, "1", "mounts")); device != "unknown" {
		return device
	}
	return rootDevice(filepath.Join(procPath, "mounts"))
}

func readDiskCounters(path, device string) (readBytes, writeBytes uint64) {
	name := filepath.Base(device)
	file, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 || fields[2] != name {
			continue
		}
		readSectors, _ := strconv.ParseUint(fields[5], 10, 64)
		writeSectors, _ := strconv.ParseUint(fields[9], 10, 64)
		return readSectors * 512, writeSectors * 512
	}
	return 0, 0
}

func defaultInterface(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 4 && fields[1] == "00000000" {
			flags, _ := strconv.ParseUint(fields[3], 16, 64)
			if flags&2 != 0 {
				return fields[0]
			}
		}
	}
	return ""
}

func readNetworkCounters(path, requested string) (rx, tx uint64, err error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, fmt.Errorf("read network stats: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		name := strings.TrimSpace(line[:colon])
		if requested == "" && name != "lo" {
			requested = name
		}
		if name != requested {
			continue
		}
		fields := strings.Fields(line[colon+1:])
		if len(fields) < 9 {
			break
		}
		rx, err = strconv.ParseUint(fields[0], 10, 64)
		if err == nil {
			tx, err = strconv.ParseUint(fields[8], 10, 64)
		}
		if err != nil {
			return 0, 0, fmt.Errorf("parse network stats: %w", err)
		}
		return rx, tx, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, fmt.Errorf("read network stats: %w", err)
	}
	if requested == "" {
		return 0, 0, nil
	}
	return 0, 0, fmt.Errorf("network interface %q not found", requested)
}

func firstNetworkInterface(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		name := strings.TrimSpace(line[:colon])
		if name != "" && name != "lo" {
			return name
		}
	}
	return ""
}

func readFrequency(procPath, sysPath string) float64 {
	entries, _ := filepath.Glob(filepath.Join(sysPath, "devices", "system", "cpu", "cpu*", "cpufreq", "scaling_cur_freq"))
	var total float64
	var count int
	for _, path := range entries {
		value, err := strconv.ParseFloat(strings.TrimSpace(firstLine(path)), 64)
		if err == nil && value > 0 {
			total += value / 1000
			count++
		}
	}
	if count > 0 {
		return total / float64(count)
	}
	file, err := os.Open(filepath.Join(procPath, "cpuinfo"))
	if err != nil {
		return 0
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), ":", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == "cpu MHz" {
			value, _ := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
			if value > 0 {
				total += value
				count++
			}
		}
	}
	if count == 0 {
		return 0
	}
	return total / float64(count)
}

func readTemperature(sysPath string) *float64 {
	paths, _ := filepath.Glob(filepath.Join(sysPath, "class", "thermal", "thermal_zone*", "temp"))
	var hottest float64
	for _, path := range paths {
		value, err := strconv.ParseFloat(strings.TrimSpace(firstLine(path)), 64)
		if err != nil {
			continue
		}
		if value > 1000 {
			value /= 1000
		}
		if value >= -20 && value <= 150 && value > hottest {
			hottest = value
		}
	}
	if hottest == 0 {
		return nil
	}
	return &hottest
}

func readZRAM(sysPath string) uint64 {
	paths, _ := filepath.Glob(filepath.Join(sysPath, "block", "zram*", "mm_stat"))
	var total uint64
	for _, path := range paths {
		fields := strings.Fields(firstLine(path))
		if len(fields) >= 3 {
			value, _ := strconv.ParseUint(fields[2], 10, 64)
			total += value
		}
	}
	return total
}

func processCount(procPath string) int {
	entries, err := os.ReadDir(procPath)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			if _, err := strconv.Atoi(entry.Name()); err == nil {
				count++
			}
		}
	}
	return count
}

func firstOSName(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return "Linux"
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "PRETTY_NAME=") {
			return strings.Trim(strings.TrimPrefix(scanner.Text(), "PRETTY_NAME="), `"'`)
		}
	}
	return "Linux"
}

func firstLine(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if scanner.Scan() {
		return strings.TrimSpace(scanner.Text())
	}
	return ""
}

func counterDelta(current, previous uint64) uint64 {
	if current < previous {
		return 0
	}
	return current - previous
}

func clamp(value, minValue, maxValue float64) float64 {
	return math.Min(math.Max(value, minValue), maxValue)
}
