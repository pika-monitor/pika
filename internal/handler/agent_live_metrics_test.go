package handler

import (
	"github.com/pika-monitor/pika/internal/metric"
	"testing"
)

func TestPublicLiveResponseKeepsOnlyNetworkTotals(t *testing.T) {
	response := &metric.LiveMetricsResponse{Series: map[string][]metric.Series{"network": {
		{Name: "upload", Labels: map[string]string{"interface": "private0"}},
		{Name: "download", Labels: map[string]string{"interface": "private1"}},
		{Name: "upload", Labels: map[string]string{"interface": ""}, Data: []metric.DataPoint{{Timestamp: 100, Value: 30}}},
		{Name: "download", Labels: map[string]string{"interface": ""}},
	}}}
	sanitizePublicLiveMetrics(response)
	if len(response.Series["network"]) != 2 || response.Series["network"][0].Data[0].Value != 30 {
		t.Fatalf("public network response: %+v", response.Series["network"])
	}
	for _, series := range response.Series["network"] {
		if series.Labels["interface"] != "" {
			t.Fatal("private interface leaked")
		}
	}
}
