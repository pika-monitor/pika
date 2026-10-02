package metric

import (
	"maps"
	"sync"

	"github.com/pika-monitor/pika/internal/protocol"
)

// DiskSummary 磁盘汇总数据
type DiskSummary struct {
	UsagePercent float64 `json:"usagePercent"` // 平均使用率
	TotalDisks   int     `json:"totalDisks"`   // 磁盘数量
	Total        uint64  `json:"total"`        // 总容量(字节)
	Used         uint64  `json:"used"`         // 已使用(字节)
	Free         uint64  `json:"free"`         // 空闲(字节)
}

// NetworkSummary 网络汇总数据
type NetworkSummary struct {
	TotalBytesSentRate  uint64 `json:"totalBytesSentRate"`  // 总发送速率(字节/秒)
	TotalBytesRecvRate  uint64 `json:"totalBytesRecvRate"`  // 总接收速率(字节/秒)
	TotalBytesSentTotal uint64 `json:"totalBytesSentTotal"` // 累计总发送流量
	TotalBytesRecvTotal uint64 `json:"totalBytesRecvTotal"` // 累计总接收流量
	TotalInterfaces     int    `json:"totalInterfaces"`     // 网卡数量
}

// DiskIOSummary 磁盘IO汇总数据
type DiskIOSummary struct {
	TotalReadBytesRate  uint64 `json:"totalReadBytesRate"`  // 总读取速率(字节/秒)
	TotalWriteBytesRate uint64 `json:"totalWriteBytesRate"` // 总写入速率(字节/秒)
	TotalDevices        int    `json:"totalDevices"`        // 设备数量
}

// LatestMetrics 最新指标数据（用于API响应）
//
// 并发：写者（websocket 上报路径）通过 Update 取写锁修改字段；读者（HTTP latest 接口）
// 通过 Snapshot 取读锁拷贝出独立对象，避免序列化时与写者并发产生 data race。
// 写者必须始终“整体替换”内部指针/切片字段（不要原地 mutate），这样读者拿到的浅拷贝
// 仍然指向稳定的旧值。
type LatestMetrics struct {
	mu sync.RWMutex `json:"-"`

	// Timestamp 最新指标的服务端接收时间（毫秒）。
	Timestamp         int64                           `json:"timestamp,omitempty"`
	SampleTimestamps  map[protocol.MetricType]int64   `json:"sampleTimestamps,omitempty"`
	CPU               *protocol.CPUData               `json:"cpu,omitempty"`
	Memory            *protocol.MemoryData            `json:"memory,omitempty"`
	Disk              *DiskSummary                    `json:"disk,omitempty"`
	DiskIO            *DiskIOSummary                  `json:"diskIO,omitempty"`
	Network           *NetworkSummary                 `json:"network,omitempty"`
	NetworkInterfaces []protocol.NetworkData          `json:"networkInterfaces,omitempty"`
	NetworkConnection *protocol.NetworkConnectionData `json:"networkConnection,omitempty"`
	Host              *protocol.HostInfoData          `json:"host,omitempty"`
	GPU               []protocol.GPUData              `json:"gpu,omitempty"`
	Temp              []protocol.TemperatureData      `json:"temperature,omitempty"`
	Monitors          []protocol.MonitorData          `json:"monitors,omitempty"`
}

// Update 在写锁内执行 fn。fn 内可以读写任何字段（包括 Timestamp）。
func (lm *LatestMetrics) Update(fn func(*LatestMetrics)) {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	fn(lm)
}

func (lm *LatestMetrics) UpdateSample(metricType protocol.MetricType, timestamp, receivedAt int64, fn func(*LatestMetrics)) {
	lm.Update(func(current *LatestMetrics) {
		fn(current)
		if current.SampleTimestamps == nil {
			current.SampleTimestamps = make(map[protocol.MetricType]int64)
		}
		current.SampleTimestamps[metricType] = timestamp
		if receivedAt > current.Timestamp {
			current.Timestamp = receivedAt
		}
	})
}

// Snapshot 在读锁内对当前状态做浅拷贝。返回的对象拥有独立的零值互斥量，
// 调用方可以安全地读取/序列化/再 sanitize（修改返回对象上的字段不会影响缓存）。
// 内部的指针/切片仍指向旧值，但因为写者每次都整体替换字段，所以是安全的。
func (lm *LatestMetrics) Snapshot() *LatestMetrics {
	lm.mu.RLock()
	defer lm.mu.RUnlock()
	return &LatestMetrics{
		Timestamp:         lm.Timestamp,
		SampleTimestamps:  maps.Clone(lm.SampleTimestamps),
		CPU:               lm.CPU,
		Memory:            lm.Memory,
		Disk:              lm.Disk,
		DiskIO:            lm.DiskIO,
		Network:           lm.Network,
		NetworkInterfaces: lm.NetworkInterfaces,
		NetworkConnection: lm.NetworkConnection,
		Host:              lm.Host,
		GPU:               lm.GPU,
		Temp:              lm.Temp,
		Monitors:          lm.Monitors,
	}
}
