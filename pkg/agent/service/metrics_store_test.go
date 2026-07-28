package service

import (
	"testing"

	"github.com/dushixiang/pika/internal/protocol"
)

func metricSample(metricType protocol.MetricType, timestamp int64) protocol.MetricSample {
	return protocol.MetricSample{Type: metricType, Timestamp: timestamp}
}

func TestMetricsStorePendingRequiresAck(t *testing.T) {
	store := newMetricsStore()
	store.put([]protocol.MetricSample{metricSample(protocol.MetricTypeCPU, 100)})

	first, cursor := store.pending()
	second, _ := store.pending()
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("unacknowledged sample must remain pending: first=%d second=%d", len(first), len(second))
	}

	store.ack(cursor)
	afterAck, _ := store.pending()
	if len(afterAck) != 0 {
		t.Fatalf("acknowledged sample returned again: %v", afterAck)
	}
}

func TestMetricsStoreAckDoesNotConsumeConcurrentUpdate(t *testing.T) {
	store := newMetricsStore()
	store.put([]protocol.MetricSample{metricSample(protocol.MetricTypeCPU, 100)})
	_, cursor := store.pending()

	store.put([]protocol.MetricSample{metricSample(protocol.MetricTypeMemory, 100)})
	store.ack(cursor)

	pending, _ := store.pending()
	if len(pending) != 1 || pending[0].Type != protocol.MetricTypeMemory {
		t.Fatalf("concurrent update was consumed by an older ack: %v", pending)
	}
}

func TestMetricsStoreUsesSequenceInsteadOfTimestampCursor(t *testing.T) {
	store := newMetricsStore()
	store.put([]protocol.MetricSample{metricSample(protocol.MetricTypeCPU, 100)})
	_, cursor := store.pending()
	store.ack(cursor)

	store.put([]protocol.MetricSample{metricSample(protocol.MetricTypeMemory, 100)})
	pending, _ := store.pending()
	if len(pending) != 1 || pending[0].Type != protocol.MetricTypeMemory {
		t.Fatalf("sample sharing an acknowledged timestamp was lost: %v", pending)
	}
}

func TestMetricsStoreIgnoresAckFromPreviousGeneration(t *testing.T) {
	store := newMetricsStore()
	store.put([]protocol.MetricSample{metricSample(protocol.MetricTypeCPU, 100)})
	_, oldCursor := store.pending()

	store.reset()
	store.ack(oldCursor)

	pending, _ := store.pending()
	if len(pending) != 1 {
		t.Fatalf("old connection ack consumed reset snapshot: %v", pending)
	}
}

func TestMetricsStoreLastInsertedSampleWinsWithinBatch(t *testing.T) {
	store := newMetricsStore()
	store.put([]protocol.MetricSample{
		metricSample(protocol.MetricTypeCPU, 200),
		metricSample(protocol.MetricTypeCPU, 100),
	})

	pending, _ := store.pending()
	if len(pending) != 1 || pending[0].Timestamp != 100 {
		t.Fatalf("last inserted sample did not win: %v", pending)
	}
}

func TestMetricsStoreAcceptsUpdateAfterClockMovesBackward(t *testing.T) {
	store := newMetricsStore()
	store.put([]protocol.MetricSample{metricSample(protocol.MetricTypeCPU, 200)})
	_, cursor := store.pending()
	store.ack(cursor)

	store.put([]protocol.MetricSample{metricSample(protocol.MetricTypeCPU, 100)})
	pending, _ := store.pending()
	if len(pending) != 1 || pending[0].Timestamp != 100 {
		t.Fatalf("sample collected after clock moved backward was lost: %v", pending)
	}
}
