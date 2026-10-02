package vmclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fullAgentMetrics 模拟两块磁盘、两个网卡的完整常规上报（34 条时间序列）。
func fullAgentMetrics(agent int, timestamp int64, value float64, run, scenario string) []Metric {
	var metrics []Metric
	add := func(name string, extra map[string]string) {
		labels := map[string]string{"__name__": "pika_batch_loadtest_" + name, "agent_id": fmt.Sprintf("load-agent-%04d", agent), "test_run": run, "test_case": scenario}
		for k, v := range extra {
			labels[k] = v
		}
		metrics = append(metrics, Metric{Metric: labels, Values: []float64{value}, Timestamps: []int64{timestamp}})
	}
	for _, name := range []string{"cpu_usage_percent", "cpu_cores_logical", "cpu_cores_physical", "memory_usage_percent", "memory_total_bytes", "memory_used_bytes", "memory_available_bytes", "memory_swap_total_bytes", "memory_swap_used_bytes"} {
		add(name, nil)
	}
	for _, mount := range []string{"/", "/data"} {
		for _, name := range []string{"disk_usage_percent", "disk_total_bytes", "disk_used_bytes", "disk_free_bytes"} {
			add(name, map[string]string{"mount_point": mount})
		}
	}
	for _, device := range []string{"eth0", "eth1"} {
		for _, name := range []string{"network_sent_bytes_rate", "network_recv_bytes_rate", "network_sent_bytes_total", "network_recv_bytes_total"} {
			add(name, map[string]string{"interface": device})
		}
	}
	for _, name := range []string{"network_conn_established", "network_conn_syn_sent", "network_conn_syn_recv", "network_conn_time_wait", "network_conn_close_wait", "network_conn_listen", "network_conn_total", "disk_read_bytes_rate", "disk_write_bytes_rate"} {
		add(name, nil)
	}
	return metrics
}

type importStatsTransport struct {
	base                                         http.RoundTripper
	imports, bytes, active, peak, newConnections atomic.Int64
}

func (s *importStatsTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path != "/api/v1/import" {
		return s.base.RoundTrip(r)
	}
	s.imports.Add(1)
	s.bytes.Add(r.ContentLength)
	n := s.active.Add(1)
	defer s.active.Add(-1)
	for old := s.peak.Load(); n > old; old = s.peak.Load() {
		if s.peak.CompareAndSwap(old, n) {
			break
		}
	}
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		if !info.Reused {
			s.newConnections.Add(1)
		}
	}}
	return s.base.RoundTrip(r.WithContext(httptrace.WithClientTrace(r.Context(), trace)))
}

