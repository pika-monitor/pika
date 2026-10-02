package service

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/pika-monitor/pika/internal/protocol"
	"github.com/pika-monitor/pika/pkg/agent/collector"
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
		scheduler.collect(time.Unix(100, 0))
		close(finished)
	}()
	<-started
	samples, _ := scheduler.collect(time.Unix(101, 0))
	if len(samples) != 0 || calls.Load() != 1 {
		t.Error("running collector was invoked again")
	}
	close(release)
	<-finished
	scheduler.collect(time.Unix(102, 0))
	if calls.Load() != 2 {
		t.Fatal("collector did not resume")
	}
}

func TestSchedulerDefaultIntervals(t *testing.T) {
	want := map[string]time.Duration{
		"cpu": 2 * time.Second, "memory": 5 * time.Second,
		"disk_io": 2 * time.Second, "network": 2 * time.Second,
		"gpu": 5 * time.Second, "network_connection": 10 * time.Second,
		"temperature": 15 * time.Second, "disk": 60 * time.Second,
		"host": 60 * time.Second,
	}
	scheduler := newMetricsScheduler(collector.NewManager(nil))
	if len(scheduler.collectors) != len(want) {
		t.Fatal("unexpected collector count")
	}
	for index := range scheduler.collectors {
		spec := &scheduler.collectors[index]
		if spec.interval != want[spec.name] {
			t.Fatalf("%s interval = %s, want %s", spec.name, spec.interval, want[spec.name])
		}
	}
}

func TestSchedulerUsesDeadlinesWithoutCatchUp(t *testing.T) {
	var calls atomic.Int32
	scheduler := &metricsScheduler{collectors: []collectorSpec{{
		name: "test", interval: 2 * time.Second,
		fn: func() (protocol.MetricSample, error) {
			calls.Add(1)
			return protocol.MetricSample{}, nil
		},
	}}}
	now := time.Unix(100, 0)
	for _, check := range []struct {
		elapsed time.Duration
		want    int32
	}{
		{0, 1},
		{time.Second, 1},
		{2*time.Second + time.Millisecond, 2},
		{4 * time.Second, 3},
		{101 * time.Second, 4},
		{101*time.Second + time.Millisecond, 4},
		{102 * time.Second, 5},
	} {
		scheduler.collect(now.Add(check.elapsed))
		if calls.Load() != check.want {
			t.Fatalf("at %s: calls=%d, want=%d", check.elapsed, calls.Load(), check.want)
		}
	}
}

func TestSchedulerReleasesCollectorAfterPanic(t *testing.T) {
	scheduler := &metricsScheduler{collectors: []collectorSpec{{
		name: "panic", required: true, interval: time.Second,
		fn: func() (protocol.MetricSample, error) { panic("test") },
	}}}
	for tick := uint64(0); tick < 2; tick++ {
		_, hasError := scheduler.collect(time.Unix(100+int64(tick), 0))
		if !hasError || scheduler.collectors[0].running.Load() {
			t.Fatal("panic did not release collector")
		}
	}
}
