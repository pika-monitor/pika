package vmclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testMetric(id int) []Metric {
	return []Metric{{Metric: map[string]string{"__name__": "cpu", "agent_id": fmt.Sprint(id)}, Values: []float64{42}, Timestamps: []int64{123456789}}}
}

func TestWriteReusesConnections(t *testing.T) {
	var connections atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		// 200 响应允许带 body；必须读取到 EOF 才能复用 HTTP/1.1 连接。
		_, _ = io.WriteString(w, strings.Repeat("ok", 4096))
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	defer server.Close()
	client := NewVMClient(server.URL, time.Second, time.Second)
	t.Cleanup(client.httpClient.CloseIdleConnections)
	for i := range 20 {
		if err := client.Write(context.Background(), testMetric(i)); err != nil {
			t.Fatal(err)
		}
	}
	if got := connections.Load(); got != 1 {
		t.Fatalf("20 sequential writes opened %d connections, want 1", got)
	}
}

func TestConcurrentWritesBoundConnections(t *testing.T) {
	const writers = 1000
	var connections atomic.Int64
	var received sync.Map
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/import" || r.Header.Get("Content-Type") != "application/x-ndjson" {
			t.Errorf("unexpected import request: %s", r.URL)
		}
		var metric Metric
		decoder := json.NewDecoder(r.Body)
		if err := decoder.Decode(&metric); err != nil {
			t.Errorf("decode import: %v", err)
		}
		if len(metric.Values) != 1 || metric.Values[0] != 42 || len(metric.Timestamps) != 1 || metric.Timestamps[0] != 123456789 {
			t.Errorf("import payload changed: %+v", metric)
		}
		if _, loaded := received.LoadOrStore(metric.Metric["agent_id"], true); loaded {
			t.Errorf("duplicate import: %s", metric.Metric["agent_id"])
		}
		_, _ = io.Copy(io.Discard, r.Body)
		time.Sleep(5 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	defer server.Close()
	client := NewVMClient(server.URL, 15*time.Second, time.Second)
	t.Cleanup(client.httpClient.CloseIdleConnections)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			<-start
			if err := client.Write(context.Background(), testMetric(i)); err != nil {
				t.Errorf("write %d: %v", i, err)
			}
		})
	}
	close(start)
	wg.Wait()
	if got := connections.Load(); got > 128 {
		t.Errorf("1000 concurrent writes opened %d connections, want <= 128", got)
	}
	count := 0
	received.Range(func(_, _ any) bool { count++; return true })
	if count != writers {
		t.Errorf("received %d imports, want %d", count, writers)
	}
}

func TestQueryUsesQueryTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(80 * time.Millisecond)
		_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer server.Close()
	client := NewVMClient(server.URL, 20*time.Millisecond, time.Second)
	t.Cleanup(client.httpClient.CloseIdleConnections)
	for _, query := range []func() error{
		func() error { _, err := client.Query(context.Background(), "up"); return err },
		func() error {
			_, err := client.QueryRange(context.Background(), "up", time.Now().Add(-time.Hour), time.Now(), time.Minute)
			return err
		},
	} {
		if err := query(); err != nil {
			t.Errorf("query was limited by write timeout: %v", err)
		}
	}
}

func TestWaitingForConnectionRespectsContext(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if requests.Add(1) == 1 {
			close(entered)
		}
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := NewVMClient(server.URL, 5*time.Second, time.Second)
	client.httpClient.Transport.(*http.Transport).MaxConnsPerHost = 1
	t.Cleanup(client.httpClient.CloseIdleConnections)
	firstDone := make(chan error, 1)
	go func() { firstDone <- client.Write(context.Background(), testMetric(1)) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		releaseOnce.Do(func() { close(release) })
		t.Fatal("first write did not reach server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := client.Write(ctx, testMetric(2))
	releaseOnce.Do(func() { close(release) })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("queued write error = %v, want context deadline exceeded", err)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("queued canceled write reached server: %d requests", got)
	}
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	// 取消排队请求后，连接池仍应能处理后续写入。
	if err := client.Write(context.Background(), testMetric(3)); err != nil {
		t.Fatal(err)
	}
}

func TestOperationsReuseConnectionAfterReadingResponses(t *testing.T) {
	var connections atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		switch r.URL.Path {
		case "/api/v1/import":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "temporarily unavailable")
		case "/api/v1/admin/tsdb/delete_series":
			_, _ = io.WriteString(w, strings.Repeat("ok", 4096))
		case "/api/v1/label/agent_id/values":
			_, _ = io.WriteString(w, `{"status":"success","data":["agent-1"]}`+strings.Repeat(" ", 8192))
		default:
			_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`+strings.Repeat(" ", 8192))
		}
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	defer server.Close()
	client := NewVMClient(server.URL, time.Second, time.Second)
	t.Cleanup(client.httpClient.CloseIdleConnections)
	for range 5 {
		if err := client.Write(context.Background(), testMetric(1)); err == nil || !strings.Contains(err.Error(), "503: temporarily unavailable") {
			t.Fatalf("write must preserve server error: %v", err)
		}
		if _, err := client.Query(context.Background(), "up"); err != nil {
			t.Fatal(err)
		}
		if _, err := client.QueryRange(context.Background(), "up", time.Now().Add(-time.Hour), time.Now(), time.Minute); err != nil {
			t.Fatal(err)
		}
		if _, err := client.GetLabelValues(context.Background(), "agent_id", nil); err != nil {
			t.Fatal(err)
		}
		if err := client.DeleteSeries(context.Background(), []string{"up"}); err != nil {
			t.Fatal(err)
		}
	}
	if got := connections.Load(); got != 1 {
		t.Fatalf("sequential operations opened %d connections, want 1", got)
	}
}
