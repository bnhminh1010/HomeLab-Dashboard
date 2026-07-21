package httpapi

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/bnhminh1010/homelab-dashboard/internal/model"
)

// marshalLegacySnapshot keeps the unversioned /ws/metrics contract from the
// original dashboard plan while /ws/v1/metrics exposes the richer envelope.
func marshalLegacySnapshot(snapshot model.SnapshotEnvelope, limit int) ([]byte, error) {
	truncated := false
	for {
		payload, err := json.Marshal(legacySnapshot(snapshot.Data, truncated))
		if err != nil {
			return nil, err
		}
		if len(payload) <= limit {
			return payload, nil
		}
		truncated = true
		switch {
		case len(snapshot.Data.Containers) > 0:
			snapshot.Data.Containers = snapshot.Data.Containers[:len(snapshot.Data.Containers)/2]
		case len(snapshot.Data.Services) > 0:
			snapshot.Data.Services = snapshot.Data.Services[:len(snapshot.Data.Services)/2]
		case len(snapshot.Data.Alerts) > 0:
			snapshot.Data.Alerts = snapshot.Data.Alerts[:len(snapshot.Data.Alerts)/2]
		default:
			return nil, fmt.Errorf("legacy metrics snapshot exceeds %d bytes without optional items", limit)
		}
	}
}

func legacySnapshot(data model.SnapshotData, truncated bool) map[string]any {
	disks := make(map[string]any, len(data.Disks))
	for _, disk := range data.Disks {
		disks[disk.MountPoint] = map[string]any{
			"total":    disk.TotalBytes / (1024 * 1024),
			"used":     disk.UsedBytes / (1024 * 1024),
			"percent":  disk.UsagePercent,
			"device":   disk.Device,
			"readBps":  disk.ReadBytesPerSecond,
			"writeBps": disk.WriteBytesPerSecond,
		}
	}
	services := make([]map[string]any, 0, len(data.Services))
	for _, service := range data.Services {
		port := ""
		if parsed, err := url.Parse(service.DisplayURL); err == nil {
			port = parsed.Port()
		}
		services = append(services, map[string]any{
			"id": service.ID, "name": service.Name, "url": service.DisplayURL,
			"icon": service.Icon, "port": port, "status": service.Status,
		})
	}
	containers := make([]map[string]any, 0, len(data.Containers))
	for _, container := range data.Containers {
		containers = append(containers, map[string]any{
			"id": container.ID, "name": container.Name, "image": container.Image,
			"status": container.State, "uptime": container.UptimeSeconds,
			"cpuPercent": container.CPUUsagePercent, "memoryUsage": container.MemoryUsageBytes,
			"memoryLimit": container.MemoryLimitBytes, "ports": container.Ports,
			"restarts": container.RestartCount, "actions": container.Actions,
		})
	}
	alerts := make([]map[string]any, 0, len(data.Alerts))
	for _, alert := range data.Alerts {
		alerts = append(alerts, map[string]any{
			"level": alert.Level, "message": alert.Message, "source": alert.Source,
			"timestamp": alert.OccurredAt.Unix(),
		})
	}
	payload := map[string]any{
		"system": map[string]any{
			"hostname": data.System.Hostname,
			"uptime":   data.System.UptimeSeconds,
			"os":       data.System.OS,
			"cpu": map[string]any{
				"percent": data.System.CPU.UsagePercent, "cores": data.System.CPU.Cores,
				"freq": data.System.CPU.FrequencyMHz, "temp": data.System.CPU.TemperatureCelsius,
			},
			"memory": map[string]any{
				"total": data.System.Memory.TotalBytes / 1024, "used": data.System.Memory.UsedBytes / 1024,
				"available": data.System.Memory.AvailableBytes / 1024,
				"swapTotal": data.System.Memory.SwapTotalBytes / 1024,
				"swapUsed":  data.System.Memory.SwapUsedBytes / 1024,
				"zramUsed":  data.System.Memory.ZRAMUsedBytes / 1024,
			},
			"disk": disks,
			"network": map[string]any{
				"bytesSent": data.Network.TXBytesPerSecond,
				"bytesRecv": data.Network.RXBytesPerSecond,
			},
			"load": data.System.LoadAverages, "processes": data.System.ProcessCount,
		},
		"services": services, "containers": containers, "alerts": alerts,
	}
	if truncated {
		payload["truncated"] = true
	}
	return payload
}
