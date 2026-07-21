package alerts

import "time"

const (
	MetricCPUPercent                 = "system.cpu.percent"
	MetricMemoryPercent              = "system.memory.percent"
	MetricDiskUsedPercent            = "disk.used.percent"
	MetricTemperatureCelsius         = "system.temperature.celsius"
	MetricServiceConsecutiveFailures = "service.consecutive_failures"
	MetricContainerHealthy           = "container.healthy"
	MetricContainerRestarts          = "container.restarts"
	MetricNodeOnline                 = "node.online"
	MetricBackupHealthy              = "backup.healthy"
)

// DefaultRules returns fresh values so callers may safely change selectors or
// enabled flags before inserting the rules.
func DefaultRules() []AlertRule {
	const cooldown = 30 * time.Minute
	return []AlertRule{
		{
			ID: "default_node_offline", Name: "Node is offline", ResourceType: "node",
			NodeSelector: WildcardSelector, ResourceSelector: WildcardSelector,
			Metric: MetricNodeOnline, Operator: OperatorLessThan, Threshold: 1,
			Severity: SeverityCritical, Cooldown: cooldown, Enabled: true,
		},
		{
			ID: "default_cpu_high", Name: "CPU usage is high", ResourceType: "host",
			NodeSelector: WildcardSelector, ResourceSelector: WildcardSelector,
			Metric: MetricCPUPercent, Operator: OperatorGreaterThan, Threshold: 90,
			For: 5 * time.Minute, Severity: SeverityWarning, Cooldown: cooldown, Enabled: true,
		},
		{
			ID: "default_memory_high", Name: "Memory usage is high", ResourceType: "host",
			NodeSelector: WildcardSelector, ResourceSelector: WildcardSelector,
			Metric: MetricMemoryPercent, Operator: OperatorGreaterThan, Threshold: 90,
			For: 5 * time.Minute, Severity: SeverityWarning, Cooldown: cooldown, Enabled: true,
		},
		{
			ID: "default_disk_high", Name: "Disk usage is high", ResourceType: "disk",
			NodeSelector: WildcardSelector, ResourceSelector: WildcardSelector,
			Metric: MetricDiskUsedPercent, Operator: OperatorGreaterThan, Threshold: 90,
			For: 10 * time.Minute, Severity: SeverityWarning, Cooldown: cooldown, Enabled: true,
		},
		{
			ID: "default_disk_critical", Name: "Disk usage is critical", ResourceType: "disk",
			NodeSelector: WildcardSelector, ResourceSelector: WildcardSelector,
			Metric: MetricDiskUsedPercent, Operator: OperatorGreaterThan, Threshold: 95,
			For: 10 * time.Minute, Severity: SeverityCritical, Cooldown: cooldown, Enabled: true,
		},
		{
			ID: "default_temperature_high", Name: "Host temperature is high", ResourceType: "host",
			NodeSelector: WildcardSelector, ResourceSelector: WildcardSelector,
			Metric: MetricTemperatureCelsius, Operator: OperatorGreaterThan, Threshold: 80,
			For: 5 * time.Minute, Severity: SeverityWarning, Cooldown: cooldown, Enabled: true,
		},
		{
			ID: "default_service_down", Name: "Service is down", ResourceType: "service",
			NodeSelector: WildcardSelector, ResourceSelector: WildcardSelector,
			Metric: MetricServiceConsecutiveFailures, Operator: OperatorGreaterThanOrEqual, Threshold: 2,
			Severity: SeverityCritical, Cooldown: cooldown, Enabled: true,
		},
		{
			ID: "default_container_unhealthy", Name: "Container is unhealthy", ResourceType: "container",
			NodeSelector: WildcardSelector, ResourceSelector: WildcardSelector,
			Metric: MetricContainerHealthy, Operator: OperatorLessThan, Threshold: 1,
			Severity: SeverityCritical, Cooldown: cooldown, Enabled: true,
		},
		{
			ID: "default_container_restarts", Name: "Container restart loop detected", ResourceType: "container",
			NodeSelector: WildcardSelector, ResourceSelector: WildcardSelector,
			Metric: MetricContainerRestarts, Operator: OperatorGreaterThan, Threshold: 3,
			Severity: SeverityCritical, Cooldown: cooldown, Enabled: true,
		},
		{
			ID: "default_backup_unhealthy", Name: "Backup is unhealthy", ResourceType: "backup",
			NodeSelector: WildcardSelector, ResourceSelector: WildcardSelector,
			Metric: MetricBackupHealthy, Operator: OperatorLessThan, Threshold: 1,
			Severity: SeverityWarning, Cooldown: cooldown, Enabled: true,
		},
	}
}
