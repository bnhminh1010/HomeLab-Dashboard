// Package containers converts the Podman API representation into the stable
// dashboard wire model. It deliberately exposes no lifecycle mutations.
package containers

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/model"
	"github.com/binhminh/HomeLab-Minh/internal/podman"
)

type Client interface {
	ListContainers(context.Context, bool) ([]podman.Container, error)
	Stats(context.Context, bool) ([]podman.ContainerStats, error)
	InspectContainer(context.Context, string) (podman.ContainerDetails, error)
}

type Collector struct {
	client Client
	now    func() time.Time
}

func New(client Client) *Collector {
	return &Collector{client: client, now: time.Now}
}

func (c *Collector) Collect(ctx context.Context, hostCores int) ([]model.Container, []model.Alert, error) {
	listed, err := c.client.ListContainers(ctx, true)
	if err != nil {
		return nil, nil, fmt.Errorf("list Podman containers: %w", err)
	}
	stats, statsErr := c.client.Stats(ctx, true)
	byID := make(map[string]podman.ContainerStats, len(stats))
	byName := make(map[string]podman.ContainerStats, len(stats))
	for _, item := range stats {
		byID[item.ID] = item
		byName[item.Name] = item
	}

	now := c.now().UTC()
	result := make([]model.Container, 0, len(listed))
	alerts := make([]model.Alert, 0)
	for _, item := range listed {
		stat, ok := byID[item.ID]
		if !ok {
			stat, ok = byName[item.Name]
		}
		details, inspectErr := c.client.InspectContainer(ctx, item.ID)
		state := strings.ToLower(item.State)
		health := ""
		restarts := uint64(0)
		if inspectErr == nil {
			if details.State != "" {
				state = strings.ToLower(details.State)
			}
			health = strings.ToLower(details.Health)
			restarts = details.RestartCount
		}
		runtimeState := state
		if restarts > 3 {
			state = "crashed"
		}
		uptime := uint64(0)
		if runtimeState == "running" && inspectErr == nil && !details.StartedAt.IsZero() && now.After(details.StartedAt) {
			uptime = uint64(now.Sub(details.StartedAt).Seconds())
		} else if runtimeState == "running" && item.Created > 0 && now.Unix() > item.Created {
			uptime = uint64(now.Unix() - item.Created)
		}
		normalizedCPU := stat.CPUPercent
		if hostCores > 0 {
			normalizedCPU = stat.CPUPercent / float64(hostCores)
		}
		result = append(result, model.Container{
			ID:                   item.ID,
			Name:                 item.Name,
			Image:                item.Image,
			State:                state,
			Health:               health,
			UptimeSeconds:        uptime,
			CPUUsagePercent:      stat.CPUPercent,
			CPUNormalizedPercent: clamp(normalizedCPU),
			MemoryUsageBytes:     stat.MemoryUsage,
			MemoryLimitBytes:     stat.MemoryLimit,
			Ports:                formatPorts(item.Ports),
			RestartCount:         restarts,
			Actions: model.ContainerActions{
				Logs: !item.Protected,
				Exec: runtimeState == "running" && restarts <= 3 && !item.Protected,
			},
		})
		if health == "unhealthy" || runtimeState == "restarting" || restarts > 3 {
			message := "Container is " + firstNonEmpty(health, runtimeState)
			if restarts > 3 {
				message = fmt.Sprintf("Container restart loop detected (%d restarts)", restarts)
			}
			alerts = append(alerts, model.Alert{
				ID:         "container:" + item.ID,
				Level:      "error",
				Source:     item.Name,
				Message:    message,
				OccurredAt: now,
			})
		}
	}
	if statsErr != nil {
		alerts = append(alerts, model.Alert{
			ID: "podman:stats", Level: "warning", Source: "podman",
			Message: "Container resource statistics are unavailable", OccurredAt: now,
		})
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name) })
	return result, alerts, nil
}

func formatPorts(ports []podman.Port) []string {
	formatted := make([]string, 0, len(ports))
	for _, port := range ports {
		protocol := port.Protocol
		if protocol == "" {
			protocol = "tcp"
		}
		target := fmt.Sprintf("%d/%s", port.ContainerPort, protocol)
		if port.HostPort > 0 {
			host := port.HostIP
			if host == "" {
				host = "host"
			}
			target += fmt.Sprintf(" → %s:%d", host, port.HostPort)
		}
		formatted = append(formatted, target)
	}
	return formatted
}

func clamp(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "unavailable"
}
