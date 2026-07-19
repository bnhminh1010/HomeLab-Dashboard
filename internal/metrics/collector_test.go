package metrics

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLinuxCollectorCalculatesRatesAndExplicitUnits(t *testing.T) {
	root := t.TempDir()
	proc := filepath.Join(root, "proc")
	sys := filepath.Join(root, "sys")
	hostRoot := filepath.Join(root, "host-root")
	for _, path := range []string{
		filepath.Join(proc, "net"), filepath.Join(proc, "sys", "kernel"), filepath.Join(proc, "1", "net"),
		filepath.Join(sys, "class", "thermal", "thermal_zone0"),
		filepath.Join(sys, "devices", "system", "cpu", "cpu0", "cpufreq"),
		filepath.Join(hostRoot, "etc"),
	} {
		if err := os.MkdirAll(path, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture(t, filepath.Join(proc, "stat"), "cpu  100 0 50 850 0 0 0 0\ncpu0 50 0 25 425 0 0 0 0\ncpu1 50 0 25 425 0 0 0 0\n")
	writeFixture(t, filepath.Join(proc, "meminfo"), "MemTotal: 1000 kB\nMemAvailable: 400 kB\nSwapTotal: 200 kB\nSwapFree: 150 kB\n")
	writeFixture(t, filepath.Join(proc, "uptime"), "123.90 0\n")
	writeFixture(t, filepath.Join(proc, "loadavg"), "0.10 0.20 0.30 1/1 1\n")
	writeFixture(t, filepath.Join(proc, "mounts"), "/dev/sda1 / ext4 rw 0 0\n")
	writeFixture(t, filepath.Join(proc, "diskstats"), "8 1 sda1 1 0 10 0 1 0 20 0 0 0 0 0 0 0 0\n")
	writeFixture(t, filepath.Join(proc, "1", "net", "route"), "Iface Destination Gateway Flags\neth0 00000000 00000000 0003\n")
	writeFixture(t, filepath.Join(proc, "1", "net", "dev"), "Inter-| Receive | Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\neth0: 1000 0 0 0 0 0 0 0 2000 0 0 0 0 0 0 0\n")
	writeFixture(t, filepath.Join(proc, "sys", "kernel", "osrelease"), "6.12.0\n")
	writeFixture(t, filepath.Join(proc, "cpuinfo"), "processor: 0\ncpu MHz: 1200\n")
	writeFixture(t, filepath.Join(sys, "class", "thermal", "thermal_zone0", "temp"), "46000\n")
	writeFixture(t, filepath.Join(sys, "devices", "system", "cpu", "cpu0", "cpufreq", "scaling_cur_freq"), "1200000\n")
	writeFixture(t, filepath.Join(hostRoot, "etc", "hostname"), "debian-server\n")
	writeFixture(t, filepath.Join(hostRoot, "etc", "os-release"), "PRETTY_NAME=\"Debian Test\"\n")

	now := time.Unix(1000, 0)
	collector, err := NewLinuxCollector(CollectorOptions{
		ProcPath: proc, SysPath: sys, RootPath: hostRoot, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.System.Memory.TotalBytes != 1000*1024 || first.System.Memory.UsedBytes != 600*1024 {
		t.Fatalf("memory bytes are wrong: %+v", first.System.Memory)
	}
	if first.System.Hostname != "debian-server" || first.System.CPU.Cores != 2 {
		t.Fatalf("unexpected system data: %+v", first.System)
	}
	if first.System.CPU.TemperatureCelsius == nil || *first.System.CPU.TemperatureCelsius != 46 {
		t.Fatalf("unexpected temperature: %v", first.System.CPU.TemperatureCelsius)
	}

	writeFixture(t, filepath.Join(proc, "stat"), "cpu  120 0 60 920 0 0 0 0\ncpu0 60 0 30 460 0 0 0 0\ncpu1 60 0 30 460 0 0 0 0\n")
	writeFixture(t, filepath.Join(proc, "diskstats"), "8 1 sda1 1 0 14 0 1 0 28 0 0 0 0 0 0 0 0\n")
	writeFixture(t, filepath.Join(proc, "1", "net", "dev"), "eth0: 3048 0 0 0 0 0 0 0 6096 0 0 0 0 0 0 0\n")
	now = now.Add(2 * time.Second)
	second, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.System.CPU.UsagePercent != 30 {
		t.Fatalf("cpu usage: got %v want 30", second.System.CPU.UsagePercent)
	}
	if second.Disks[0].ReadBytesPerSecond != 1024 || second.Disks[0].WriteBytesPerSecond != 2048 {
		t.Fatalf("disk rates: %+v", second.Disks[0])
	}
	if second.Network.RXBytesPerSecond != 1024 || second.Network.TXBytesPerSecond != 2048 {
		t.Fatalf("network rates: %+v", second.Network)
	}
}

func TestHostRootDevicePrefersHostPIDOneMountTable(t *testing.T) {
	proc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proc, "1"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(proc, "mounts"), "overlay / overlay rw 0 0\n")
	writeFixture(t, filepath.Join(proc, "1", "mounts"), "/dev/nvme0n1p7 / btrfs rw 0 0\n")
	if got := hostRootDevice(proc); got != "/dev/nvme0n1p7" {
		t.Fatalf("hostRootDevice()=%q want /dev/nvme0n1p7", got)
	}

	if err := os.Remove(filepath.Join(proc, "1", "mounts")); err != nil {
		t.Fatal(err)
	}
	if got := hostRootDevice(proc); got != "overlay" {
		t.Fatalf("fallback hostRootDevice()=%q want overlay", got)
	}
}

func writeFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o640); err != nil {
		t.Fatal(err)
	}
}
