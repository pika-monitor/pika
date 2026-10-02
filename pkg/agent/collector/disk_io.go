package collector

import (
	"sync"
	"time"

	"github.com/pika-monitor/pika/internal/protocol"
	"github.com/shirou/gopsutil/v4/disk"
)

// DiskIOCollector 磁盘 IO 监控采集器
type DiskIOCollector struct {
	mu           sync.Mutex
	previous     map[string]disk.IOCountersStat
	previousTime time.Time
}

// NewDiskIOCollector 创建磁盘 IO 采集器
func NewDiskIOCollector() *DiskIOCollector {
	return &DiskIOCollector{}
}

// Collect 采集磁盘 IO 数据并根据相邻采样的时间差计算速率
func (d *DiskIOCollector) Collect() ([]protocol.DiskIOData, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	secondCounters, err := d.collectOnce()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	elapsed := now.Sub(d.previousTime).Seconds()
	firstStatsMap := d.previous
	d.previous = secondCounters
	d.previousTime = now

	// 计算速率(基于两次采集的差值)
	var diskIODataList []protocol.DiskIOData
	for device, counter := range secondCounters {
		diskIOData := protocol.DiskIOData{
			Device:         device,
			ReadCount:      counter.ReadCount,
			WriteCount:     counter.WriteCount,
			ReadBytes:      counter.ReadBytes,
			WriteBytes:     counter.WriteBytes,
			ReadTime:       counter.ReadTime,
			WriteTime:      counter.WriteTime,
			IoTime:         counter.IoTime,
			IopsInProgress: counter.IopsInProgress,
		}

		// 计算速率(如果第一次采集有数据)
		if firstStat, exists := firstStatsMap[device]; exists && elapsed > 0 {
			readBytesDelta := safeDelta(counter.ReadBytes, firstStat.ReadBytes)
			writeBytesDelta := safeDelta(counter.WriteBytes, firstStat.WriteBytes)
			diskIOData.ReadBytesRate = counterRate(readBytesDelta, elapsed)
			diskIOData.WriteBytesRate = counterRate(writeBytesDelta, elapsed)
		} else {
			// 如果第一次采集没有该设备数据,速率为0
			diskIOData.ReadBytesRate = 0
			diskIOData.WriteBytesRate = 0
		}

		diskIODataList = append(diskIODataList, diskIOData)
	}

	return diskIODataList, nil
}

// collectOnce 执行一次磁盘 IO 数据采集
func (d *DiskIOCollector) collectOnce() (map[string]disk.IOCountersStat, error) {
	ioCounters, err := disk.IOCounters()
	if err != nil {
		return nil, err
	}
	return ioCounters, nil
}
