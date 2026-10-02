package vmclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBatchWriterThousandAgents(t *testing.T) {
	const agents = 1000
	var imports, concurrent, peak atomic.Int64
	var received sync.Map
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		imports.Add(1)
		n := concurrent.Add(1)
		defer concurrent.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		decoder := json.NewDecoder(r.Body)
		for {
			var metric Metric
			if err := decoder.Decode(&metric); err != nil {
				if err != io.EOF {
					t.Errorf("decode import: %v", err)
				}
				break
			}
			if len(metric.Values) != 1 || metric.Values[0] != 42 || len(metric.Timestamps) != 1 || metric.Timestamps[0] != 123456789 {
				t.Errorf("payload changed: %+v", metric)
			}
			if _, loaded := received.LoadOrStore(metric.Metric["agent_id"], true); loaded {
				t.Errorf("duplicate metric: %s", metric.Metric["agent_id"])
			}
		}
		time.Sleep(5 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := NewVMClient(server.URL, 5*time.Second, time.Second)
	t.Cleanup(client.httpClient.CloseIdleConnections)
	writer := NewBatchWriter(client)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range agents {
		wg.Go(func() {
			<-start
			if err := writer.Write(context.Background(), testMetric(i)); err != nil {
				t.Errorf("agent %d: %v", i, err)
			}
		})
	}
	close(start)
	wg.Wait()
	count := 0
	received.Range(func(_, _ any) bool { count++; return true })
	if count != agents {
		t.Fatalf("received %d agents, want %d", count, agents)
	}
	if got := imports.Load(); got >= agents/10 {
		t.Errorf("sent %d imports for 1000 agents, want fewer than 100", got)
	}
	if got := peak.Load(); got > batchWorkers {
		t.Errorf("peak imports = %d, want <= %d", got, batchWorkers)
	}
	t.Logf("1000 agents: %d imports, peak concurrency %d", imports.Load(), peak.Load())
	deadline := time.Now().Add(time.Second)
	for {
		writer.mu.Lock()
		workers := writer.workers
		writer.mu.Unlock()
		if workers == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("workers did not exit after queue drained")
		}
		time.Sleep(time.Millisecond)
	}
	if len(writer.slots) != 0 {
		t.Fatal("queue slots were not released")
	}
	// 空闲退出后，后续上报仍能启动 worker。
	if err := writer.Write(context.Background(), testMetric(agents)); err != nil {
		t.Fatal(err)
	}
}

func TestBatchWriterWaitsForImportAndPropagatesFailure(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		<-release
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "VM unavailable")
	}))
	defer server.Close()
	defer once.Do(func() { close(release) })
	client := NewVMClient(server.URL, time.Second, time.Second)
	t.Cleanup(client.httpClient.CloseIdleConnections)
	writer := NewBatchWriter(client)
	done := make(chan error, 1)
	go func() { done <- writer.Write(context.Background(), testMetric(1)) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("import never started")
	}
	select {
	case err := <-done:
		t.Fatalf("write returned before VM response: %v", err)
	default:
	}
	once.Do(func() { close(release) })
	if err := <-done; err == nil || !strings.Contains(err.Error(), "503: VM unavailable") {
		t.Fatalf("VM failure was lost: %v", err)
	}
}

func TestBatchWriterCancellationDoesNotCancelSharedImport(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var received atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decoder := json.NewDecoder(r.Body)
		for {
			var metric Metric
			if err := decoder.Decode(&metric); err != nil {
				break
			}
			received.Add(1)
		}
		close(entered)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	defer once.Do(func() { close(release) })
	writer := NewBatchWriter(NewVMClient(server.URL, time.Second, time.Second))
	t.Cleanup(writer.client.httpClient.CloseIdleConnections)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 预先入队，使两个请求确定进入同一次导入。
	r1 := &writeRequest{ctx: ctx, done: make(chan error, 1)}
	r2 := &writeRequest{ctx: context.Background(), done: make(chan error, 1)}
	r1.payload, _ = encodeMetrics(testMetric(1))
	r2.payload, _ = encodeMetrics(testMetric(2))
	writer.slots <- struct{}{}
	writer.slots <- struct{}{}
	writer.queue <- r1
	writer.queue <- r2
	writer.workers = 1
	go writer.run()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("import never started")
	}
	cancel()
	once.Do(func() { close(release) })
	if err := <-r2.done; err != nil {
		t.Fatalf("other agent import canceled: %v", err)
	}
	if got := received.Load(); got != 2 {
		t.Fatalf("received %d metrics, want 2 in shared import", got)
	}
}

func TestBatchWriterQueueWaitRespectsContext(t *testing.T) {
	writer := NewBatchWriter(NewVMClient("http://unused.invalid", time.Second, time.Second))
	for range batchQueueSize {
		writer.slots <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := writer.Write(ctx, testMetric(1)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queue wait error: %v", err)
	}
	if len(writer.queue) != 0 {
		t.Fatal("timed out request was enqueued")
	}
}

func TestBatchWriterEncodingErrorIsIsolated(t *testing.T) {
	var imports atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		imports.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	writer := NewBatchWriter(NewVMClient(server.URL, time.Second, time.Second))
	t.Cleanup(writer.client.httpClient.CloseIdleConnections)
	invalid := testMetric(1)
	invalid[0].Values[0] = math.NaN()
	if err := writer.Write(context.Background(), invalid); err == nil {
		t.Fatal("invalid payload was accepted")
	}
	if err := writer.Write(context.Background(), testMetric(2)); err != nil {
		t.Fatal(err)
	}
	if imports.Load() != 1 || len(writer.slots) != 0 {
		t.Fatal("invalid payload was imported or slot leaked")
	}
}

func TestBatchWriterBoundsCombinedPayloads(t *testing.T) {
	var sizes []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sizes = append(sizes, len(body))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	writer := NewBatchWriter(NewVMClient(server.URL, time.Second, time.Second))
	t.Cleanup(writer.client.httpClient.CloseIdleConnections)
	var requests []*writeRequest
	// 三个可合并请求，以及一个超过合并目标、必须独立导入的请求。
	for _, labelSize := range []int{batchMaxBytes / 3, batchMaxBytes / 3, batchMaxBytes / 3, batchMaxBytes + 100} {
		metrics := testMetric(len(requests))
		metrics[0].Metric["large_label"] = strings.Repeat("x", labelSize)
		payload, err := encodeMetrics(metrics)
		if err != nil {
			t.Fatal(err)
		}
		r := &writeRequest{ctx: context.Background(), payload: payload, done: make(chan error, 1)}
		requests = append(requests, r)
		writer.slots <- struct{}{}
		writer.queue <- r
	}
	writer.workers = 1
	go writer.run()
	for _, r := range requests {
		if err := <-r.done; err != nil {
			t.Fatal(err)
		}
	}
	if len(sizes) != 3 {
		t.Fatalf("import sizes = %v, want three bounded imports", sizes)
	}
	if sizes[0] > batchMaxBytes || sizes[1] > batchMaxBytes || sizes[2] != len(requests[3].payload) {
		t.Fatalf("unexpected combined payload sizes: %v", sizes)
	}
}
