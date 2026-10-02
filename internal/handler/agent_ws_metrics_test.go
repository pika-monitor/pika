package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/pika-monitor/pika/internal/protocol"
	"github.com/pika-monitor/pika/internal/service"
	"github.com/pika-monitor/pika/internal/vmclient"
	ws "github.com/pika-monitor/pika/internal/websocket"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func TestMetricsMessageCombinesSamplesAndPreservesErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		invalid bool
	}{
		{"success", http.StatusNoContent, false},
		{"VM failure", http.StatusServiceUnavailable, false},
		{"invalid sample", http.StatusNoContent, true},
		{"VM failure takes precedence over invalid sample", http.StatusServiceUnavailable, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var imports atomic.Int64
			var got []vmclient.Metric
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				imports.Add(1)
				decoder := json.NewDecoder(r.Body)
				for {
					var m vmclient.Metric
					if err := decoder.Decode(&m); err != nil {
						if err != io.EOF {
							t.Errorf("decode: %v", err)
						}
						break
					}
					got = append(got, m)
				}
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err := database.DB()
			if err != nil {
				t.Fatal(err)
			}
			defer sqlDB.Close()
			h := &AgentHandler{
				logger:        zap.NewNop(),
				metricService: service.NewMetricService(zap.NewNop(), database, nil, nil, vmclient.NewVMClient(server.URL, time.Second, time.Second)),
			}
			batch := protocol.MetricsBatch{Samples: []protocol.MetricSample{
				{Type: protocol.MetricTypeCPU, Data: protocol.CPUData{UsagePercent: 42}, Timestamp: 111},
				{Type: protocol.MetricTypeMemory, Data: protocol.MemoryData{UsagePercent: 24}, Timestamp: 222},
			}}
			if tc.invalid {
				batch.Samples = append(batch.Samples, protocol.MetricSample{Type: protocol.MetricTypeDisk, Data: "invalid"})
			}
			data, err := json.Marshal(batch)
			if err != nil {
				t.Fatal(err)
			}
			err = h.handleMetricsMessage(context.Background(), "agent-1", data)
			var permanent *ws.PermanentMessageError
			if tc.status == http.StatusServiceUnavailable {
				if err == nil || errors.As(err, &permanent) {
					t.Fatalf("VM failure must remain retryable: %v", err)
				}
			} else if tc.invalid {
				if !errors.As(err, &permanent) {
					t.Fatalf("invalid sample must remain permanent: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if imports.Load() != 1 || len(got) != 9 {
				t.Fatalf("got %d imports and %d metrics, want one import with 9 metrics", imports.Load(), len(got))
			}
			for _, m := range got {
				wantTimestamp := int64(222)
				if m.Metric["__name__"] == "pika_cpu_usage_percent" || m.Metric["__name__"] == "pika_cpu_cores_logical" || m.Metric["__name__"] == "pika_cpu_cores_physical" {
					wantTimestamp = 111
				}
				if m.Metric["agent_id"] != "agent-1" || len(m.Timestamps) != 1 || m.Timestamps[0] != wantTimestamp {
					t.Errorf("sample identity/timestamp changed: %+v", m)
				}
			}
		})
	}
}
