package service

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pika-monitor/pika/internal/metric"
	"github.com/pika-monitor/pika/internal/vmclient"
)

const (
	liveResourceWindow   = 5 * time.Minute
	liveMonitorWindow    = 15 * time.Minute
	liveRecentWindow     = time.Minute
	liveHistoryTTL       = 10 * time.Second
	liveMaxAgents        = 512
	liveMaxSeries        = 256
	liveMaxCachedSamples = 1000000
)

type liveMetricDefinition struct{ kind, name string }

var liveMetricDefinitions = map[string]liveMetricDefinition{
	"pika_cpu_usage_percent":        {"cpu", "usage"},
	"pika_memory_usage_percent":     {"memory", "usage"},
	"pika_disk_read_bytes_rate":     {"disk_io", "read"},
	"pika_disk_write_bytes_rate":    {"disk_io", "write"},
	"pika_network_sent_bytes_rate":  {"network", "upload"},
	"pika_network_recv_bytes_rate":  {"network", "download"},
	"pika_network_conn_established": {"network_connection", "established"},
	"pika_network_conn_time_wait":   {"network_connection", "time_wait"},
	"pika_network_conn_close_wait":  {"network_connection", "close_wait"},
	"pika_network_conn_listen":      {"network_connection", "listen"},
	"pika_gpu_utilization_percent":  {"gpu", "utilization"},
	"pika_gpu_temperature_celsius":  {"gpu", "temperature"},
	"pika_temperature_celsius":      {"temperature", "temperature"},
	"pika_monitor_response_time_ms": {"monitor", "response_time"},
}

type liveAgentHistory struct {
	seenAt         time.Time
	historySamples int
	truncated      bool
	recent         map[string]vmclient.Metric
	history        []vmclient.Metric
	expiresAt      time.Time
	historyErr     error
	monitorLabels  map[string]map[string]string
}

func metricIdentity(labels map[string]string) string {
	data, _ := json.Marshal(labels)
	return string(data)
}

// caller holds liveMu. Cleanup is rate limited and all agent/series buffers are bounded.
func (s *MetricService) liveAgentLocked(agentID string, now time.Time) *liveAgentHistory {
	if s.liveAgents == nil {
		s.liveAgents = make(map[string]*liveAgentHistory)
	}
	if !now.Before(s.liveCleanupAt) {
		for id, entry := range s.liveAgents {
			if now.Sub(entry.seenAt) > liveMonitorWindow {
				s.liveCachedSamples -= entry.historySamples
				delete(s.liveAgents, id)
			}
		}
		s.liveCleanupAt = now.Add(time.Minute)
	}
	entry := s.liveAgents[agentID]
	if entry == nil {
		if len(s.liveAgents) >= liveMaxAgents {
			var oldest string
			var oldestAt time.Time
			for id, existing := range s.liveAgents {
				if oldest == "" || existing.seenAt.Before(oldestAt) {
					oldest, oldestAt = id, existing.seenAt
				}
			}
			s.liveCachedSamples -= s.liveAgents[oldest].historySamples
			delete(s.liveAgents, oldest)
		}
		entry = &liveAgentHistory{recent: make(map[string]vmclient.Metric)}
		s.liveAgents[agentID] = entry
	}
	entry.seenAt = now
	return entry
}

// Keep every received sample during the database's ingestion/query delay.
// Older history comes from Export, not from a latest-wins snapshot.
func (s *MetricService) recordLiveMetrics(agentID string, samples []vmclient.Metric) {
	now := time.Now()
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	entry := s.liveAgentLocked(agentID, now)
	cutoff := now.Add(-liveRecentWindow).UnixMilli()
	for key, row := range entry.recent {
		next := filterRawMetric(row, cutoff, now.Add(time.Second*10).UnixMilli())
		if len(next.Values) == 0 {
			delete(entry.recent, key)
		} else {
			entry.recent[key] = next
		}
	}
	for _, row := range samples {
		if _, ok := liveMetricDefinitions[row.Metric["__name__"]]; !ok {
			continue
		}
		key := metricIdentity(row.Metric)
		existing, ok := entry.recent[key]
		if !ok && len(entry.recent) >= liveMaxSeries {
			entry.truncated = true
			continue
		}
		if !ok {
			existing.Metric = maps.Clone(row.Metric)
		}
		existing.Values = append(existing.Values, row.Values...)
		existing.Timestamps = append(existing.Timestamps, row.Timestamps...)
		existing = filterRawMetric(existing, cutoff, now.Add(time.Second*10).UnixMilli())
		// At most 128 points per series, even with excessive/replayed reports.
		if len(existing.Values) > 128 {
			entry.truncated = true
			existing.Values = existing.Values[len(existing.Values)-128:]
			existing.Timestamps = existing.Timestamps[len(existing.Timestamps)-128:]
		}
		entry.recent[key] = existing
	}
}

