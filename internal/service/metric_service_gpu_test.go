package service

import (
	"testing"

	"github.com/pika-monitor/pika/internal/vmclient"
)

func TestConvertGPUQueryResultPreservesMetricIdentity(t *testing.T) {
	result := &vmclient.QueryResult{Data: vmclient.ResultData{Result: []vmclient.Result{
		{Metric: map[string]string{"gpu_index": "0", "gpu_name": "GPU A", "target": "private", "__name__": "raw_metric"}, Values: [][]interface{}{{float64(100), "25"}}},
		{Metric: map[string]string{"gpu_index": "1", "gpu_name": "GPU B"}, Values: [][]interface{}{{float64(100), "75"}}},
	}}}
	service := &MetricService{}
	for _, metricType := range []string{"utilization", "temperature"} {
		t.Run(metricType, func(t *testing.T) {
			series := service.convertQueryResultToSeries(result, metricType, map[string]string{"agg": "max"})
			if len(series) != 2 {
				t.Fatalf("expected two GPUs, got %d", len(series))
			}
			for index, entry := range series {
				gpuIndex := []string{"0", "1"}[index]
				if entry.Name != "GPU_"+gpuIndex || entry.Labels["gpu_index"] != gpuIndex || entry.Labels["metric_type"] != metricType {
					t.Fatalf("GPU identity or metric type lost: %+v", entry)
				}
				if entry.Labels["agg"] != "max" || entry.Labels["gpu_name"] == "" {
					t.Fatalf("expected existing labels to remain: %+v", entry.Labels)
				}
				if _, exists := entry.Labels["target"]; exists {
					t.Fatal("private target leaked")
				}
				if _, exists := entry.Labels["__name__"]; exists {
					t.Fatal("internal metric name leaked")
				}
				if len(entry.Data) != 1 || entry.Data[0].Timestamp != 100000 {
					t.Fatalf("unexpected data points: %+v", entry.Data)
				}
			}
		})
	}
}

func TestConvertTemperatureQueryResultKeepsSensorName(t *testing.T) {
	result := &vmclient.QueryResult{Data: vmclient.ResultData{Result: []vmclient.Result{
		{Metric: map[string]string{"sensor_label": "CPU"}, Values: [][]interface{}{{float64(100), "50"}}},
	}}}
	series := (&MetricService{}).convertQueryResultToSeries(result, "temperature", nil)
	if len(series) != 1 || series[0].Name != "CPU" || series[0].Data[0].Value != 50 {
		t.Fatalf("sensor series changed unexpectedly: %+v", series)
	}
	if _, exists := series[0].Labels["sensor_label"]; exists {
		t.Fatal("sensor label should remain merged into the name")
	}
}
