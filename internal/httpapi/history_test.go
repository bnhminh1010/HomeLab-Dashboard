package httpapi

import (
	"testing"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/history"
)

func TestHistoryDownsamplingBoundsResponseAndPreservesTotals(t *testing.T) {
	points := make([]history.ServiceUptimePoint, 120)
	for index := range points {
		points[index] = history.ServiceUptimePoint{
			At: time.Unix(int64(index*3600), 0).UTC(), UpSeconds: 3000,
			DownSeconds: 600, TransitionCount: 1,
		}
	}
	reduced := downsampleServicePoints(points, 60)
	if len(reduced) != 60 {
		t.Fatalf("downsampled points = %d, want 60", len(reduced))
	}
	var up, down, transitions int64
	for _, point := range reduced {
		up += point.UpSeconds
		down += point.DownSeconds
		transitions += point.TransitionCount
	}
	if up != 120*3000 || down != 120*600 || transitions != 120 {
		t.Fatalf("downsampling changed service totals: up=%d down=%d transitions=%d", up, down, transitions)
	}

	hosts := make([]history.HostPoint, 120)
	for index := range hosts {
		hosts[index] = history.HostPoint{At: time.Unix(int64(index), 0), SampleCount: 1, CPUPercent: float64(index)}
	}
	if got := downsampleHostPoints(hosts, 60); len(got) != 60 || got[0].CPUPercent != 0.5 || got[59].CPUPercent != 118.5 {
		t.Fatalf("unexpected host downsample: first=%+v last=%+v len=%d", got[0], got[len(got)-1], len(got))
	}
}

func TestHistoryResolutionUsesPointBudgetBeforeQuery(t *testing.T) {
	now := time.Now().UTC()
	if got := boundedHostResolution(history.Query{From: now.Add(-time.Hour), To: now, Resolution: history.ResolutionAuto}, 60); got != history.Resolution1m {
		t.Fatalf("one-hour host resolution = %s", got)
	}
	if got := boundedHostResolution(history.Query{From: now.Add(-24 * time.Hour), To: now, Resolution: history.ResolutionAuto}, 60); got != history.Resolution15m {
		t.Fatalf("one-day host resolution = %s", got)
	}
	if got := boundedContainerResolution(history.Query{From: now.Add(-24 * time.Hour), To: now, Resolution: history.ResolutionAuto}, 60); got != history.Resolution1h {
		t.Fatalf("one-day container resolution = %s", got)
	}
	if got := boundedHostResolution(history.Query{From: now.Add(-90 * 24 * time.Hour), To: now, Resolution: history.ResolutionRaw}, 1); got != history.Resolution15m {
		t.Fatalf("explicit raw host query bypassed point budget: %s", got)
	}
	if got := boundedContainerResolution(history.Query{From: now.Add(-90 * 24 * time.Hour), To: now, Resolution: history.ResolutionRaw}, 1); got != history.Resolution1h {
		t.Fatalf("explicit raw container query bypassed point budget: %s", got)
	}
}
