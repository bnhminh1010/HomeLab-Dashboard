package model

import "time"

type ServiceStatus string

const (
	ServiceStatusUnknown  ServiceStatus = "unknown"
	ServiceStatusUp       ServiceStatus = "up"
	ServiceStatusDegraded ServiceStatus = "degraded"
	ServiceStatusDown     ServiceStatus = "down"
)

type ServiceInput struct {
	Name       string `json:"name"`
	Icon       string `json:"icon,omitempty"`
	DisplayURL string `json:"displayUrl"`
	URL        string `json:"url,omitempty"`
	Port       string `json:"port,omitempty"`
	ProbeURL   string `json:"probeUrl,omitempty"`
}

type Service struct {
	ID                  string        `json:"id"`
	Name                string        `json:"name"`
	Icon                string        `json:"icon,omitempty"`
	DisplayURL          string        `json:"displayUrl"`
	ProbeURL            string        `json:"probeUrl,omitempty"`
	Status              ServiceStatus `json:"status"`
	ConsecutiveFailures int           `json:"consecutiveFailures,omitempty"`
	LastCheckedAt       *time.Time    `json:"lastCheckedAt,omitempty"`
	LatencyMS           *int64        `json:"latencyMs,omitempty"`
	CreatedAt           time.Time     `json:"createdAt"`
	UpdatedAt           time.Time     `json:"updatedAt"`
}

type CPUStats struct {
	UsagePercent       float64  `json:"usagePercent"`
	Cores              int      `json:"cores"`
	FrequencyMHz       float64  `json:"frequencyMHz"`
	TemperatureCelsius *float64 `json:"temperatureCelsius"`
}

type MemoryStats struct {
	TotalBytes     uint64 `json:"totalBytes"`
	UsedBytes      uint64 `json:"usedBytes"`
	AvailableBytes uint64 `json:"availableBytes"`
	SwapTotalBytes uint64 `json:"swapTotalBytes"`
	SwapUsedBytes  uint64 `json:"swapUsedBytes"`
	ZRAMUsedBytes  uint64 `json:"zramUsedBytes"`
}

type SystemStats struct {
	Hostname      string      `json:"hostname"`
	OS            string      `json:"os"`
	Kernel        string      `json:"kernel"`
	UptimeSeconds uint64      `json:"uptimeSeconds"`
	ProcessCount  int         `json:"processCount"`
	LoadAverages  [3]float64  `json:"loadAverages"`
	CPU           CPUStats    `json:"cpu"`
	Memory        MemoryStats `json:"memory"`
}

type DiskStats struct {
	MountPoint          string  `json:"mountPoint"`
	Device              string  `json:"device"`
	TotalBytes          uint64  `json:"totalBytes"`
	UsedBytes           uint64  `json:"usedBytes"`
	UsagePercent        float64 `json:"usagePercent"`
	ReadBytesPerSecond  float64 `json:"readBytesPerSecond"`
	WriteBytesPerSecond float64 `json:"writeBytesPerSecond"`
}

type NetworkStats struct {
	Interface        string  `json:"interface"`
	RXBytesPerSecond float64 `json:"rxBytesPerSecond"`
	TXBytesPerSecond float64 `json:"txBytesPerSecond"`
}

type ContainerActions struct {
	Logs bool `json:"logs"`
	Exec bool `json:"exec"`
}

type Container struct {
	ID                   string           `json:"id"`
	Name                 string           `json:"name"`
	Image                string           `json:"image"`
	State                string           `json:"state"`
	Health               string           `json:"health,omitempty"`
	UptimeSeconds        uint64           `json:"uptimeSeconds"`
	CPUUsagePercent      float64          `json:"cpuUsagePercent"`
	CPUNormalizedPercent float64          `json:"cpuNormalizedPercent"`
	MemoryUsageBytes     uint64           `json:"memoryUsageBytes"`
	MemoryLimitBytes     uint64           `json:"memoryLimitBytes"`
	Ports                []string         `json:"ports"`
	RestartCount         uint64           `json:"restartCount"`
	Actions              ContainerActions `json:"actions"`
}

type Alert struct {
	ID         string    `json:"id"`
	Level      string    `json:"level"`
	Source     string    `json:"source"`
	Message    string    `json:"message"`
	OccurredAt time.Time `json:"occurredAt"`
}

type SnapshotData struct {
	System     SystemStats  `json:"system"`
	Disks      []DiskStats  `json:"disks"`
	Network    NetworkStats `json:"network"`
	Services   []Service    `json:"services"`
	Containers []Container  `json:"containers"`
	Alerts     []Alert      `json:"alerts"`
}

type SnapshotEnvelope struct {
	Version          int          `json:"version"`
	Type             string       `json:"type"`
	Sequence         uint64       `json:"seq"`
	CollectedAt      time.Time    `json:"collectedAt"`
	Truncated        bool         `json:"truncated,omitempty"`
	TruncatedSources []string     `json:"truncatedSources,omitempty"`
	StaleSources     []string     `json:"staleSources,omitempty"`
	Data             SnapshotData `json:"data"`
}

type AuditEvent struct {
	ID         int64          `json:"id"`
	Actor      string         `json:"actor"`
	Action     string         `json:"action"`
	TargetType string         `json:"targetType"`
	TargetID   string         `json:"targetId"`
	Outcome    string         `json:"outcome"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	CreatedAt  time.Time      `json:"createdAt"`
}
