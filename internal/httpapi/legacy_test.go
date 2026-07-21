package httpapi

import (
	"strings"
	"testing"

	"github.com/bnhminh1010/homelab-dashboard/internal/model"
)

func TestMetricsMarshallersRespectFrameLimit(t *testing.T) {
	snapshot := model.SnapshotEnvelope{Version: 1, Type: "metrics.snapshot", Data: model.SnapshotData{
		System:     model.SystemStats{Hostname: "host"},
		Containers: make([]model.Container, 80),
	}}
	for index := range snapshot.Data.Containers {
		snapshot.Data.Containers[index] = model.Container{
			ID: strings.Repeat("a", 64), Name: strings.Repeat("container", 20),
			Image: strings.Repeat("image", 30), Ports: []string{strings.Repeat("port", 30)},
		}
	}
	for name, marshal := range map[string]func(model.SnapshotEnvelope, int) ([]byte, error){
		"versioned": marshalSnapshot,
		"legacy":    marshalLegacySnapshot,
	} {
		t.Run(name, func(t *testing.T) {
			payload, err := marshal(snapshot, 4<<10)
			if err != nil {
				t.Fatal(err)
			}
			if len(payload) > 4<<10 {
				t.Fatalf("frame size=%d", len(payload))
			}
		})
	}
}

func TestLegacySnapshotUsesOriginalFieldNames(t *testing.T) {
	payload := legacySnapshot(model.SnapshotData{
		System: model.SystemStats{
			Hostname: "debian", UptimeSeconds: 42, ProcessCount: 7,
			CPU:    model.CPUStats{UsagePercent: 12.5, Cores: 4},
			Memory: model.MemoryStats{TotalBytes: 8 * 1024},
		},
		Services:   []model.Service{{ID: "svc", DisplayURL: "http://host:8080", Status: model.ServiceStatusUp}},
		Containers: []model.Container{{ID: "container", State: "running", RestartCount: 2}},
	}, false)
	system := payload["system"].(map[string]any)
	cpu := system["cpu"].(map[string]any)
	services := payload["services"].([]map[string]any)
	containers := payload["containers"].([]map[string]any)
	if system["uptime"] != uint64(42) || cpu["percent"] != 12.5 || services[0]["url"] != "http://host:8080" || services[0]["port"] != "8080" || containers[0]["restarts"] != uint64(2) {
		t.Fatalf("legacy contract mismatch: %+v", payload)
	}
}
