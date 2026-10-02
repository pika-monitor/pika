package metric

import (
	"testing"

	"github.com/pika-monitor/pika/internal/protocol"
)

func TestSampleTimestampsRemainIndependent(t *testing.T) {
	latest := &LatestMetrics{}
	latest.UpdateSample(protocol.MetricTypeCPU, 100, 200, func(current *LatestMetrics) {
		current.CPU = &protocol.CPUData{UsagePercent: 42}
	})
	snapshot := latest.Snapshot()
	latest.UpdateSample(protocol.MetricTypeMemory, 110, 210, func(current *LatestMetrics) {
		current.Memory = &protocol.MemoryData{UsagePercent: 50}
	})
	updated := latest.Snapshot()
	if updated.SampleTimestamps[protocol.MetricTypeCPU] != 100 || updated.SampleTimestamps[protocol.MetricTypeMemory] != 110 || updated.Timestamp != 210 {
		t.Fatalf("unexpected timestamps: %+v", updated)
	}
	if _, exists := snapshot.SampleTimestamps[protocol.MetricTypeMemory]; exists {
		t.Fatal("update mutated an existing snapshot")
	}
	snapshot.SampleTimestamps[protocol.MetricTypeCPU] = 999
	if latest.Snapshot().SampleTimestamps[protocol.MetricTypeCPU] != 100 {
		t.Fatal("snapshot mutation changed the cache")
	}
}
