package service

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/dushixiang/pika/internal/protocol"
	"github.com/dushixiang/pika/pkg/agent/collector"
)

// collectorBaseInterval 采集循环的基础节奏，所有采集器的采集间隔必须是它的整数倍
const collectorBaseInterval = 1 * time.Second

// slowCollectorThreshold 单个采集器耗时超过此阈值时打印慢日志
const slowCollectorThreshold = 1500 * time.Millisecond

// collectFn 单个采集器的采集函数签名
type collectFn func() (protocol.MetricSample, error)

// collectorSpec 描述一个周期性采集器
type collectorSpec struct {
	name     string
	required bool          // required=true 时采集失败计入错误；GPU/温度等可选项为 false
	interval time.Duration // 必须为 collectorBaseInterval 的整数倍
	fn       collectFn
}

// metricsScheduler 按 tick 调度并行采集，封装到期判定、并发执行、错误分类
type metricsScheduler struct {
	collectors []collectorSpec
}

// newMetricsScheduler 用 manager 构造一次性的采集器表（不再每 tick 重建）
func newMetricsScheduler(m *collector.Manager) *metricsScheduler {
	return &metricsScheduler{
		collectors: []collectorSpec{
			{"cpu", true, 1 * time.Second, m.CollectCPU},
			{"memory", true, 1 * time.Second, m.CollectMemory},
			{"disk_io", true, 1 * time.Second, m.CollectDiskIO},
			{"network", true, 1 * time.Second, m.CollectNetwork},
			{"gpu", false, 1 * time.Second, m.CollectGPU},
			{"network_connection", true, 1 * time.Second, m.CollectNetworkConnection},
			{"temperature", false, 5 * time.Second, m.CollectTemperature},
			{"disk", true, 30 * time.Second, m.CollectDisk},
			{"host", true, 60 * time.Second, m.CollectHost},
		},
	}
}

// collectResult 单个采集器的执行结果
type collectResult struct {
	name     string
	required bool
	sample   protocol.MetricSample
	err      error
}

// collect 执行本次 tick 到期的采集器，返回样本与是否有错误。
// 到期采集器并行执行，每个有 panic 恢复；错误按 ErrNoData/required/optional 三态分类。
func (s *metricsScheduler) collect(tickCount uint64) (samples []protocol.MetricSample, hasError bool) {
	results := make(chan collectResult, len(s.collectors))
	var wg sync.WaitGroup

	for i := range s.collectors {
		c := &s.collectors[i]
		every := uint64(c.interval / collectorBaseInterval)
		if every == 0 || tickCount%every != 0 {
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					slog.Error("采集器 panic", "collector", c.name, "panic", r)
					results <- collectResult{name: c.name, required: c.required, err: fmt.Errorf("panic: %v", r)}
				}
			}()

			start := time.Now()
			sample, err := c.fn()
			if d := time.Since(start); d > slowCollectorThreshold {
				slog.Info("采集耗时", "collector", c.name, "duration", d)
			}
			results <- collectResult{name: c.name, required: c.required, sample: sample, err: err}
		}()
	}

	wg.Wait()
	close(results)

	samples = make([]protocol.MetricSample, 0, len(s.collectors))
	for r := range results {
		if r.err != nil {
			if errors.Is(r.err, collector.ErrNoData) {
				continue
			}
			if r.required {
				slog.Warn("采集指标失败", "collector", r.name, "error", r.err)
				hasError = true
			} else {
				slog.Info("采集可选指标失败", "collector", r.name, "error", r.err)
			}
			continue
		}
		samples = append(samples, r.sample)
	}

	return samples, hasError
}
