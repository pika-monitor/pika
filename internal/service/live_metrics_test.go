package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-orz/cache"
	"github.com/pika-monitor/pika/internal/metric"
	"github.com/pika-monitor/pika/internal/vmclient"
	"go.uber.org/zap"
)

func TestLiveHistoryBatchesCachesAndMergesEveryReceivedSample(t *testing.T) {
	now := time.Now().UnixMilli()
	var exports atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exports.Add(1)
		if r.URL.Path != "/api/v1/export" || len(r.URL.Query()["match[]"]) != 1 {
			t.Errorf("not a batch export: %s", r.URL)
		}
		time.Sleep(20 * time.Millisecond)
		fmt.Fprintf(w, `{"metric":{"__name__":"pika_cpu_usage_percent","agent_id":"a"},"values":[1,2],"timestamps":[%d,%d]}`, now-120000, now-4000)
	}))
	defer server.Close()
	service := &MetricService{vmClient: vmclient.NewVMClient(server.URL, time.Second, time.Second), latestCache: cache.New[string, *metric.LatestMetrics](time.Minute)}
	for _, point := range []struct {
		ts    int64
		value int
	}{{now - 4000, 3}, {now - 2000, 99}, {now - 1000, 4}} {
		_, err := service.PrepareMetricData(context.Background(), "a", "cpu", json.RawMessage(fmt.Sprintf(`{"usagePercent":%d}`, point.value)), point.ts)
		if err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			response, err := service.GetLiveMetrics(context.Background(), "a")
			if err != nil {
				t.Error(err)
				return
			}
			points := response.Series["cpu"][0].Data
			if len(points) != 4 || points[1].Value != 3 || points[2].Value != 99 || points[2].Timestamp != now-2000 {
				t.Errorf("missing middle samples/peak: %+v", points)
			}
			if response.End-response.Start != 300000 || response.End-response.MonitorStart != 900000 {
				t.Errorf("wrong window: %+v", response)
			}
		})
	}
	wg.Wait()
	if exports.Load() != 1 {
		t.Fatalf("20 readers triggered %d exports", exports.Load())
	}
	service.liveMu.Lock()
	service.liveAgents["a"].expiresAt = time.Time{}
	service.liveMu.Unlock()
	service.GetLiveMetrics(context.Background(), "a")
	if exports.Load() != 2 {
		t.Fatal("expired history did not reload")
	}
}

func TestLiveSeriesIdentityNetworkTotalsAndPrivacy(t *testing.T) {
	now := time.Now().UnixMilli()
	rows := []vmclient.Metric{
		createMetric("pika_network_sent_bytes_rate", "a", map[string]string{"interface": "eth0"}, 10, now-2000),
		createMetric("pika_network_sent_bytes_rate", "a", map[string]string{"interface": "eth1"}, 20, now-2000),
		createMetric("pika_gpu_utilization_percent", "a", map[string]string{"gpu_index": "1"}, 99, now-2000),
		createMetric("pika_gpu_temperature_celsius", "a", map[string]string{"gpu_index": "1"}, 50, now-2000),
		createMetric("pika_monitor_response_time_ms", "a", map[string]string{"monitor_id": "m", "target": "private"}, 42, now-600000),
		createMetric("pika_cpu_usage_percent", "b", nil, 100, now-2000),
	}
	response := buildLiveResponse("a", rows, now, map[string]map[string]string{"m": {"monitor_name": "M", "interval_ms": "60000"}})
	if len(response.Series["gpu"]) != 2 || response.Series["gpu"][0].Labels["metric_type"] == response.Series["gpu"][1].Labels["metric_type"] {
		t.Fatal("GPU metric identity lost")
	}
	monitor := response.Series["monitor"][0]
	if monitor.Labels["target"] != "" || monitor.Labels["monitor_name"] != "M" || monitor.Data[0].Timestamp != now-600000 {
		t.Fatal("monitor privacy/window changed")
	}
	if len(response.Series["cpu"]) != 0 {
		t.Fatal("cross-agent sample leaked")
	}
	network := response.Series["network"]
	if len(network) != 3 || network[2].Labels["interface"] != "" || network[2].Data[0].Value != 30 {
		t.Fatalf("wrong total: %+v", network)
	}
}