func filterRawMetric(row vmclient.Metric, start, end int64) vmclient.Metric {
	values := make(map[int64]float64)
	for i, ts := range row.Timestamps {
		if i < len(row.Values) && ts >= start && ts <= end && !math.IsNaN(row.Values[i]) && !math.IsInf(row.Values[i], 0) {
			values[ts] = row.Values[i]
		}
	}
	next := vmclient.Metric{Metric: row.Metric}
	for ts := range values {
		next.Timestamps = append(next.Timestamps, ts)
	}
	sort.Slice(next.Timestamps, func(i, j int) bool { return next.Timestamps[i] < next.Timestamps[j] })
	for _, ts := range next.Timestamps {
		next.Values = append(next.Values, values[ts])
	}
	return next
}

func (s *MetricService) loadLiveHistory(ctx context.Context, agentID string) error {
	result := s.liveHistoryGroup.DoChan(agentID, func() (any, error) {
		now := time.Now()
		s.liveMu.Lock()
		entry := s.liveAgentLocked(agentID, now)
		if now.Before(entry.expiresAt) {
			err := entry.historyErr
			s.liveMu.Unlock()
			return nil, err
		}
		s.liveMu.Unlock()
		// A cancelled browser does not cancel other viewers sharing this load.
		queryCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		names := make([]string, 0, len(liveMetricDefinitions))
		for name := range liveMetricDefinitions {
			names = append(names, name)
		}
		sort.Strings(names)
		matcher := fmt.Sprintf(`{agent_id=%q,__name__=~%q}`, agentID, strings.Join(names, "|"))
		rows, err := s.vmClient.Export(queryCtx, []string{matcher}, now.Add(-liveMonitorWindow), now)
		labels := make(map[string]map[string]string)
		if err == nil && s.monitorRepo != nil {
			ids := make(map[string]bool)
			for _, row := range rows {
				if id := row.Metric["monitor_id"]; id != "" {
					ids[id] = true
				}
			}
			// Include newly received monitors which may not yet be visible in storage.
			s.liveMu.Lock()
			for _, row := range entry.recent {
				if id := row.Metric["monitor_id"]; id != "" {
					ids[id] = true
				}
			}
			s.liveMu.Unlock()
			list := make([]string, 0, len(ids))
			for id := range ids {
				list = append(list, id)
			}
			if len(list) > 0 {
				monitors, labelErr := s.monitorRepo.FindByIdIn(queryCtx, list)
				if labelErr != nil {
					err = fmt.Errorf("load monitor labels: %w", labelErr)
				} else {
					for _, monitor := range monitors {
						labels[monitor.ID] = map[string]string{"monitor_name": monitor.Name, "interval_ms": strconv.Itoa(monitor.Interval * 1000)}
					}
				}
			}
		}
		if err == nil {
			retained := make([]vmclient.Metric, 0, len(rows))
			for _, row := range rows {
				def, ok := liveMetricDefinitions[row.Metric["__name__"]]
				if !ok || row.Metric["agent_id"] != agentID {
					continue
				}
				start := now.Add(-liveResourceWindow).UnixMilli()
				if def.kind == "monitor" {
					start = now.Add(-liveMonitorWindow).UnixMilli()
				}
				row = filterRawMetric(row, start, now.UnixMilli())
				if len(row.Values) > 0 {
					retained = append(retained, row)
				}
			}
			rows = retained
		}

		s.liveMu.Lock()
		defer s.liveMu.Unlock()
		if s.liveAgents[agentID] != entry {
			return nil, fmt.Errorf("live history invalidated during query")
		}
		if err == nil {

			count := 0
			for _, row := range rows {
				count += len(row.Values)
			}
			s.liveCachedSamples -= entry.historySamples
			for s.liveCachedSamples+count > liveMaxCachedSamples {
				var oldest *liveAgentHistory
				for id, candidate := range s.liveAgents {
					if id != agentID && candidate.historySamples > 0 && (oldest == nil || candidate.seenAt.Before(oldest.seenAt)) {
						oldest = candidate
					}
				}
				if oldest == nil {
					break
				}
				s.liveCachedSamples -= oldest.historySamples
				oldest.history = nil
				oldest.historySamples = 0
				oldest.expiresAt = time.Time{}
			}
			entry.historySamples = count
			s.liveCachedSamples += count
			entry.truncated = false
			entry.history = rows
			entry.monitorLabels = labels
		}
		entry.historyErr = err
		entry.expiresAt = time.Now().Add(liveHistoryTTL)
		if err != nil {
			entry.expiresAt = time.Now().Add(2 * time.Second)
		}
		return nil, err
	})
	select {
	case call := <-result:
		return call.Err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *MetricService) GetLiveMetrics(ctx context.Context, agentID string) (*metric.LiveMetricsResponse, error) {
	err := s.loadLiveHistory(ctx, agentID)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	now := time.Now()
	s.liveMu.Lock()
	entry := s.liveAgentLocked(agentID, now)
	rows := append([]vmclient.Metric(nil), entry.history...)
	for _, row := range entry.recent {
		rows = append(rows, row)
	}
	labels := entry.monitorLabels
	truncated := entry.truncated
	s.liveMu.Unlock()
	if err != nil && len(rows) == 0 {
		return nil, fmt.Errorf("load live history: %w", err)
	}
	response := buildLiveResponse(agentID, rows, now.UnixMilli(), labels)
	if truncated {
		response.HistoryError = "部分实时数据暂缺，等待历史回补"
	}
	if err != nil {
		response.HistoryError = "历史数据刷新失败，当前显示缓存数据"
	}
	return response, nil
}

func buildLiveResponse(agentID string, rows []vmclient.Metric, end int64, monitorLabels map[string]map[string]string) *metric.LiveMetricsResponse {
	response := &metric.LiveMetricsResponse{AgentID: agentID, GeneratedAt: end, Start: end - liveResourceWindow.Milliseconds(), End: end, MonitorStart: end - liveMonitorWindow.Milliseconds(), Series: make(map[string][]metric.Series), LatestSampleAt: make(map[string]int64)}
	grouped := make(map[string]*metric.Series)
	kinds := make(map[string]string)
	for _, row := range rows {
		def, ok := liveMetricDefinitions[row.Metric["__name__"]]
		if !ok || row.Metric["agent_id"] != agentID {
			continue
		}
		start := response.Start
		if def.kind == "monitor" {
			start = response.MonitorStart
		}
		row = filterRawMetric(row, start, end)
		if len(row.Values) == 0 {
			continue
		}
		labels := maps.Clone(row.Metric)
		delete(labels, "__name__")
		delete(labels, "target")
		delete(labels, "agent_id")
		name := def.name
		if def.kind == "gpu" {
			name = "GPU_" + labels["gpu_index"]
			labels["metric_type"] = def.name
		}
		if def.kind == "temperature" {
			name = labels["sensor_label"]
			if name == "" {
				name = def.name
			}
			delete(labels, "sensor_label")
		}
		if def.kind == "monitor" {
			for k, v := range monitorLabels[labels["monitor_id"]] {
				labels[k] = v
			}
		}
		key := def.kind + ":" + name + ":" + metricIdentity(labels)
		series := grouped[key]
		if series == nil {
			series = &metric.Series{Name: name, Labels: labels}
			grouped[key] = series
			kinds[key] = def.kind
		}
		for i, ts := range row.Timestamps {
			series.Data = append(series.Data, metric.DataPoint{Timestamp: ts, Value: row.Values[i]})
		}
	}
	keys := make([]string, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	// Deduplicate overlap (received samples override exported values), preserving peaks.
	for _, key := range keys {
		series := grouped[key]
		points := make(map[int64]float64)
		for _, p := range series.Data {
			points[p.Timestamp] = p.Value
		}
		series.Data = nil
		for ts, value := range points {
			series.Data = append(series.Data, metric.DataPoint{Timestamp: ts, Value: value})
		}
		sort.Slice(series.Data, func(i, j int) bool { return series.Data[i].Timestamp < series.Data[j].Timestamp })
		kind := kinds[key]
		response.Series[kind] = append(response.Series[kind], *series)
		last := series.Data[len(series.Data)-1].Timestamp
		if last > response.LatestSampleAt[kind] {
			response.LatestSampleAt[kind] = last
		}
	}
	// Per-interface samples in one report share a timestamp. Sum only those real
	// samples; do not interpolate asynchronous interfaces or double count totals.
	for _, name := range []string{"upload", "download"} {
		sums := make(map[int64]float64)
		for _, series := range response.Series["network"] {
			if series.Name == name {
				for _, p := range series.Data {
					sums[p.Timestamp] += p.Value
				}
			}
		}
		if len(sums) > 0 {
			total := metric.Series{Name: name, Labels: map[string]string{"interface": ""}}
			for ts, value := range sums {
				total.Data = append(total.Data, metric.DataPoint{Timestamp: ts, Value: value})
			}
			sort.Slice(total.Data, func(i, j int) bool { return total.Data[i].Timestamp < total.Data[j].Timestamp })
			response.Series["network"] = append(response.Series["network"], total)
		}
	}
	return response
}

func (s *MetricService) clearLiveMetrics(agentID string) {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	if entry := s.liveAgents[agentID]; entry != nil {
		s.liveCachedSamples -= entry.historySamples
	}
	delete(s.liveAgents, agentID)
}
