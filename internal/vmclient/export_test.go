package vmclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestExportPreservesOriginalSamplesAndChunks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/export" || r.URL.Query().Get("start") != "1.234" || len(r.URL.Query()["match[]"]) != 2 {
			t.Errorf("unexpected request %s", r.URL)
		}
		fmt.Fprintln(w, `{"metric":{"__name__":"cpu"},"values":[1,99],"timestamps":[1234,3234]}`)
		fmt.Fprintln(w, `{"metric":{"__name__":"cpu"},"values":[2],"timestamps":[5234]}`)
	}))
	defer server.Close()
	rows, err := NewVMClient(server.URL, time.Second, time.Second).Export(context.Background(), []string{"cpu", "memory"}, time.UnixMilli(1234), time.UnixMilli(6234))
	if err != nil || len(rows) != 2 || rows[0].Timestamps[1] != 3234 || rows[0].Values[1] != 99 {
		t.Fatalf("samples altered: %+v %v", rows, err)
	}
}

func TestExportRejectsPartialOrInvalidResponse(t *testing.T) {
	for _, body := range []string{`{"metric":{},"values":[1,2],"timestamps":[1]}`, `{"metric":{},"values":[1],"timestamps":[1]}` + "\n" + `broken`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		_, err := NewVMClient(server.URL, time.Second, time.Second).Export(context.Background(), []string{"cpu"}, time.Unix(0, 0), time.Now())
		server.Close()
		if err == nil {
			t.Fatal("accepted invalid/partial export")
		}
	}
}

func TestReadConcurrencyIsBoundedAndQueueHonorsCancellation(t *testing.T) {
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
		time.Sleep(20 * time.Millisecond)
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
	}))
	defer server.Close()
	client := NewVMClient(server.URL, time.Second, time.Second)
	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() {
			if _, err := client.QueryRange(context.Background(), "up", time.Now().Add(-time.Minute), time.Now(), time.Second); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if maxRunning.Load() > 16 || maxRunning.Load() < 2 {
		t.Fatalf("read concurrency %d", maxRunning.Load())
	}
	for range 16 {
		client.querySlots <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := client.Query(ctx, "up"); err == nil {
		t.Fatal("queued query ignored cancellation")
	}
	for range 16 {
		<-client.querySlots
	}
}
