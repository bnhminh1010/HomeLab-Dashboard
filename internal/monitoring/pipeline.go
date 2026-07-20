package monitoring

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/alerts"
	"github.com/binhminh/HomeLab-Minh/internal/history"
	"github.com/binhminh/HomeLab-Minh/internal/metrics"
	"github.com/binhminh/HomeLab-Minh/internal/model"
)

const (
	hostHistoryInterval      = 10 * time.Second
	containerHistoryInterval = 30 * time.Second
)

type HistoryWriter interface {
	RecordHost(history.HostSample) bool
	RecordContainer(history.ContainerSample) bool
	RecordServiceTransition(history.ServiceTransition) bool
}

type AlertEngine interface {
	Evaluate(context.Context, []alerts.Sample) (alerts.EvaluationResult, error)
}

type Options struct {
	History           HistoryWriter
	Alerts            AlertEngine
	ActiveAlertStates []alerts.AlertState
	OnError           func(error)
}

type Pipeline struct {
	history HistoryWriter
	alerts  AlertEngine
	onError func(error)

	mu                    sync.Mutex
	lastHostHistory       map[string]time.Time
	lastContainerHist     map[string]time.Time
	serviceStates         map[string]history.ServiceState
	serviceRecordedAt     map[string]time.Time
	knownAlertResources   map[string]map[string]struct{}
	missingAlertResources map[string]int
}

func New(options Options) (*Pipeline, error) {
	if options.History == nil && options.Alerts == nil {
		return nil, errors.New("monitoring: history writer or alert engine is required")
	}
	pipeline := &Pipeline{
		history: options.History, alerts: options.Alerts, onError: options.OnError,
		lastHostHistory: make(map[string]time.Time), lastContainerHist: make(map[string]time.Time),
		serviceStates:         make(map[string]history.ServiceState),
		serviceRecordedAt:     make(map[string]time.Time),
		knownAlertResources:   make(map[string]map[string]struct{}),
		missingAlertResources: make(map[string]int),
	}
	pipeline.seedActiveAlertResources(options.ActiveAlertStates)
	return pipeline, nil
}

func (p *Pipeline) seedActiveAlertResources(states []alerts.AlertState) {
	for _, state := range states {
		if state.Status == alerts.StatusResolved || state.NodeID == "" || state.ResourceID == "" {
			continue
		}
		family := alertResourceFamily(state.ResourceType)
		if family == "" {
			continue
		}
		group := state.NodeID + "\x00" + family
		if p.knownAlertResources[group] == nil {
			p.knownAlertResources[group] = make(map[string]struct{})
		}
		p.knownAlertResources[group][state.ResourceID] = struct{}{}
	}
}

func alertResourceFamily(resourceType string) string {
	switch resourceType {
	case "host":
		return "host-temperature"
	case "disk":
		return "disks"
	case "service":
		return "services"
	case "container":
		return "containers"
	default:
		return ""
	}
}

func (p *Pipeline) RunLocal(ctx context.Context, hub *metrics.Hub) error {
	if hub == nil {
		return errors.New("monitoring: metrics hub is required")
	}
	updates, cancel := hub.Subscribe()
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case snapshot, ok := <-updates:
			if !ok {
				return nil
			}
			if err := p.Handle(ctx, history.LocalNodeID, snapshot); err != nil && p.onError != nil {
				p.onError(err)
			}
		}
	}
}

func (p *Pipeline) Handle(ctx context.Context, nodeID string, snapshot model.SnapshotEnvelope) error {
	if nodeID == "" {
		nodeID = history.LocalNodeID
	}
	at := snapshot.CollectedAt.UTC()
	if at.IsZero() {
		return errors.New("monitoring: snapshot timestamp is required")
	}
	stale := make(map[string]bool, len(snapshot.StaleSources))
	for _, source := range snapshot.StaleSources {
		stale[source] = true
	}
	for _, source := range snapshot.TruncatedSources {
		stale[source] = true
	}
	if snapshot.Truncated && len(snapshot.TruncatedSources) == 0 {
		// Conservative compatibility for older remote agents that only exposed
		// a global truncation flag. Never resolve resources from an incomplete
		// inventory.
		stale["containers"] = true
		stale["services"] = true
	}
	p.mu.Lock()
	samples := p.samplesLocked(nodeID, snapshot.Data, at, stale)
	p.recordHistoryLocked(nodeID, snapshot.Data, at, stale)
	p.mu.Unlock()
	if p.alerts == nil {
		return nil
	}
	_, err := p.alerts.Evaluate(ctx, samples)
	return err
}

// HandleNodeOffline records a transport-level outage without pretending the
// node's cached component metrics are fresh. A subsequent snapshot emits the
// matching healthy node observation and resolves the incident normally.
func (p *Pipeline) HandleNodeOffline(ctx context.Context, nodeID string, at time.Time) error {
	return p.HandleNodeAvailability(ctx, nodeID, false, at)
}

