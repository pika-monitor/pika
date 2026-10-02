package service

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/pika-monitor/pika/internal/protocol"
)

func TestSchedulerSkipsRunningCollector(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	var calls atomic.Int32
	scheduler := &metricsScheduler{collectors: []collectorSpec{{
		name: "blocked", interval: time.Second,
		fn: func() (protocol.MetricSample, error) {
			if calls.Add(1) == 1 {
				close(started)
				<-release
			}
			return protocol.MetricSample{}, nil
		},
	}}}
	go func() {
		scheduler.collect(0)
		close(finished)
	}()
	<-started
	samples, _ := scheduler.collect(1)
	if len(samples) != 0 || calls.Load() != 1 {
		t.Error("running collector was invoked again")
	}
	close(release)
	<-finished
	scheduler.collect(2)
	if calls.Load() != 2 {
		t.Fatal("collector did not resume")
	}
}

func TestSchedulerReleasesCollectorAfterPanic(t *testing.T) {
	scheduler := &metricsScheduler{collectors: []collectorSpec{{
		name: "panic", required: true, interval: time.Second,
		fn: func() (protocol.MetricSample, error) { panic("test") },
	}}}
	for tick := uint64(0); tick < 2; tick++ {
		_, hasError := scheduler.collect(tick)
		if !hasError || scheduler.collectors[0].running.Load() {
			t.Fatal("panic did not release collector")
		}
	}
}
