// Package history owns the storage-neutral data model and background workers
// used for dashboard metric history. Runtime wiring intentionally lives outside
// this package.
package history

import (
	"context"
	"errors"
	"time"
)

const (
	LocalNodeID                = "local"
	DefaultSoftQuota           = int64(2 << 30)
	DefaultQueueCapacity       = 256
	DefaultBatchSize           = 128
	DefaultFlushInterval       = 10 * time.Second
	HostSampleInterval         = 10 * time.Second
	ContainerSampleInterval    = 30 * time.Second
	HostRawRetention           = 48 * time.Hour
	HostMinuteRetention        = 30 * 24 * time.Hour
	HostQuarterRetention       = 90 * 24 * time.Hour
	ContainerRawRetention      = 48 * time.Hour
	Container5mRetention       = 30 * 24 * time.Hour
	ContainerHourRetention     = 90 * 24 * time.Hour
	ServiceRetention           = 90 * 24 * time.Hour
	ServiceObservationInterval = time.Minute
	ServiceObservationTTL      = 2 * time.Minute
	MaxResourceCatalogEntries  = 500
)

var (
	ErrInvalidRange      = errors.New("history query requires from before to")
	ErrInvalidResolution = errors.New("unsupported history resolution")
)

type Resolution string

const (
	ResolutionAuto Resolution = "auto"
	ResolutionRaw  Resolution = "raw"
	Resolution1m   Resolution = "1m"
	Resolution5m   Resolution = "5m"
	Resolution15m  Resolution = "15m"
	Resolution1h   Resolution = "1h"
)

type HostSample struct {
	NodeID                  string
	CollectedAt             time.Time
	CPUPercent              float64
	MemoryUsedBytes         uint64
	MemoryTotalBytes        uint64
	DiskUsedBytes           uint64
	DiskTotalBytes          uint64
	NetworkRXBytesPerSecond float64
	NetworkTXBytesPerSecond float64
	LoadOne                 float64
	TemperatureCelsius      *float64
}

type ContainerSample struct {
	NodeID           string
	InstanceID       string
	Name             string
	Image            string
	CollectedAt      time.Time
	CPUPercent       float64
	MemoryUsageBytes uint64
	MemoryLimitBytes uint64
	RestartCount     uint64
}

type ServiceState string

const (
	ServiceUp       ServiceState = "up"
	ServiceDown     ServiceState = "down"
	ServiceDegraded ServiceState = "degraded"
	ServiceUnknown  ServiceState = "unknown"
)

func (s ServiceState) Valid() bool {
	switch s {
	case ServiceUp, ServiceDown, ServiceDegraded, ServiceUnknown:
		return true
	default:
		return false
	}
}

type ServiceTransition struct {
	NodeID     string
	ServiceID  string
	State      ServiceState
	ObservedAt time.Time
}

type Batch struct {
	Hosts              []HostSample
	Containers         []ContainerSample
	ServiceTransitions []ServiceTransition
}

func (b Batch) Len() int {
	return len(b.Hosts) + len(b.Containers) + len(b.ServiceTransitions)
}

type Query struct {
	NodeID     string
	InstanceID string
	From       time.Time
	To         time.Time
	Resolution Resolution
}

func (q Query) normalized() (Query, error) {
	if q.NodeID == "" {
		q.NodeID = LocalNodeID
	}
	if q.Resolution == "" {
		q.Resolution = ResolutionAuto
	}
	q.From = q.From.UTC()
	q.To = q.To.UTC()
	if q.From.IsZero() || q.To.IsZero() || !q.From.Before(q.To) {
		return Query{}, ErrInvalidRange
	}
	return q, nil
}

type HostPoint struct {
	At                      time.Time `json:"at"`
	SampleCount             int64     `json:"sampleCount"`
	CPUPercent              float64   `json:"cpuPercent"`
	MemoryUsedBytes         float64   `json:"memoryUsedBytes"`
	MemoryTotalBytes        float64   `json:"memoryTotalBytes"`
	DiskUsedBytes           float64   `json:"diskUsedBytes"`
	DiskTotalBytes          float64   `json:"diskTotalBytes"`
	NetworkRXBytesPerSecond float64   `json:"networkRXBytesPerSecond"`
	NetworkTXBytesPerSecond float64   `json:"networkTXBytesPerSecond"`
	LoadOne                 float64   `json:"loadOne"`
	TemperatureCelsius      *float64  `json:"temperatureCelsius,omitempty"`
}

