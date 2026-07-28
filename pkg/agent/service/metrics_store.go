package service

import (
	"sort"
	"sync"

	"github.com/dushixiang/pika/internal/protocol"
)

// metricsStore is a constant-size, latest-wins snapshot store. Delivery state
// uses an internal sequence instead of sample timestamps, which may collide or
// move backwards when the system clock changes.
type metricsStore struct {
	mu         sync.RWMutex
	latest     map[protocol.MetricType]versionedMetric
	nextSeq    uint64
	ackedSeq   uint64
	generation uint64
}

type versionedMetric struct {
	sample protocol.MetricSample
	seq    uint64
}

// metricsCursor identifies exactly the snapshot observed by pending. The
// generation prevents a delayed acknowledgement from an old connection from
// advancing the cursor after reset.
type metricsCursor struct {
	seq        uint64
	generation uint64
}

func newMetricsStore() *metricsStore {
	return &metricsStore{latest: make(map[protocol.MetricType]versionedMetric)}
}

// put stores samples in collection order. The last inserted sample for a
// metric type wins, regardless of its wall-clock timestamp.
func (s *metricsStore) put(samples []protocol.MetricSample) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, sample := range samples {
		s.nextSeq++
		s.latest[sample.Type] = versionedMetric{sample: sample, seq: s.nextSeq}
	}
}

// pending returns samples not acknowledged on the current connection. It does
// not mutate delivery state; the caller must acknowledge the cursor only after
// the complete network write succeeds.
func (s *metricsStore) pending() ([]protocol.MetricSample, metricsCursor) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cursor := metricsCursor{seq: s.ackedSeq, generation: s.generation}
	samples := make([]protocol.MetricSample, 0, len(s.latest))
	for _, metric := range s.latest {
		if metric.seq <= s.ackedSeq {
			continue
		}
		samples = append(samples, metric.sample)
		if metric.seq > cursor.seq {
			cursor.seq = metric.seq
		}
	}

	sort.Slice(samples, func(i, j int) bool {
		if samples[i].Timestamp == samples[j].Timestamp {
			return samples[i].Type < samples[j].Type
		}
		return samples[i].Timestamp < samples[j].Timestamp
	})
	return samples, cursor
}

// ack commits a successful write. Stale and old-connection acknowledgements
// are harmless.
func (s *metricsStore) ack(cursor metricsCursor) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if cursor.generation == s.generation && cursor.seq > s.ackedSeq {
		s.ackedSeq = cursor.seq
	}
}

// reset starts a new delivery generation and makes the current full snapshot
// pending for the new connection.
func (s *metricsStore) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.generation++
	s.ackedSeq = 0
}