// HandleNodeAvailability evaluates the transport liveness signal independently
// from cached metric freshness.
func (p *Pipeline) HandleNodeAvailability(ctx context.Context, nodeID string, online bool, at time.Time) error {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" || at.IsZero() {
		return errors.New("monitoring: node id and timestamp are required")
	}
	if p.alerts == nil {
		return nil
	}
	value := 0.0
	if online {
		value = 1
	}
	_, err := p.alerts.Evaluate(ctx, []alerts.Sample{{
		NodeID: nodeID, ResourceType: "node", ResourceID: nodeID,
		Metric: alerts.MetricNodeOnline, Value: value, ObservedAt: at.UTC(),
	}})
	return err
}

func (p *Pipeline) recordHistoryLocked(nodeID string, data model.SnapshotData, at time.Time, stale map[string]bool) {
	if p.history == nil {
		return
	}
	if !stale["host"] {
		if last := p.lastHostHistory[nodeID]; last.IsZero() || at.Sub(last) >= hostHistoryInterval {
			disk := primaryDisk(data.Disks)
			p.history.RecordHost(history.HostSample{
				NodeID: nodeID, CollectedAt: at, CPUPercent: data.System.CPU.UsagePercent,
				MemoryUsedBytes: data.System.Memory.UsedBytes, MemoryTotalBytes: data.System.Memory.TotalBytes,
				DiskUsedBytes: disk.UsedBytes, DiskTotalBytes: disk.TotalBytes,
				NetworkRXBytesPerSecond: data.Network.RXBytesPerSecond,
				NetworkTXBytesPerSecond: data.Network.TXBytesPerSecond,
				LoadOne:                 data.System.LoadAverages[0], TemperatureCelsius: data.System.CPU.TemperatureCelsius,
			})
			p.lastHostHistory[nodeID] = at
		}
	}
	if !stale["containers"] {
		if last := p.lastContainerHist[nodeID]; last.IsZero() || at.Sub(last) >= containerHistoryInterval {
			for _, container := range data.Containers {
				if container.ID == "" || container.Name == "" {
					continue
				}
				p.history.RecordContainer(history.ContainerSample{
					NodeID: nodeID, InstanceID: container.ID, Name: container.Name, Image: container.Image,
					CollectedAt: at, CPUPercent: container.CPUUsagePercent,
					MemoryUsageBytes: container.MemoryUsageBytes, MemoryLimitBytes: container.MemoryLimitBytes,
					RestartCount: container.RestartCount,
				})
			}
			p.lastContainerHist[nodeID] = at
		}
	}
	if stale["services"] {
		return
	}
	seenServices := make(map[string]struct{}, len(data.Services))
	for _, service := range data.Services {
		if service.ID == "" {
			continue
		}
		state := serviceHistoryState(service.Status)
		key := nodeID + "\x00" + service.ID
		seenServices[key] = struct{}{}
		previous, exists := p.serviceStates[key]
		if !exists || previous != state || at.Sub(p.serviceRecordedAt[key]) >= history.ServiceObservationInterval {
			if p.history.RecordServiceTransition(history.ServiceTransition{
				NodeID: nodeID, ServiceID: service.ID, State: state, ObservedAt: at,
			}) {
				p.serviceRecordedAt[key] = at
			}
		}
		p.serviceStates[key] = state
	}
	prefix := nodeID + "\x00"
	for key, previous := range p.serviceStates {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if _, present := seenServices[key]; present {
			continue
		}
		removeState := previous == history.ServiceUnknown
		if previous != history.ServiceUnknown {
			serviceID := strings.TrimPrefix(key, prefix)
			if p.history.RecordServiceTransition(history.ServiceTransition{
				NodeID: nodeID, ServiceID: serviceID, State: history.ServiceUnknown, ObservedAt: at,
			}) {
				removeState = true
			}
		}
		if removeState {
			delete(p.serviceStates, key)
			delete(p.serviceRecordedAt, key)
		}
	}
}