func runAgentLoad(t *testing.T, client *VMClient, run, scenario string, rounds int, uniform bool) (int64, *importStatsTransport) {
	t.Helper()
	const agents = 1000
	stats := &importStatsTransport{base: client.httpClient.Transport}
	client.httpClient.Transport = stats
	writer := NewBatchWriter(client)
	started := time.Now().Add(50 * time.Millisecond)
	timestamp := started.UnixMilli()
	var mu sync.Mutex
	var latencies []time.Duration
	var failures int
	var firstError error
	var peakQueued atomic.Int64
	stop := make(chan struct{})
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				n := int64(len(writer.slots))
				for old := peakQueued.Load(); n > old; old = peakQueued.Load() {
					if peakQueued.CompareAndSwap(old, n) {
						break
					}
				}
			case <-stop:
				return
			}
		}
	}()
	var wg sync.WaitGroup
	for agent := range agents {
		wg.Go(func() {
			phase := time.Duration(0)
			if uniform {
				phase = time.Duration(agent) * time.Millisecond
			}
			for round := range rounds {
				if wait := time.Until(started.Add(phase + time.Duration(round)*time.Second)); wait > 0 {
					time.Sleep(wait)
				}
				metrics := fullAgentMetrics(agent, timestamp+int64(round)*1000, float64(round+1), run, scenario)
				before := time.Now()
				err := writer.Write(context.Background(), metrics)
				duration := time.Since(before)
				mu.Lock()
				latencies = append(latencies, duration)
				if err != nil {
					failures++
					if firstError == nil {
						firstError = err
					}
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	close(stop)
	<-monitorDone
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p := func(percent int) time.Duration { return latencies[(len(latencies)-1)*percent/100] }
	t.Logf("%s: agents=%d rounds=%d samples=%d imports=%d bytes=%d peak_imports=%d new_connections=%d peak_inflight_reports=%d elapsed=%s latency_p50=%s p95=%s p99=%s max=%s failures=%d", scenario, agents, rounds, agents*rounds*34, stats.imports.Load(), stats.bytes.Load(), stats.peak.Load(), stats.newConnections.Load(), peakQueued.Load(), time.Since(started), p(50), p(95), p(99), latencies[len(latencies)-1], failures)
	if failures != 0 {
		t.Fatalf("%d failed writes, first: %v", failures, firstError)
	}
	if stats.peak.Load() > batchWorkers {
		t.Fatalf("import concurrency exceeded limit: %d", stats.peak.Load())
	}
	if stats.newConnections.Load() > batchWorkers {
		t.Errorf("connection churn: %d new connections", stats.newConnections.Load())
	}
	if time.Since(started) > time.Duration(rounds)*time.Second+5*time.Second {
		t.Error("load did not drain within 5 seconds of scheduled end")
	}
	deadline := time.Now().Add(time.Second)
	for {
		writer.mu.Lock()
		workers := writer.workers
		writer.mu.Unlock()
		if workers == 0 && len(writer.slots) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("writer did not drain and exit")
		}
		time.Sleep(time.Millisecond)
	}
	return timestamp, stats
}

// TestBatchWriterVictoriaMetrics 是显式启用的本地集成测试。
// PIKA_VM_TEST_URL=http://127.0.0.1:8428 go test ./internal/vmclient -run TestBatchWriterVictoriaMetrics -v -count=1
// 只写入 pika_batch_loadtest_* 指标，使用独立 test_run 标签，测试结束清理该标签的数据。
func TestBatchWriterVictoriaMetrics(t *testing.T) {
	endpoint := os.Getenv("PIKA_VM_TEST_URL")
	if endpoint == "" {
		t.Skip("set PIKA_VM_TEST_URL to run against local VictoriaMetrics")
	}
	rounds := 10
	if raw := os.Getenv("PIKA_VM_TEST_ROUNDS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 120 {
			t.Fatal("PIKA_VM_TEST_ROUNDS must be 1..120")
		}
		rounds = n
	}
	run := fmt.Sprintf("pika-test-%d", time.Now().UnixNano())
	cleanupClient := NewVMClient(endpoint, 5*time.Second, 10*time.Second)
	defer cleanupClient.httpClient.CloseIdleConnections()
	matcher := fmt.Sprintf(`{__name__=~"pika_batch_loadtest_.*",test_run=%q}`, run)
	defer func() {
		if err := cleanupClient.DeleteSeries(context.Background(), []string{matcher}); err != nil {
			t.Errorf("cleanup %s failed: %v", run, err)
		} else {
			t.Logf("cleaned test data: %s", run)
		}
	}()
	for _, scenario := range []string{"burst", "uniform"} {
		t.Run(scenario, func(t *testing.T) {
			client := NewVMClient(endpoint, 5*time.Second, 10*time.Second)
			transport := client.httpClient.Transport.(*http.Transport)
			defer transport.CloseIdleConnections()
			timestamp, _ := runAgentLoad(t, client, run, scenario, rounds, scenario == "uniform")
			selector := fmt.Sprintf(`{__name__=~"pika_batch_loadtest_.*",test_run=%q,test_case=%q}`, run, scenario)
			verifyVMExport(t, endpoint, selector, timestamp, rounds)
		})
	}
}

func verifyVMExport(t *testing.T, endpoint, selector string, timestamp int64, rounds int) {
	t.Helper()
	exportClient := &http.Client{Timeout: 15 * time.Second}
	defer exportClient.CloseIdleConnections()
	deadline := time.Now().Add(20 * time.Second)
	for {
		params := url.Values{"match[]": {selector}, "start": {fmt.Sprint(timestamp/1000 - 1)}, "end": {fmt.Sprint((timestamp+int64(rounds)*1000)/1000 + 1)}}
		resp, err := exportClient.Get(strings.TrimRight(endpoint, "/") + "/api/v1/export?" + params.Encode())
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("export status %d: %s", resp.StatusCode, body)
		}
		seen := make(map[string]map[int64]bool)
		samples := 0
		decoder := json.NewDecoder(resp.Body)
		for {
			var metric Metric
			if err := decoder.Decode(&metric); err != nil {
				if err != io.EOF {
					resp.Body.Close()
					t.Fatal(err)
				}
				break
			}
			key, _ := json.Marshal(metric.Metric)
			if seen[string(key)] == nil {
				seen[string(key)] = make(map[int64]bool)
			}
			if len(metric.Values) != len(metric.Timestamps) {
				t.Error("export value/timestamp length mismatch")
				continue
			}
			for i, ts := range metric.Timestamps {
				if seen[string(key)][ts] {
					t.Errorf("duplicate sample in export: %s at %d", key, ts)
				}
				seen[string(key)][ts] = true
				round := (ts - timestamp) / 1000
				if ts < timestamp || (ts-timestamp)%1000 != 0 || round >= int64(rounds) || metric.Values[i] != float64(round+1) {
					t.Errorf("unexpected persisted sample at %d: value=%v", ts, metric.Values[i])
				}
				samples++
			}
		}
		resp.Body.Close()
		if samples == 1000*34*rounds {
			if len(seen) != 1000*34 {
				t.Fatalf("exported %d series, want 34000", len(seen))
			}
			for key, points := range seen {
				if len(points) != rounds {
					t.Fatalf("series %s has %d samples, want %d", key, len(points), rounds)
				}
			}
			t.Logf("VM export verified: %d series, %d samples, all timestamps and values correct", len(seen), samples)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("exported %d samples, want %d", samples, 1000*34*rounds)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func TestBatchWriterFullMetricsWithSlowVM(t *testing.T) {
	for _, delay := range []time.Duration{50 * time.Millisecond, 200 * time.Millisecond} {
		t.Run(delay.String(), func(t *testing.T) {
			var samples atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				decoder := json.NewDecoder(r.Body)
				for {
					var metric Metric
					if err := decoder.Decode(&metric); err != nil {
						if err != io.EOF {
							t.Errorf("decode import: %v", err)
						}
						break
					}
					samples.Add(int64(len(metric.Values)))
				}
				time.Sleep(delay)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			client := NewVMClient(server.URL, 5*time.Second, time.Second)
			transport := client.httpClient.Transport.(*http.Transport)
			defer transport.CloseIdleConnections()
			runAgentLoad(t, client, "mock", delay.String(), 3, true)
			if samples.Load() != 1000*3*34 {
				t.Fatalf("received %d samples, want 102000", samples.Load())
			}
		})
	}
}

func TestBatchWriterFullQueueRecovers(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	defer releaseOnce.Do(func() { close(release) })
	client := NewVMClient(server.URL, 10*time.Second, time.Second)
	defer client.httpClient.CloseIdleConnections()
	writer := NewBatchWriter(client)
	var wg sync.WaitGroup
	for agent := range batchQueueSize {
		wg.Go(func() {
			if err := writer.Write(context.Background(), fullAgentMetrics(agent, time.Now().UnixMilli(), 1, "queue", "blocked")); err != nil {
				t.Errorf("queued write failed: %v", err)
			}
		})
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(writer.slots) != batchQueueSize {
		if time.Now().After(deadline) {
			t.Fatalf("only %d slots filled", len(writer.slots))
		}
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := writer.Write(ctx, fullAgentMetrics(9999, time.Now().UnixMilli(), 1, "queue", "overflow")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("overflow request must wait and time out: %v", err)
	}
	if len(writer.slots) != batchQueueSize {
		t.Fatalf("overflow changed occupied slots: %d", len(writer.slots))
	}
	releaseOnce.Do(func() { close(release) })
	wg.Wait()
	if err := writer.Write(context.Background(), fullAgentMetrics(10000, time.Now().UnixMilli(), 1, "queue", "recovered")); err != nil {
		t.Fatalf("write after recovery: %v", err)
	}
	t.Logf("all %d occupied slots drained; canceled overflow exited; new write succeeded", batchQueueSize)
}

func TestBatchWriterFullMetricsAfterVMFailure(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	var accepted atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		decoder := json.NewDecoder(r.Body)
		for {
			var metric Metric
			if err := decoder.Decode(&metric); err != nil {
				if err != io.EOF {
					t.Errorf("decode: %v", err)
				}
				break
			}
			accepted.Add(int64(len(metric.Values)))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := NewVMClient(server.URL, 5*time.Second, time.Second)
	defer client.httpClient.CloseIdleConnections()
	writer := NewBatchWriter(client)
	for _, unavailable := range []bool{true, false} {
		fail.Store(unavailable)
		var failures atomic.Int64
		var wg sync.WaitGroup
		for agent := range 1000 {
			wg.Go(func() {
				err := writer.Write(context.Background(), fullAgentMetrics(agent, time.Now().UnixMilli(), 1, "failure", "recovery"))
				if err != nil {
					failures.Add(1)
					if !strings.Contains(err.Error(), "503") {
						t.Errorf("unexpected failure: %v", err)
					}
				}
			})
		}
		wg.Wait()
		want := int64(0)
		if unavailable {
			want = 1000
		}
		if failures.Load() != want {
			t.Fatalf("unavailable=%v: failures=%d, want %d", unavailable, failures.Load(), want)
		}
	}
	if accepted.Load() != 34000 {
		t.Fatalf("recovery accepted %d samples, want 34000", accepted.Load())
	}
	t.Log("503 returned to all 1000 affected reports; all 34000 subsequent samples imported after recovery")
}