func TestLiveHistoryFailureIsExplicitAndRecentDataSurvives(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	service := &MetricService{vmClient: vmclient.NewVMClient(server.URL, time.Second, time.Second)}
	if _, err := service.GetLiveMetrics(context.Background(), "empty"); err == nil {
		t.Fatal("total failure returned successful empty history")
	}
	service.recordLiveMetrics("a", []vmclient.Metric{createMetric("pika_cpu_usage_percent", "a", nil, 99, time.Now().Add(-time.Second).UnixMilli())})
	response, err := service.GetLiveMetrics(context.Background(), "a")
	if err != nil || response.HistoryError == "" || response.Series["cpu"][0].Data[0].Value != 99 {
		t.Fatalf("recent samples/error lost: %+v %v", response, err)
	}
}

func TestHistoricalQueriesRunInParallelAndReportFailures(t *testing.T) {
	var running, maxRunning atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := running.Add(1)
		defer running.Add(-1)
		for {
			old := maxRunning.Load()
			if n <= old || maxRunning.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		if r.URL.Query().Get("query") == "" {
			t.Error("missing query")
		}
		if r.URL.Query().Get("step") == "1s" {
			w.WriteHeader(503)
			return
		}
		if contains := r.URL.Query().Get("query"); len(contains) > 0 {
			// One connection-state query fails; the others remain usable.
			if strings.Contains(contains, "conn_listen") {
				w.WriteHeader(503)
				return
			}
		}
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[100,"42"]]}]}}`)
	}))
	defer server.Close()
	service := &MetricService{vmClient: vmclient.NewVMClient(server.URL, time.Second, time.Second), logger: zap.NewNop()}
	response, err := service.GetMetrics(context.Background(), "a", "network_connection", 0, 3600000, "all", "")
	if err != nil || len(response.FailedSeries) != 1 || response.FailedSeries[0] != "listen" || len(response.Series) != 3 {
		t.Fatalf("partial failure hidden: %+v %v", response, err)
	}
	if maxRunning.Load() < 2 || maxRunning.Load() > 4 {
		t.Fatalf("invalid query concurrency %d", maxRunning.Load())
	}
	if _, err := service.GetMetrics(context.Background(), "a", "cpu", 0, 120000, "all", ""); err == nil {
		t.Fatal("total failure swallowed")
	}
}

func TestConcurrentFirstReportsDoNotLoseLatestMetricTypes(t *testing.T) {
	service := &MetricService{latestCache: cache.New[string, *metric.LatestMetrics](time.Minute)}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, kind := range []string{"cpu", "memory"} {
		wg.Go(func() {
			<-start
			_, err := service.PrepareMetricData(context.Background(), "a", kind, json.RawMessage(`{"usagePercent":42}`), time.Now().UnixMilli())
			if err != nil {
				t.Error(err)
			}
		})
	}
	close(start)
	wg.Wait()
	latest, _ := service.GetLatestMetrics("a")
	if latest.CPU == nil || latest.Memory == nil || len(latest.SampleTimestamps) != 2 {
		t.Fatalf("first reports overwrote each other: %+v", latest)
	}
}

func TestEmptyDiskIOReportDoesNotManufactureZerosOrPanic(t *testing.T) {
	service := &MetricService{latestCache: cache.New[string, *metric.LatestMetrics](time.Minute)}
	for _, payload := range []string{`[]`, `[null]`} {
		rows, err := service.PrepareMetricData(context.Background(), "a", "disk_io", json.RawMessage(payload), time.Now().UnixMilli())
		if err != nil || len(rows) != 0 {
			t.Fatalf("empty report became zero samples: %+v %v", rows, err)
		}
	}
}

func TestClearLiveHistoryDoesNotRestoreAnObsoleteInflightExport(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		fmt.Fprint(w, `{"metric":{"__name__":"pika_cpu_usage_percent","agent_id":"a"},"values":[1],"timestamps":[1]}`)
	}))
	defer server.Close()
	service := &MetricService{vmClient: vmclient.NewVMClient(server.URL, time.Second, time.Second)}
	done := make(chan error, 1)
	go func() { done <- service.loadLiveHistory(context.Background(), "a") }()
	<-started
	service.clearLiveMetrics("a")
	close(release)
	if err := <-done; err == nil {
		t.Fatal("obsolete export repopulated deleted cache")
	}
	service.liveMu.Lock()
	defer service.liveMu.Unlock()
	if len(service.liveAgents) != 0 || service.liveCachedSamples != 0 {
		t.Fatal("cleared history was restored")
	}
}