func (p *Pipeline) samplesLocked(nodeID string, data model.SnapshotData, at time.Time, stale map[string]bool) []alerts.Sample {
	samples := make([]alerts.Sample, 0, 4+len(data.Disks)+len(data.Services)+len(data.Containers)*2)
	add := func(resourceType, resourceID, metric string, value float64) {
		samples = append(samples, alerts.Sample{
			NodeID: nodeID, ResourceType: resourceType, ResourceID: resourceID,
			Metric: metric, Value: value, ObservedAt: at,
		})
	}
	add("node", nodeID, alerts.MetricNodeOnline, 1)
	if !stale["host"] {
		add("host", "host", alerts.MetricCPUPercent, data.System.CPU.UsagePercent)
		memoryPercent := 0.0
		if data.System.Memory.TotalBytes > 0 {
			memoryPercent = float64(data.System.Memory.UsedBytes) / float64(data.System.Memory.TotalBytes) * 100
		}
		add("host", "host", alerts.MetricMemoryPercent, memoryPercent)
		temperatureResources := make(map[string]struct{}, 1)
		if data.System.CPU.TemperatureCelsius != nil {
			add("host", "host", alerts.MetricTemperatureCelsius, *data.System.CPU.TemperatureCelsius)
			temperatureResources["host"] = struct{}{}
		}
		p.reconcileMissingLocked(nodeID, "host-temperature", temperatureResources, func(resourceID string) {
			add("host", resourceID, alerts.MetricTemperatureCelsius, 0)
		})
		diskResources := make(map[string]struct{}, len(data.Disks))
		for _, disk := range data.Disks {
			resourceID := disk.MountPoint
			if resourceID == "" {
				resourceID = disk.Device
			}
			if resourceID != "" {
				add("disk", resourceID, alerts.MetricDiskUsedPercent, disk.UsagePercent)
				diskResources[resourceID] = struct{}{}
			}
		}
		p.reconcileMissingLocked(nodeID, "disks", diskResources, func(resourceID string) {
			add("disk", resourceID, alerts.MetricDiskUsedPercent, 0)
		})
	}
	if !stale["services"] {
		serviceResources := make(map[string]struct{}, len(data.Services))
		for _, service := range data.Services {
			if service.ID == "" {
				continue
			}
			serviceResources[service.ID] = struct{}{}
			// Unknown services have not completed a probe since startup. Do not
			// emit a synthetic clean sample that could resolve a persisted outage.
			if service.LastCheckedAt == nil {
				continue
			}
			failures := service.ConsecutiveFailures
			// Compatibility for snapshots produced by an older agent that did not
			// expose the counter: a down status already means its two-failure
			// debounce completed.
			if failures == 0 && service.Status == model.ServiceStatusDown {
				failures = 2
			}
			add("service", service.ID, alerts.MetricServiceConsecutiveFailures, float64(failures))
		}
		p.reconcileMissingLocked(nodeID, "services", serviceResources, func(resourceID string) {
			add("service", resourceID, alerts.MetricServiceConsecutiveFailures, 0)
		})
	}
	if !stale["containers"] {
		containerResources := make(map[string]struct{}, len(data.Containers))
		for _, container := range data.Containers {
			if container.ID == "" {
				continue
			}
			healthy := 1.0
			state := strings.ToLower(container.State)
			if strings.EqualFold(container.Health, "unhealthy") || state == "restarting" || state == "crashed" || state == "dead" {
				healthy = 0
			}
			add("container", container.ID, alerts.MetricContainerHealthy, healthy)
			add("container", container.ID, alerts.MetricContainerRestarts, float64(container.RestartCount))
			containerResources[container.ID] = struct{}{}
		}
		p.reconcileMissingLocked(nodeID, "containers", containerResources, func(resourceID string) {
			add("container", resourceID, alerts.MetricContainerHealthy, 1)
			add("container", resourceID, alerts.MetricContainerRestarts, 0)
		})
	}
	return samples
}

// reconcileMissingLocked emits two clean observations for resources that
// disappeared from a fresh collector result. The alert engine consequently
// resolves stale firing states without treating a collector outage as healthy.
func (p *Pipeline) reconcileMissingLocked(
	nodeID string,
	family string,
	current map[string]struct{},
	emitClean func(resourceID string),
) {
	group := nodeID + "\x00" + family
	known := p.knownAlertResources[group]
	if known == nil {
		known = make(map[string]struct{}, len(current))
		p.knownAlertResources[group] = known
	}
	for resourceID := range current {
		known[resourceID] = struct{}{}
		delete(p.missingAlertResources, group+"\x00"+resourceID)
	}
	for resourceID := range known {
		if _, exists := current[resourceID]; exists {
			continue
		}
		missingKey := group + "\x00" + resourceID
		if p.missingAlertResources[missingKey] < 2 {
			emitClean(resourceID)
			p.missingAlertResources[missingKey]++
		}
		if p.missingAlertResources[missingKey] >= 2 {
			delete(known, resourceID)
			delete(p.missingAlertResources, missingKey)
		}
	}
	if len(known) == 0 {
		delete(p.knownAlertResources, group)
	}
}

func primaryDisk(disks []model.DiskStats) model.DiskStats {
	for _, disk := range disks {
		if disk.MountPoint == "/" {
			return disk
		}
	}
	if len(disks) > 0 {
		return disks[0]
	}
	return model.DiskStats{}
}

func serviceHistoryState(status model.ServiceStatus) history.ServiceState {
	switch status {
	case model.ServiceStatusUp:
		return history.ServiceUp
	case model.ServiceStatusDown:
		return history.ServiceDown
	case model.ServiceStatusDegraded:
		return history.ServiceDegraded
	default:
		return history.ServiceUnknown
	}
}
