package service

import (
	"fmt"

	"github.com/pika-monitor/pika/internal/protocol"
	"github.com/pika-monitor/pika/internal/vmclient"
)

// convertToMetrics 将指标数据转换为 VictoriaMetrics Metric 对象
func (s *MetricService) convertToMetrics(agentID string, metricType string, data interface{}, timestamp int64) []vmclient.Metric {
	var metrics []vmclient.Metric

	switch protocol.MetricType(metricType) {
	case protocol.MetricTypeCPU:
		cpuData := data.(*protocol.CPUData)
		metrics = append(metrics, createMetric("pika_cpu_usage_percent", agentID, nil, cpuData.UsagePercent, timestamp))
		metrics = append(metrics, createMetric("pika_cpu_cores_logical", agentID, nil, float64(cpuData.LogicalCores), timestamp))
		metrics = append(metrics, createMetric("pika_cpu_cores_physical", agentID, nil, float64(cpuData.PhysicalCores), timestamp))

	case protocol.MetricTypeMemory:
		memData := data.(*protocol.MemoryData)
		metrics = append(metrics, createMetric("pika_memory_usage_percent", agentID, nil, memData.UsagePercent, timestamp))
		metrics = append(metrics, createMetric("pika_memory_total_bytes", agentID, nil, float64(memData.Total), timestamp))
		metrics = append(metrics, createMetric("pika_memory_used_bytes", agentID, nil, float64(memData.Used), timestamp))
		metrics = append(metrics, createMetric("pika_memory_available_bytes", agentID, nil, float64(memData.Available), timestamp))
		metrics = append(metrics, createMetric("pika_memory_swap_total_bytes", agentID, nil, float64(memData.SwapTotal), timestamp))
		metrics = append(metrics, createMetric("pika_memory_swap_used_bytes", agentID, nil, float64(memData.SwapUsed), timestamp))

	case protocol.MetricTypeDisk:
		diskDataList := data.([]protocol.DiskData)
		for _, diskData := range diskDataList {
			labels := map[string]string{"mount_point": diskData.MountPoint}
			metrics = append(metrics, createMetric("pika_disk_usage_percent", agentID, labels, diskData.UsagePercent, timestamp))
			metrics = append(metrics, createMetric("pika_disk_total_bytes", agentID, labels, float64(diskData.Total), timestamp))
			metrics = append(metrics, createMetric("pika_disk_used_bytes", agentID, labels, float64(diskData.Used), timestamp))
			metrics = append(metrics, createMetric("pika_disk_free_bytes", agentID, labels, float64(diskData.Free), timestamp))
		}

	case protocol.MetricTypeNetwork:
		networkDataList := data.([]protocol.NetworkData)
		for _, netData := range networkDataList {
			labels := map[string]string{"interface": netData.Interface}
			metrics = append(metrics, createMetric("pika_network_sent_bytes_rate", agentID, labels, float64(netData.BytesSentRate), timestamp))
			metrics = append(metrics, createMetric("pika_network_recv_bytes_rate", agentID, labels, float64(netData.BytesRecvRate), timestamp))
			metrics = append(metrics, createMetric("pika_network_sent_bytes_total", agentID, labels, float64(netData.BytesSentTotal), timestamp))
			metrics = append(metrics, createMetric("pika_network_recv_bytes_total", agentID, labels, float64(netData.BytesRecvTotal), timestamp))
		}

	case protocol.MetricTypeNetworkConnection:
		connData := data.(*protocol.NetworkConnectionData)
		metrics = append(metrics, createMetric("pika_network_conn_established", agentID, nil, float64(connData.Established), timestamp))
		metrics = append(metrics, createMetric("pika_network_conn_syn_sent", agentID, nil, float64(connData.SynSent), timestamp))
		metrics = append(metrics, createMetric("pika_network_conn_syn_recv", agentID, nil, float64(connData.SynRecv), timestamp))
		metrics = append(metrics, createMetric("pika_network_conn_time_wait", agentID, nil, float64(connData.TimeWait), timestamp))
		metrics = append(metrics, createMetric("pika_network_conn_close_wait", agentID, nil, float64(connData.CloseWait), timestamp))
		metrics = append(metrics, createMetric("pika_network_conn_listen", agentID, nil, float64(connData.Listen), timestamp))
		metrics = append(metrics, createMetric("pika_network_conn_total", agentID, nil, float64(connData.Total), timestamp))

	case protocol.MetricTypeDiskIO:
		diskIODataList := data.([]*protocol.DiskIOData)
		// 汇总所有磁盘的 IO
		var totalReadRate, totalWriteRate uint64
		for _, diskIOData := range diskIODataList {
			if diskIOData == nil {
				continue
			}
			totalReadRate += diskIOData.ReadBytesRate
			totalWriteRate += diskIOData.WriteBytesRate
		}
		metrics = append(metrics, createMetric("pika_disk_read_bytes_rate", agentID, nil, float64(totalReadRate), timestamp))
		metrics = append(metrics, createMetric("pika_disk_write_bytes_rate", agentID, nil, float64(totalWriteRate), timestamp))

	case protocol.MetricTypeGPU:
		gpuDataList := data.([]protocol.GPUData)
		for _, gpuData := range gpuDataList {
			labels := map[string]string{
				"gpu_index": fmt.Sprintf("%d", gpuData.Index),
				"gpu_name":  gpuData.Name,
			}
			metrics = append(metrics, createMetric("pika_gpu_utilization_percent", agentID, labels, gpuData.Utilization, timestamp))
			metrics = append(metrics, createMetric("pika_gpu_memory_total_bytes", agentID, labels, float64(gpuData.MemoryTotal), timestamp))
			metrics = append(metrics, createMetric("pika_gpu_memory_used_bytes", agentID, labels, float64(gpuData.MemoryUsed), timestamp))
			metrics = append(metrics, createMetric("pika_gpu_temperature_celsius", agentID, labels, gpuData.Temperature, timestamp))
			metrics = append(metrics, createMetric("pika_gpu_power_draw_watts", agentID, labels, gpuData.PowerUsage, timestamp))
		}

	case protocol.MetricTypeTemperature:
		tempDataList := data.([]protocol.TemperatureData)
		for _, tempData := range tempDataList {
			// 只使用稳定的 sensor_label (Type) 作为标签
			// 不使用 sensor_key，因为同一类型的最大值可能来自不同传感器，会导致产生多条时间序列
			labels := map[string]string{
				"sensor_label": tempData.Type,
			}
			metrics = append(metrics, createMetric("pika_temperature_celsius", agentID, labels, tempData.Temperature, timestamp))
		}

	case protocol.MetricTypeMonitor:
		monitorDataList := data.([]protocol.MonitorData)
		for _, monitorData := range monitorDataList {
			labels := map[string]string{
				"monitor_id":   monitorData.MonitorId,
				"monitor_type": monitorData.Type,
				"target":       monitorData.Target,
			}
			metrics = append(metrics, createMetric("pika_monitor_response_time_ms", agentID, labels, float64(monitorData.ResponseTime), timestamp))
		}
	}

	return metrics
}

// createMetric 创建 VictoriaMetrics Metric 对象
func createMetric(metricName, agentID string, extraLabels map[string]string, value float64, timestamp int64) vmclient.Metric {
	// 创建 metric labels，包含 __name__ 和 agent_id
	labels := make(map[string]string)
	labels["__name__"] = metricName
	labels["agent_id"] = agentID

	// 添加额外的 labels
	if extraLabels != nil {
		for k, v := range extraLabels {
			labels[k] = v
		}
	}

	return vmclient.Metric{
		Metric:     labels,
		Values:     []float64{value},
		Timestamps: []int64{timestamp},
	}
}
