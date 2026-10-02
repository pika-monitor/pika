package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/go-orz/cache"
	"github.com/pika-monitor/pika/internal/metric"
	"github.com/pika-monitor/pika/internal/protocol"
)

func TestPrepareMetricDataPreservesPerTypeSampleTime(t *testing.T) {
	service := &MetricService{latestCache: cache.New[string, *metric.LatestMetrics](time.Minute)}
	for _, sample := range []struct {
		kind      string
		data      string
		timestamp int64
	}{
		{"cpu", `{"usagePercent":42}`, 100},
		{"memory", `{"usagePercent":50}`, 110},
	} {
		metrics, err := service.PrepareMetricData(context.Background(), "agent", sample.kind, json.RawMessage(sample.data), sample.timestamp)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range metrics {
			if entry.Timestamps[0] != sample.timestamp {
				t.Fatal("VM timestamp differs from sample time")
			}
		}
	}
	for _, sample := range []struct{ kind, data string }{
		{"cpu", `invalid`},
		{"gpu", `[]`},
		{"disk", `[]`},
	} {
		service.PrepareMetricData(context.Background(), "agent", sample.kind, json.RawMessage(sample.data), 120)
	}
	latest, _ := service.latestCache.Get("agent")
	snapshot := latest.Snapshot()
	if snapshot.SampleTimestamps[protocol.MetricTypeCPU] != 100 || snapshot.SampleTimestamps[protocol.MetricTypeMemory] != 110 || len(snapshot.SampleTimestamps) != 2 {
		t.Fatalf("timestamps changed without a valid update: %+v", snapshot.SampleTimestamps)
	}
}