type ContainerPoint struct {
	At               time.Time `json:"at"`
	SampleCount      int64     `json:"sampleCount"`
	CPUPercent       float64   `json:"cpuPercent"`
	MemoryUsageBytes float64   `json:"memoryUsageBytes"`
	MemoryLimitBytes float64   `json:"memoryLimitBytes"`
	RestartCount     uint64    `json:"restartCount"`
}

type ContainerInstance struct {
	NodeID      string    `json:"nodeId"`
	InstanceID  string    `json:"instanceId"`
	Name        string    `json:"name"`
	Image       string    `json:"image"`
	FirstSeenAt time.Time `json:"firstSeenAt"`
	LastSeenAt  time.Time `json:"lastSeenAt"`
}

type ServiceSeries struct {
	NodeID      string    `json:"nodeId"`
	ServiceID   string    `json:"serviceId"`
	Name        string    `json:"name"`
	FirstSeenAt time.Time `json:"firstSeenAt"`
	LastSeenAt  time.Time `json:"lastSeenAt"`
}

type ServiceUptimePoint struct {
	At              time.Time `json:"at"`
	UpSeconds       int64     `json:"upSeconds"`
	DownSeconds     int64     `json:"downSeconds"`
	DegradedSeconds int64     `json:"degradedSeconds"`
	UnknownSeconds  int64     `json:"unknownSeconds"`
	TransitionCount int64     `json:"transitionCount"`
}

type QuotaState struct {
	UsedBytes  int64   `json:"usedBytes"`
	LimitBytes int64   `json:"limitBytes"`
	Ratio      float64 `json:"ratio"`
	Warning    bool    `json:"warning"`
	Full       bool    `json:"full"`
}

type WriterRepository interface {
	WriteHistoryBatch(context.Context, Batch) error
	HistorySizeBytes(context.Context) (int64, error)
}

type MaintenanceRepository interface {
	RollupHistory(context.Context, time.Time) error
	RetainHistory(context.Context, time.Time) error
}

type Reader interface {
	QueryHostHistory(context.Context, Query) ([]HostPoint, Resolution, error)
	QueryContainerHistory(context.Context, Query) ([]ContainerPoint, Resolution, error)
	QueryServiceUptime(context.Context, string, string, time.Time, time.Time) ([]ServiceUptimePoint, error)
	GetContainerInstance(context.Context, string, string) (ContainerInstance, error)
	ListContainerInstances(context.Context, string) ([]ContainerInstance, error)
	ListServiceSeries(context.Context, string) ([]ServiceSeries, error)
}

type Repository interface {
	WriterRepository
	MaintenanceRepository
	Reader
}

func ResolveHost(q Query) (Query, Resolution, error) {
	normalized, err := q.normalized()
	if err != nil {
		return Query{}, "", err
	}
	resolution := normalized.Resolution
	if resolution == ResolutionAuto {
		switch span := normalized.To.Sub(normalized.From); {
		case span <= 48*time.Hour:
			resolution = ResolutionRaw
		case span <= 30*24*time.Hour:
			resolution = Resolution1m
		default:
			resolution = Resolution15m
		}
	}
	if resolution != ResolutionRaw && resolution != Resolution1m && resolution != Resolution15m {
		return Query{}, "", ErrInvalidResolution
	}
	return normalized, resolution, nil
}

func ResolveContainer(q Query) (Query, Resolution, error) {
	normalized, err := q.normalized()
	if err != nil {
		return Query{}, "", err
	}
	if normalized.InstanceID == "" {
		return Query{}, "", errors.New("container history requires instance id")
	}
	resolution := normalized.Resolution
	if resolution == ResolutionAuto {
		switch span := normalized.To.Sub(normalized.From); {
		case span <= 48*time.Hour:
			resolution = ResolutionRaw
		case span <= 30*24*time.Hour:
			resolution = Resolution5m
		default:
			resolution = Resolution1h
		}
	}
	if resolution != ResolutionRaw && resolution != Resolution5m && resolution != Resolution1h {
		return Query{}, "", ErrInvalidResolution
	}
	return normalized, resolution, nil
}
