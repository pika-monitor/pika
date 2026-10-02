package service

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-orz/toolkit/syncx"
	"github.com/pika-monitor/pika/internal/metric"
	"github.com/pika-monitor/pika/internal/protocol"
	"github.com/pika-monitor/pika/internal/repo"
	"github.com/pika-monitor/pika/internal/vmclient"

	"github.com/go-orz/cache"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// MetricService 指标服务
type MetricService struct {
	logger          *zap.Logger
	agentRepo       *repo.AgentRepo
	monitorRepo     *repo.MonitorRepo
	propertyService *PropertyService
	trafficService  *TrafficService // 流量统计服务
	vmClient        *vmclient.VMClient
	batchWriter     *vmclient.BatchWriter

	latestCache cache.Cache[string, *metric.LatestMetrics] // Agent 最新指标缓存

	monitorLatestCache cache.Cache[string, *metric.LatestMonitorMetrics] // 监控最新指标缓存

	monitorSparklineMu       sync.Mutex
	monitorSparklineCache    map[string]monitorSparklineCacheEntry
	monitorSparklineInflight map[string]*monitorSparklineCall
}

// NewMetricService 创建指标服务
func NewMetricService(logger *zap.Logger, db *gorm.DB, propertyService *PropertyService, trafficService *TrafficService, vmClient *vmclient.VMClient) *MetricService {
	return &MetricService{
		logger:                   logger,
		agentRepo:                repo.NewAgentRepo(db),
		monitorRepo:              repo.NewMonitorRepo(db),
		propertyService:          propertyService,
		trafficService:           trafficService,
		vmClient:                 vmClient,
		batchWriter:              vmclient.NewBatchWriter(vmClient),
		latestCache:              cache.New[string, *metric.LatestMetrics](time.Minute),
		monitorLatestCache:       cache.New[string, *metric.LatestMonitorMetrics](5 * time.Minute), // 监控数据缓存 5 分钟
		monitorSparklineCache:    make(map[string]monitorSparklineCacheEntry),
		monitorSparklineInflight: make(map[string]*monitorSparklineCall),
	}
}

// CleanAgentMetrics 清理指定探针的所有指标数据
func (s *MetricService) CleanAgentMetrics(ctx context.Context, agentID string) error {
	if s.vmClient == nil {
		return fmt.Errorf("vmClient not initialized")
	}

	s.logger.Info("开始清理探针指标数据", zap.String("agentID", agentID))

	// 在 VictoriaMetrics 中删除与该 agent 相关的所有时间序列数据
	matchers := []string{
		fmt.Sprintf(`{agent_id="%s"}`, agentID), // 删除具有该 agent_id 标签的所有时间序列
	}

	if err := s.vmClient.DeleteSeries(ctx, matchers); err != nil {
		s.logger.Error("删除VictoriaMetrics中的探针指标数据失败",
			zap.String("agentID", agentID),
			zap.Error(err))
		return fmt.Errorf("删除VictoriaMetrics中的探针指标数据失败: %w", err)
	}

	s.logger.Info("成功清理探针指标数据", zap.String("agentID", agentID))
	return nil
}

// CleanOrphanedAgentMetrics 批量清理已删除探针的指标数据
func (s *MetricService) CleanOrphanedAgentMetrics(ctx context.Context) error {
	if s.vmClient == nil {
		return fmt.Errorf("vmClient not initialized")
	}

	s.logger.Info("开始清理已删除探针的指标数据")

	// 获取 VictoriaMetrics 中的所有 agent_id 值
	agentIDs, err := s.vmClient.GetLabelValues(ctx, "agent_id", []string{})
	if err != nil {
		s.logger.Error("获取VictoriaMetrics中的agent_id列表失败", zap.Error(err))
		return fmt.Errorf("获取VictoriaMetrics中的agent_id列表失败: %w", err)
	}

	if len(agentIDs) == 0 {
		s.logger.Info("没有在VictoriaMetrics中找到任何agent_id，跳过清理")
		return nil
	}

	// 获取当前数据库中所有存在的 agent id
	dbAgents, err := s.agentRepo.FindAll(ctx)
	if err != nil {
		s.logger.Error("查询数据库中的探针列表失败", zap.Error(err))
		return fmt.Errorf("查询数据库中的探针列表失败: %w", err)
	}

	// 创建现有 agent id 的映射表
	existingAgentIDs := make(map[string]bool)
	for _, agent := range dbAgents {
		existingAgentIDs[agent.ID] = true
	}

	// 找出已不在数据库中存在的 agent id
	var orphanedAgentIDs []string
	for _, agentID := range agentIDs {
		if !existingAgentIDs[agentID] {
			orphanedAgentIDs = append(orphanedAgentIDs, agentID)
		}
	}

	if len(orphanedAgentIDs) == 0 {
		s.logger.Info("没有找到残留的探针指标数据需要清理")
		return nil
	}

	s.logger.Info("发现需清理的残留探针", zap.Strings("agentIDs", orphanedAgentIDs))

	// 逐个清理残留探针的指标数据
	for _, agentID := range orphanedAgentIDs {
		// 1. 清理内存缓存
		s.DeleteAgentLatestMetricsCache(agentID)
		s.CleanAgentFromMonitorCache(agentID)

		// 2. 清理 VictoriaMetrics 数据
		matchers := []string{
			fmt.Sprintf(`{agent_id="%s"}`, agentID),
		}

		if err := s.vmClient.DeleteSeries(ctx, matchers); err != nil {
			s.logger.Error("删除VictoriaMetrics中的残留探针指标数据失败",
				zap.String("agentID", agentID),
				zap.Error(err))
			// 继续处理下一个，不要因为一个失败而终止
			continue
		}

		s.logger.Info("成功清理残留探针指标数据", zap.String("agentID", agentID))
	}

	s.logger.Info("完成残留探针指标数据清理", zap.Int("clearedCount", len(orphanedAgentIDs)))
	return nil
}

// HandleMetricData 处理指标数据
//
// 并发：每次调用都会刷新 latestCache 的 TTL（defer Set），且对 latestMetrics 字段的写入
// 走 Update 在写锁内 commit；读者侧 GetLatestMetrics 通过 Snapshot 取读锁拷贝，避免与
// json.Marshal 之间的 data race。
//
// 时间戳：sample.Timestamp（agent 端 wall clock）只用于写入 VictoriaMetrics 的样本时间，
// LatestMetrics.Timestamp 改用服务端 time.Now()（单调推进），避免 agent NTP 倒拨导致
// 前端 useLiveBuffer 的去重逻辑永久冻结。
func (s *MetricService) HandleMetricData(ctx context.Context, agentID string, metricType string, data json.RawMessage, timestamp int64) error {
	metrics, err := s.PrepareMetricData(ctx, agentID, metricType, data, timestamp)
	if err != nil {
		return err
	}
	return s.WriteMetrics(ctx, metrics)
}

// WriteMetrics 等待合并导入完成后返回，避免可靠消息在数据写入前被 ACK。
func (s *MetricService) WriteMetrics(ctx context.Context, metrics []vmclient.Metric) error {
	if s.batchWriter != nil {
		return s.batchWriter.Write(ctx, metrics)
	}
	return s.vmClient.Write(ctx, metrics)
}

// PrepareMetricData 更新实时缓存并转换指标，不单独发起 HTTP 写入。
func (s *MetricService) PrepareMetricData(ctx context.Context, agentID string, metricType string, data json.RawMessage, timestamp int64) ([]vmclient.Metric, error) {
	if timestamp == 0 {
		timestamp = time.Now().UnixMilli()
	}
	serverNow := time.Now().UnixMilli()

	// 取/建缓存项；defer 里再 Set 一次以刷新 TTL（即便本次没有任何字段变化也能延期）
	latestMetrics, ok := s.latestCache.Get(agentID)
	if !ok {
		latestMetrics = &metric.LatestMetrics{}
	}
	defer s.latestCache.Set(agentID, latestMetrics, time.Hour)

	// 推进缓存时间戳：服务端 wall clock，永远向前
	latestMetrics.Update(func(lm *metric.LatestMetrics) {
		lm.Timestamp = serverNow
	})

	// 解析数据并转换为 VictoriaMetrics 指标，由调用方合并写入。
	switch protocol.MetricType(metricType) {
	case protocol.MetricTypeCPU:
		var cpuData protocol.CPUData
		if err := json.Unmarshal(data, &cpuData); err != nil {
			return nil, err
		}
		latestMetrics.Update(func(lm *metric.LatestMetrics) {
			lm.CPU = &cpuData
		})
		metrics := s.convertToMetrics(agentID, metricType, &cpuData, timestamp)
		return metrics, nil

	case protocol.MetricTypeMemory:
		var memData protocol.MemoryData
		if err := json.Unmarshal(data, &memData); err != nil {
			return nil, err
		}
		latestMetrics.Update(func(lm *metric.LatestMetrics) {
			lm.Memory = &memData
		})
		metrics := s.convertToMetrics(agentID, metricType, &memData, timestamp)
		return metrics, nil

	case protocol.MetricTypeDisk:
		var diskDataList []protocol.DiskData
		if err := json.Unmarshal(data, &diskDataList); err != nil {
			return nil, err
		}
		// 无有效磁盘数据时，不更新缓存（保留上一次有效值）
		if len(diskDataList) > 0 {
			var totalTotal, totalUsed, totalFree uint64
			for _, diskData := range diskDataList {
				totalTotal += diskData.Total
				totalUsed += diskData.Used
				totalFree += diskData.Free
			}
			usagePercent := float64(totalUsed) / float64(totalTotal) * 100
			summary := &metric.DiskSummary{
				UsagePercent: usagePercent,
				TotalDisks:   len(diskDataList),
				Total:        totalTotal,
				Used:         totalUsed,
				Free:         totalFree,
			}
			latestMetrics.Update(func(lm *metric.LatestMetrics) {
				lm.Disk = summary
			})
		}
		metrics := s.convertToMetrics(agentID, metricType, diskDataList, timestamp)
		return metrics, nil

	case protocol.MetricTypeNetwork:
		var networkDataList []protocol.NetworkData
		if err := json.Unmarshal(data, &networkDataList); err != nil {
			return nil, err
		}
		// 无有效网络数据时，不更新缓存（保留上一次有效值）
		if len(networkDataList) > 0 {
			var totalSentRate, totalRecvRate uint64
			var totalSentTotal, totalRecvTotal uint64
			for _, netData := range networkDataList {
				totalSentRate += netData.BytesSentRate
				totalRecvRate += netData.BytesRecvRate
				totalSentTotal += netData.BytesSentTotal
				totalRecvTotal += netData.BytesRecvTotal
			}
			summary := &metric.NetworkSummary{
				TotalBytesSentRate:  totalSentRate,
				TotalBytesRecvRate:  totalRecvRate,
				TotalBytesSentTotal: totalSentTotal,
				TotalBytesRecvTotal: totalRecvTotal,
				TotalInterfaces:     len(networkDataList),
			}
			latestMetrics.Update(func(lm *metric.LatestMetrics) {
				lm.Network = summary
				lm.NetworkInterfaces = networkDataList
			})
			// 更新流量统计（此处涉及外部 service，不持锁调用）
			if err := s.trafficService.UpdateAgentTraffic(ctx, agentID, totalRecvTotal, totalSentTotal); err != nil {
				s.logger.Error("更新探针流量统计失败",
					zap.String("agentId", agentID),
					zap.Error(err))
			}
		}
		metrics := s.convertToMetrics(agentID, metricType, networkDataList, timestamp)
		return metrics, nil

	case protocol.MetricTypeNetworkConnection:
		var connData protocol.NetworkConnectionData
		if err := json.Unmarshal(data, &connData); err != nil {
			return nil, err
		}
		latestMetrics.Update(func(lm *metric.LatestMetrics) {
			lm.NetworkConnection = &connData
		})
		metrics := s.convertToMetrics(agentID, metricType, &connData, timestamp)
		return metrics, nil

	case protocol.MetricTypeDiskIO:
		var diskIODataList []*protocol.DiskIOData
		if err := json.Unmarshal(data, &diskIODataList); err != nil {
			return nil, err
		}
		// 无有效数据时不更新缓存（保留上一次有效值）
		if len(diskIODataList) > 0 {
			var totalRead, totalWrite uint64
			for _, ioData := range diskIODataList {
				if ioData == nil {
					continue
				}
				totalRead += ioData.ReadBytesRate
				totalWrite += ioData.WriteBytesRate
			}
			summary := &metric.DiskIOSummary{
				TotalReadBytesRate:  totalRead,
				TotalWriteBytesRate: totalWrite,
				TotalDevices:        len(diskIODataList),
			}
			latestMetrics.Update(func(lm *metric.LatestMetrics) {
				lm.DiskIO = summary
			})
		}
		metrics := s.convertToMetrics(agentID, metricType, diskIODataList, timestamp)
		return metrics, nil

	case protocol.MetricTypeHost:
		var hostData protocol.HostInfoData
		if err := json.Unmarshal(data, &hostData); err != nil {
			return nil, err
		}
		latestMetrics.Update(func(lm *metric.LatestMetrics) {
			lm.Host = &hostData
		})
		return nil, nil

	case protocol.MetricTypeGPU:
		var gpuDataList []protocol.GPUData
		if err := json.Unmarshal(data, &gpuDataList); err != nil {
			return nil, err
		}
		// 无 GPU 数据时，不更新缓存
		if len(gpuDataList) > 0 {
			latestMetrics.Update(func(lm *metric.LatestMetrics) {
				lm.GPU = gpuDataList
			})
		}
		metrics := s.convertToMetrics(agentID, metricType, gpuDataList, timestamp)
		return metrics, nil

	case protocol.MetricTypeTemperature:
		var tempDataList []protocol.TemperatureData
		if err := json.Unmarshal(data, &tempDataList); err != nil {
			return nil, err
		}
		// 无温度数据时，不更新缓存
		if len(tempDataList) > 0 {
			latestMetrics.Update(func(lm *metric.LatestMetrics) {
				lm.Temp = tempDataList
			})
		}
		metrics := s.convertToMetrics(agentID, metricType, tempDataList, timestamp)
		return metrics, nil

	case protocol.MetricTypeMonitor:
		var monitorDataList []protocol.MonitorData
		if err := json.Unmarshal(data, &monitorDataList); err != nil {
			return nil, err
		}
		for i := range monitorDataList {
			monitorDataList[i].AgentId = agentID // 关联探针ID
		}
		latestMetrics.Update(func(lm *metric.LatestMetrics) {
			lm.Monitors = monitorDataList
		})
		for _, monitorData := range monitorDataList {
			s.updateMonitorCache(agentID, &monitorData, timestamp)
		}

		metrics := s.convertToMetrics(agentID, metricType, monitorDataList, timestamp)
		return metrics, nil

	default:
		s.logger.Warn("unknown cpiMetric type", zap.String("type", metricType))
		return nil, nil
	}
}

// GetMetrics 获取聚合指标数据（从 VictoriaMetrics 查询）
// 返回统一的 GetMetricsResponse 格式
func (s *MetricService) GetMetrics(ctx context.Context, agentID, metricType string, start, end int64, interfaceName string, aggregation string) (*metric.GetMetricsResponse, error) {
	step := vmclient.AutoStep(time.UnixMilli(start), time.UnixMilli(end))

	// 构造 PromQL 查询（返回多个查询以支持多系列）
	queries := s.buildPromQLQueries(agentID, metricType, interfaceName, aggregation, step)
	if len(queries) == 0 {
		return nil, fmt.Errorf("unsupported metric type: %s", metricType)
	}

	// 执行查询并转换结果
	// step 设为 0，让 VictoriaMetrics 自动选择合适的步长
	var series []metric.Series

	for _, q := range queries {
		result, err := s.vmClient.QueryRange(ctx, q.Query,
			time.UnixMilli(start),
			time.UnixMilli(end),
			step)
		if err != nil {
			s.logger.Error("查询 VictoriaMetrics 失败",
				zap.String("query", q.Query),
				zap.Error(err))
			continue // 跳过失败的查询，继续处理其他查询
		}

		// 转换查询结果为 MetricSeries
		convertedSeries := s.convertQueryResultToSeries(result, q.Name, q.Labels)
		series = append(series, convertedSeries...)
	}

	// 如果是监控类型，添加监控任务名称到标签中
	if metricType == "monitor" && len(series) > 0 {
		// 收集所有 monitor_id
		monitorIdSet := make(map[string]struct{})
		for _, s := range series {
			if monitorId, ok := s.Labels["monitor_id"]; ok {
				monitorIdSet[monitorId] = struct{}{}
			}
		}

		// 查询 monitor 信息
		if len(monitorIdSet) > 0 {
			monitorIds := make([]string, 0, len(monitorIdSet))
			for monitorId := range monitorIdSet {
				monitorIds = append(monitorIds, monitorId)
			}

			monitors, err := s.monitorRepo.FindByIdIn(ctx, monitorIds)
			if err != nil {
				s.logger.Error("查询 monitor 信息失败", zap.Error(err))
			} else {
				// 构建 monitorId -> monitorName 映射
				monitorNameMap := make(map[string]string)
				for _, monitor := range monitors {
					monitorNameMap[monitor.ID] = monitor.Name
				}

				// 在每个 series 的 labels 中添加 monitor_name
				for i := range series {
					if monitorId, ok := series[i].Labels["monitor_id"]; ok {
						if monitorName, exists := monitorNameMap[monitorId]; exists {
							series[i].Labels["monitor_name"] = monitorName
						}
					}
				}
			}
		}
	}

	return &metric.GetMetricsResponse{
		AgentID: agentID,
		Type:    metricType,
		Range:   fmt.Sprintf("%d-%d", start, end),
		Series:  series,
	}, nil
}

// CleanMonitorCache 清理监控任务缓存中不再关联的探针数据
func (s *MetricService) CleanMonitorCache(ctx context.Context, monitorID string) error {
	// 从缓存读取监控数据
	latestMetrics, ok := s.monitorLatestCache.Get(monitorID)
	if !ok {
		// 缓存不存在，无需清理
		return nil
	}

	// 查询监控任务配置
	monitorTask, err := s.monitorRepo.FindById(ctx, monitorID)
	if err != nil {
		return err
	}

	// 解析目标探针集合（指定探针与标签匹配探针的并集，均未指定时对所有探针生效）
	targetSet, err := resolveMonitorTargetSet(ctx, s.agentRepo, &monitorTask)
	if err != nil {
		return err
	}

	// 只在有过滤条件时清理缓存
	if !targetSet.all {
		// 遍历缓存中的探针，移除不再关联的探针数据
		for agentId := range latestMetrics.Agents.Keys() {
			if !targetSet.Contains(agentId) {
				// 该探针已不再关联到此监控任务，从缓存中移除
				latestMetrics.Agents.Delete(agentId)
				s.logger.Debug("从监控缓存中移除探针",
					zap.String("monitorID", monitorID),
					zap.String("agentID", agentId))
			}
		}
	}

	return nil
}

// CleanAgentFromMonitorCache 从所有监控缓存中移除指定探针的数据
func (s *MetricService) CleanAgentFromMonitorCache(agentID string) {
	// 获取所有监控缓存键
	monitorIDs := s.monitorLatestCache.Keys()

	for _, monitorID := range monitorIDs {
		// 获取监控缓存
		latestMetrics, ok := s.monitorLatestCache.Get(monitorID)
		if !ok {
			continue
		}

		// 从该监控缓存中删除指定探针的数据
		latestMetrics.Agents.Delete(agentID)
		s.logger.Debug("从监控缓存中移除探针数据",
			zap.String("monitorID", monitorID),
			zap.String("agentID", agentID))
	}

	s.logger.Info("已从所有监控缓存中移除探针数据", zap.String("agentID", agentID))
}

// DeleteAgentLatestMetricsCache 删除探针在内存中的最新指标缓存
func (s *MetricService) DeleteAgentLatestMetricsCache(agentID string) {
	s.latestCache.Delete(agentID)
	s.logger.Debug("已删除探针最新指标缓存", zap.String("agentID", agentID))
}

// updateMonitorCache 更新监控数据缓存
func (s *MetricService) updateMonitorCache(agentID string, monitorData *protocol.MonitorData, timestamp int64) {
	monitorID := monitorData.MonitorId

	// 获取或创建监控缓存
	latestMetrics, ok := s.monitorLatestCache.Get(monitorID)
	if !ok {
		latestMetrics = &metric.LatestMonitorMetrics{
			MonitorID: monitorID,
			Agents:    syncx.NewSafeMap[string, *protocol.MonitorData](),
		}
	}

	// 更新探针数据
	latestMetrics.Agents.Set(agentID, monitorData)
	latestMetrics.UpdatedAt = timestamp

	// 保存到缓存（5分钟过期）
	s.monitorLatestCache.Set(monitorID, latestMetrics, 5*time.Minute)
}

// GetLatestMetrics 获取最新指标的快照
// 返回值是缓存项的浅拷贝（无互斥量），调用方可以安全 marshal 或在副本上 sanitize 字段，
// 不会与上报路径产生 data race。
func (s *MetricService) GetLatestMetrics(agentID string) (*metric.LatestMetrics, bool) {
	metrics, ok := s.latestCache.Get(agentID)
	if !ok {
		return nil, false
	}
	return metrics.Snapshot(), true
}

// DeleteAgentMetrics 删除探针在 VictoriaMetrics 中的历史指标
func (s *MetricService) DeleteAgentMetrics(ctx context.Context, agentID string) error {
	if agentID == "" {
		return fmt.Errorf("agentID 不能为空")
	}

	// 删除内存中的最新指标缓存
	s.latestCache.Delete(agentID)

	// 删除 VictoriaMetrics 中该探针的所有 pika 指标
	matcher := fmt.Sprintf(`{__name__=~"pika_.*",agent_id=%q}`, agentID)
	if err := s.vmClient.DeleteSeries(ctx, []string{matcher}); err != nil {
		s.logger.Error("删除探针 VictoriaMetrics 数据失败",
			zap.String("agentID", agentID),
			zap.Error(err))
		return err
	}

	s.logger.Info("探针 VictoriaMetrics 数据删除成功", zap.String("agentID", agentID))
	return nil
}

// DeleteMonitorMetrics 删除服务监控在 VictoriaMetrics 中的历史指标
func (s *MetricService) DeleteMonitorMetrics(ctx context.Context, monitorID string) error {
	if monitorID == "" {
		return fmt.Errorf("monitorID 不能为空")
	}

	// 删除内存中的监控缓存
	s.monitorLatestCache.Delete(monitorID)

	// 删除 VictoriaMetrics 中该监控任务的所有 pika monitor 指标
	matcher := fmt.Sprintf(`{__name__=~"pika_monitor_.*",monitor_id=%q}`, monitorID)
	if err := s.vmClient.DeleteSeries(ctx, []string{matcher}); err != nil {
		s.logger.Error("删除服务监控 VictoriaMetrics 数据失败",
			zap.String("monitorID", monitorID),
			zap.Error(err))
		return err
	}

	s.logger.Info("服务监控 VictoriaMetrics 数据删除成功", zap.String("monitorID", monitorID))
	return nil
}

// GetAvailableNetworkInterfaces 获取探针的可用网卡列表（从 VictoriaMetrics 查询）
func (s *MetricService) GetAvailableNetworkInterfaces(ctx context.Context, agentID string) ([]string, error) {
	// 查询 interface label 的所有值，排除空字符串（汇总数据）
	match := []string{fmt.Sprintf(`pika_network_sent_bytes_rate{agent_id="%s"}`, agentID)}
	allInterfaces, err := s.vmClient.GetLabelValues(ctx, "interface", match)
	if err != nil {
		s.logger.Error("查询网卡列表失败",
			zap.String("agentID", agentID),
			zap.Error(err))
		return []string{}, nil // 返回空列表而不是错误
	}

	// 过滤掉空字符串（汇总数据）
	interfaces := make([]string, 0, len(allInterfaces))
	for _, iface := range allInterfaces {
		if iface != "" {
			interfaces = append(interfaces, iface)
		}
	}

	return interfaces, nil
}

// buildPromQLQueries 构造 PromQL 查询列表（支持多系列）
func (s *MetricService) buildPromQLQueries(agentID, metricType string, interfaceName string, aggregation string, step time.Duration) []metric.QueryDefinition {
	var queries []metric.QueryDefinition

	switch metricType {
	case "cpu":
		queries = []metric.QueryDefinition{{
			Name:  "usage",
			Query: fmt.Sprintf(`pika_cpu_usage_percent{agent_id="%s"}`, agentID),
		}}

	case "memory":
		queries = []metric.QueryDefinition{{
			Name:  "usage",
			Query: fmt.Sprintf(`pika_memory_usage_percent{agent_id="%s"}`, agentID),
		}}

	case "disk":
		queries = []metric.QueryDefinition{{
			Name:  "usage",
			Query: fmt.Sprintf(`pika_disk_usage_percent{agent_id="%s",mount_point=""}`, agentID),
		}}

	case "network":
		// 网络流量：上行和下行
		if interfaceName != "" && interfaceName != "all" {
			// 指定网卡
			queries = []metric.QueryDefinition{
				{
					Name:   "upload",
					Query:  fmt.Sprintf(`pika_network_sent_bytes_rate{agent_id="%s",interface="%s"}`, agentID, interfaceName),
					Labels: map[string]string{"interface": interfaceName},
				},
				{
					Name:   "download",
					Query:  fmt.Sprintf(`pika_network_recv_bytes_rate{agent_id="%s",interface="%s"}`, agentID, interfaceName),
					Labels: map[string]string{"interface": interfaceName},
				},
			}
		} else {
			// 所有网卡汇总
			queries = []metric.QueryDefinition{
				{
					Name:  "upload",
					Query: fmt.Sprintf(`sum(pika_network_sent_bytes_rate{agent_id="%s"}) by (agent_id)`, agentID),
				},
				{
					Name:  "download",
					Query: fmt.Sprintf(`sum(pika_network_recv_bytes_rate{agent_id="%s"}) by (agent_id)`, agentID),
				},
			}
		}

	case "network_connection":
		// 网络连接统计：多个状态
		queries = []metric.QueryDefinition{
			{Name: "established", Query: fmt.Sprintf(`pika_network_conn_established{agent_id="%s"}`, agentID)},
			{Name: "time_wait", Query: fmt.Sprintf(`pika_network_conn_time_wait{agent_id="%s"}`, agentID)},
			{Name: "close_wait", Query: fmt.Sprintf(`pika_network_conn_close_wait{agent_id="%s"}`, agentID)},
			{Name: "listen", Query: fmt.Sprintf(`pika_network_conn_listen{agent_id="%s"}`, agentID)},
		}

	case "disk_io":
		// 磁盘 IO：读和写
		queries = []metric.QueryDefinition{
			{Name: "read", Query: fmt.Sprintf(`pika_disk_read_bytes_rate{agent_id="%s"}`, agentID)},
			{Name: "write", Query: fmt.Sprintf(`pika_disk_write_bytes_rate{agent_id="%s"}`, agentID)},
		}

	case "gpu":
		// GPU：利用率和温度（按 GPU 分组）
		queries = []metric.QueryDefinition{
			{
				Name:  "utilization",
				Query: fmt.Sprintf(`pika_gpu_utilization_percent{agent_id="%s"}`, agentID),
			},
			{
				Name:  "temperature",
				Query: fmt.Sprintf(`pika_gpu_temperature_celsius{agent_id="%s"}`, agentID),
			},
		}

	case "temperature":
		// 温度：按传感器类型分组
		queries = []metric.QueryDefinition{{
			Name:  "temperature",
			Query: fmt.Sprintf(`pika_temperature_celsius{agent_id="%s"}`, agentID),
		}}

	case "monitor":
		// 监控：响应时间（该探针参与的所有监控任务）
		queries = []metric.QueryDefinition{{
			Name:  "response_time",
			Query: fmt.Sprintf(`pika_monitor_response_time_ms{agent_id="%s"}`, agentID),
		}}
	}

	return expandAggregationQueries(queries, aggregation, step)
}

// expandAggregationQueries 按聚合模式展开查询：
//   - aggregation="" / "max" → 单条 max_over_time（默认；用峰值聚合，避免 step 增大时丢峰）
//   - aggregation="avg"      → 单条 avg_over_time
//   - aggregation="raw"      → 原查询，不加窗口函数
//
// 当 step <= 0 时退化为 raw（无法计算窗口）。
func expandAggregationQueries(queries []metric.QueryDefinition, aggregation string, step time.Duration) []metric.QueryDefinition {
	windowSeconds := int(step.Seconds())
	if aggregation == "raw" || windowSeconds <= 0 {
		return queries
	}

	window := fmt.Sprintf("%ds", windowSeconds)
	fn := "max_over_time"
	agg := "max"
	if aggregation == "avg" {
		fn = "avg_over_time"
		agg = "avg"
	}

	expanded := make([]metric.QueryDefinition, 0, len(queries))
	for _, q := range queries {
		wrapped := fmt.Sprintf(`%s((%s)[%s:])`, fn, q.Query, window)
		expanded = append(expanded, withAggLabel(q, agg, wrapped))
	}
	return expanded
}

// withAggLabel 复制 QueryDefinition，替换 PromQL 并附加 agg 标签
func withAggLabel(q metric.QueryDefinition, agg, query string) metric.QueryDefinition {
	labels := make(map[string]string, len(q.Labels)+1)
	maps.Copy(labels, q.Labels)
	labels["agg"] = agg
	return metric.QueryDefinition{
		Name:   q.Name,
		Query:  query,
		Labels: labels,
	}
}

// convertQueryResultToSeries 将 VictoriaMetrics 查询结果转换为 MetricSeries
func (s *MetricService) convertQueryResultToSeries(result *vmclient.QueryResult, seriesName string, extraLabels map[string]string) []metric.Series {
	if result == nil || len(result.Data.Result) == 0 {
		return nil
	}

	var allSeries []metric.Series

	// 遍历所有时间序列
	for _, timeSeries := range result.Data.Result {
		// 提取数据点
		var dataPoints []metric.DataPoint
		for _, valueArray := range timeSeries.Values {
			if len(valueArray) != 2 {
				continue
			}

			// valueArray: [timestamp(float64), value(string)]
			timestamp, ok := valueArray[0].(float64)
			if !ok {
				continue
			}
			valueStr, ok := valueArray[1].(string)
			if !ok {
				continue
			}

			value, _ := strconv.ParseFloat(valueStr, 64)
			dataPoints = append(dataPoints, metric.DataPoint{
				Timestamp: int64(timestamp * 1000), // 转换为毫秒
				Value:     value,
			})
		}

		// 合并标签
		labels := make(map[string]string)
		for k, v := range timeSeries.Metric {
			// 只排除 __name__ 内部标签，保留 agent_id（监控功能需要用它来区分探针）
			if k != "__name__" {
				labels[k] = v
			}
		}
		// 添加额外标签
		for k, v := range extraLabels {
			labels[k] = v
		}

		// 构建系列名称（如果有特定标签如 GPU index 或 sensor_label，添加到名称中）
		finalName := seriesName
		if sensorLabel, ok := labels["sensor_label"]; ok {
			finalName = sensorLabel
			delete(labels, "sensor_label") // 已合并到名称中，从标签中删除
		} else if gpuIndex, ok := labels["gpu_index"]; ok {
			finalName = fmt.Sprintf("GPU_%s", gpuIndex)
			delete(labels, "gpu_index")
		}

		// 移除 target 标签（避免数据泄露）
		delete(labels, "target")

		allSeries = append(allSeries, metric.Series{
			Name:   finalName,
			Labels: labels,
			Data:   dataPoints,
		})
	}

	return allSeries
}

// buildMonitorPromQLQueries 构建监控查询的 PromQL 语句
func (s *MetricService) buildMonitorPromQLQueries(monitorID string, aggregation string, step time.Duration) []metric.QueryDefinition {
	queries := []metric.QueryDefinition{
		{Name: "response_time", Query: fmt.Sprintf(`pika_monitor_response_time_ms{monitor_id="%s"}`, monitorID)},
	}
	return expandAggregationQueries(queries, aggregation, step)
}

type monitorSparklineAccumulator struct {
	sum   float64
	count int
	max   float64
}

// aggregateMonitorSparklines 将按探针返回的响应时间序列聚合为每个监控项的平均值和最大值。
func aggregateMonitorSparklines(result *vmclient.QueryResult, allowedMonitorIDs map[string]struct{}) map[string][]metric.MonitorSparklinePoint {
	if result == nil || len(result.Data.Result) == 0 {
		return map[string][]metric.MonitorSparklinePoint{}
	}

	accumulators := make(map[string]map[int64]*monitorSparklineAccumulator)
	for _, series := range result.Data.Result {
		monitorID := series.Metric["monitor_id"]
		if _, allowed := allowedMonitorIDs[monitorID]; !allowed {
			continue
		}

		if accumulators[monitorID] == nil {
			accumulators[monitorID] = make(map[int64]*monitorSparklineAccumulator)
		}
		for _, valueArray := range series.Values {
			if len(valueArray) != 2 {
				continue
			}
			timestamp, timestampOK := valueArray[0].(float64)
			valueString, valueOK := valueArray[1].(string)
			if !timestampOK || !valueOK {
				continue
			}
			value, err := strconv.ParseFloat(valueString, 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				continue
			}

			timestampMillis := int64(timestamp * 1000)
			accumulator := accumulators[monitorID][timestampMillis]
			if accumulator == nil {
				accumulator = &monitorSparklineAccumulator{max: value}
				accumulators[monitorID][timestampMillis] = accumulator
			}
			accumulator.sum += value
			accumulator.count++
			if value > accumulator.max {
				accumulator.max = value
			}
		}
	}

	resultByMonitor := make(map[string][]metric.MonitorSparklinePoint, len(accumulators))
	for monitorID, byTimestamp := range accumulators {
		points := make([]metric.MonitorSparklinePoint, 0, len(byTimestamp))
		for timestamp, accumulator := range byTimestamp {
			if accumulator.count == 0 {
				continue
			}
			points = append(points, metric.MonitorSparklinePoint{
				Timestamp: timestamp,
				Avg:       accumulator.sum / float64(accumulator.count),
				Max:       accumulator.max,
			})
		}
		sort.Slice(points, func(i, j int) bool {
			return points[i].Timestamp < points[j].Timestamp
		})
		resultByMonitor[monitorID] = points
	}

	return resultByMonitor
}

type monitorSparklineCacheEntry struct {
	generatedAt int64
	expiresAt   time.Time
	items       map[string][]metric.MonitorSparklinePoint
}

type monitorSparklineCall struct {
	done        chan struct{}
	generatedAt int64
	items       map[string][]metric.MonitorSparklinePoint
	err         error
}

func normalizeMonitorSparklineIDs(monitorIDs []string) []string {
	unique := make(map[string]struct{}, len(monitorIDs))
	for _, monitorID := range monitorIDs {
		if monitorID != "" {
			unique[monitorID] = struct{}{}
		}
	}

	normalized := make([]string, 0, len(unique))
	for monitorID := range unique {
		normalized = append(normalized, monitorID)
	}
	sort.Strings(normalized)
	return normalized
}

func cloneMonitorSparklines(items map[string][]metric.MonitorSparklinePoint) map[string][]metric.MonitorSparklinePoint {
	cloned := make(map[string][]metric.MonitorSparklinePoint, len(items))
	for monitorID, points := range items {
		cloned[monitorID] = append([]metric.MonitorSparklinePoint(nil), points...)
	}
	return cloned
}

// GetCachedMonitorSparklines 返回带短期缓存的批量走势图，并合并同一查询的并发请求。
func (s *MetricService) GetCachedMonitorSparklines(ctx context.Context, monitorIDs []string, window, ttl time.Duration) (int64, map[string][]metric.MonitorSparklinePoint, error) {
	normalizedIDs := normalizeMonitorSparklineIDs(monitorIDs)
	if len(normalizedIDs) == 0 {
		return time.Now().UnixMilli(), map[string][]metric.MonitorSparklinePoint{}, nil
	}

	key := strconv.FormatInt(window.Milliseconds(), 10) + ":" + strings.Join(normalizedIDs, "\x00")
	now := time.Now()

	s.monitorSparklineMu.Lock()
	if s.monitorSparklineCache == nil {
		s.monitorSparklineCache = make(map[string]monitorSparklineCacheEntry)
	}
	if s.monitorSparklineInflight == nil {
		s.monitorSparklineInflight = make(map[string]*monitorSparklineCall)
	}
	if cached, ok := s.monitorSparklineCache[key]; ok {
		if now.Before(cached.expiresAt) {
			s.monitorSparklineMu.Unlock()
			return cached.generatedAt, cloneMonitorSparklines(cached.items), nil
		}
		delete(s.monitorSparklineCache, key)
	}
	if call, ok := s.monitorSparklineInflight[key]; ok {
		s.monitorSparklineMu.Unlock()
		select {
		case <-call.done:
			return call.generatedAt, cloneMonitorSparklines(call.items), call.err
		case <-ctx.Done():
			return 0, nil, ctx.Err()
		}
	}

	call := &monitorSparklineCall{done: make(chan struct{})}
	s.monitorSparklineInflight[key] = call
	s.monitorSparklineMu.Unlock()

	end := time.Now().UnixMilli()
	items, err := s.GetMonitorSparklines(ctx, normalizedIDs, end-window.Milliseconds(), end)
	if err == nil && items == nil {
		items = map[string][]metric.MonitorSparklinePoint{}
	}

	s.monitorSparklineMu.Lock()
	call.generatedAt = end
	call.items = items
	call.err = err
	if err == nil && ttl > 0 {
		cacheNow := time.Now()
		for cacheKey, cached := range s.monitorSparklineCache {
			if !cacheNow.Before(cached.expiresAt) {
				delete(s.monitorSparklineCache, cacheKey)
			}
		}
		s.monitorSparklineCache[key] = monitorSparklineCacheEntry{
			generatedAt: end,
			expiresAt:   cacheNow.Add(ttl),
			items:       cloneMonitorSparklines(items),
		}
	}
	delete(s.monitorSparklineInflight, key)
	close(call.done)
	s.monitorSparklineMu.Unlock()

	return end, cloneMonitorSparklines(items), err
}

// GetMonitorSparklines 使用一次 VictoriaMetrics 查询返回多个监控项的列表走势图。
func (s *MetricService) GetMonitorSparklines(ctx context.Context, monitorIDs []string, start, end int64) (map[string][]metric.MonitorSparklinePoint, error) {
	if len(monitorIDs) == 0 {
		return map[string][]metric.MonitorSparklinePoint{}, nil
	}

	allowedMonitorIDs := make(map[string]struct{}, len(monitorIDs))
	escapedMonitorIDs := make([]string, 0, len(monitorIDs))
	for _, monitorID := range monitorIDs {
		if monitorID == "" {
			continue
		}
		allowedMonitorIDs[monitorID] = struct{}{}
		escapedMonitorIDs = append(escapedMonitorIDs, regexp.QuoteMeta(monitorID))
	}
	if len(escapedMonitorIDs) == 0 {
		return map[string][]metric.MonitorSparklinePoint{}, nil
	}

	query := fmt.Sprintf(`pika_monitor_response_time_ms{monitor_id=~"^(?:%s)$"}`, strings.Join(escapedMonitorIDs, "|"))
	queryResult, err := s.vmClient.QueryRange(ctx, query, time.UnixMilli(start), time.UnixMilli(end), time.Minute)
	if err != nil {
		return nil, err
	}

	return aggregateMonitorSparklines(queryResult, allowedMonitorIDs), nil
}

// GetMonitorHistory 获取监控任务的历史趋势数据
func (s *MetricService) GetMonitorHistory(ctx context.Context, monitorID string, start, end int64, aggregation string) (*metric.GetMetricsResponse, error) {
	// 查询监控任务配置
	monitorTask, err := s.monitorRepo.FindById(ctx, monitorID)
	if err != nil {
		s.logger.Error("查询监控任务失败", zap.String("monitorID", monitorID), zap.Error(err))
		return nil, err
	}

	step := vmclient.AutoStep(time.UnixMilli(start), time.UnixMilli(end))
	queries := s.buildMonitorPromQLQueries(monitorID, aggregation, step)

	var series []metric.Series
	for _, q := range queries {
		result, err := s.vmClient.QueryRange(
			ctx,
			q.Query,
			time.UnixMilli(start),
			time.UnixMilli(end),
			step,
		)
		if err != nil {
			s.logger.Warn("查询历史趋势失败", zap.String("query", q.Name), zap.Error(err))
			continue
		}
		convertedSeries := s.convertQueryResultToSeries(result, q.Name, q.Labels)
		series = append(series, convertedSeries...)
	}

	// 过滤掉已取消关联的 agent 数据（仅在有过滤条件时）
	targetSet, err := resolveMonitorTargetSet(ctx, s.agentRepo, &monitorTask)
	if err != nil {
		s.logger.Error("解析监控目标探针失败", zap.String("monitorID", monitorID), zap.Error(err))
		return nil, err
	}

	agentIdSet := make(map[string]struct{})
	if !targetSet.all {
		// 有过滤条件，只保留当前关联的 agent 数据
		filteredSeries := make([]metric.Series, 0)
		for _, s := range series {
			if agentId, ok := s.Labels["agent_id"]; ok {
				if targetSet.Contains(agentId) {
					filteredSeries = append(filteredSeries, s)
					agentIdSet[agentId] = struct{}{}
				}
			}
		}
		series = filteredSeries
	} else {
		// 无过滤条件，收集所有 agent_id
		for _, s := range series {
			if agentId, ok := s.Labels["agent_id"]; ok {
				agentIdSet[agentId] = struct{}{}
			}
		}
	}

	// 查询 agent 信息
	if len(agentIdSet) > 0 {
		agentIds := make([]string, 0, len(agentIdSet))
		for agentId := range agentIdSet {
			agentIds = append(agentIds, agentId)
		}

		agents, err := s.agentRepo.FindByIdIn(ctx, agentIds)
		if err != nil {
			s.logger.Error("查询 agent 信息失败", zap.Error(err))
		} else {
			// 构建 agentId -> agentName 映射
			agentNameMap := make(map[string]string)
			for _, agent := range agents {
				agentNameMap[agent.ID] = agent.Name
			}

			// 在每个 series 的 labels 中添加 agent_name
			for i := range series {
				if agentId, ok := series[i].Labels["agent_id"]; ok {
					if agentName, exists := agentNameMap[agentId]; exists {
						series[i].Labels["agent_name"] = agentName
					}
				}
			}
		}
	}

	return &metric.GetMetricsResponse{
		AgentID: "", // 监控查询不限定单个agent
		Type:    "monitor",
		Range:   fmt.Sprintf("%d-%d", start, end),
		Series:  series,
	}, nil
}

// GetMonitorAgentStats 获取监控任务各探针的统计数据（只从缓存读取）
func (s *MetricService) GetMonitorAgentStats(ctx context.Context, monitorID string) []protocol.MonitorData {
	// 从缓存读取监控数据
	latestMetrics, ok := s.monitorLatestCache.Get(monitorID)
	if !ok {
		// 缓存不存在，返回空列表
		return []protocol.MonitorData{}
	}

	// 查询监控任务配置
	monitorTask, err := s.monitorRepo.FindById(ctx, monitorID)
	if err != nil {
		s.logger.Error("查询监控任务失败", zap.String("monitorID", monitorID), zap.Error(err))
		return []protocol.MonitorData{}
	}

	// 解析目标探针集合（指定探针与标签匹配探针的并集，均未指定时对所有探针生效）
	targetSet, err := resolveMonitorTargetSet(ctx, s.agentRepo, &monitorTask)
	if err != nil {
		s.logger.Error("解析监控目标探针失败", zap.String("monitorID", monitorID), zap.Error(err))
		return []protocol.MonitorData{}
	}

	// 收集所有当前关联的 agentId（从缓存中过滤）
	agentIds := make([]string, 0)
	for agentId := range latestMetrics.Agents.Keys() {
		if targetSet.Contains(agentId) {
			agentIds = append(agentIds, agentId)
		}
	}

	// 查询 agent 信息
	agents, err := s.agentRepo.FindByIdIn(ctx, agentIds)
	if err != nil {
		s.logger.Error("查询 agent 信息失败", zap.Error(err))
	}

	// 构建 agentId -> agentName 映射
	agentNameMap := make(map[string]string)
	for _, agent := range agents {
		agentNameMap[agent.ID] = agent.Name
	}

	// 转换为数组并填充 agent 名称
	result := make([]protocol.MonitorData, 0, len(agentIds))
	for stat := range latestMetrics.Agents.Values() {
		// 只返回目标集合内的 agent 数据
		if targetSet.Contains(stat.AgentId) {
			stat.AgentName = agentNameMap[stat.AgentId] // 填充 agent 名称
			result = append(result, *stat)
		}
	}
	// 根据响应时间排序
	sort.Slice(result, func(i, j int) bool {
		return result[i].ResponseTime < result[j].ResponseTime
	})

	return result
}

// GetMonitorStats 获取监控任务的聚合统计数据（只从缓存读取）
func (s *MetricService) GetMonitorStats(ctx context.Context, monitorID string) *metric.MonitorStatsResult {
	// 从缓存读取监控数据
	latestMetrics, ok := s.monitorLatestCache.Get(monitorID)
	if !ok {
		// 缓存不存在，返回默认值
		return &metric.MonitorStatsResult{
			Status: "unknown",
		}
	}

	// 查询监控任务配置
	monitorTask, err := s.monitorRepo.FindById(ctx, monitorID)
	if err != nil {
		s.logger.Error("查询监控任务失败", zap.String("monitorID", monitorID), zap.Error(err))
		return &metric.MonitorStatsResult{
			Status: "unknown",
		}
	}

	// 解析目标探针集合（指定探针与标签匹配探针的并集，均未指定时对所有探针生效）
	targetSet, err := resolveMonitorTargetSet(ctx, s.agentRepo, &monitorTask)
	if err != nil {
		s.logger.Error("解析监控目标探针失败", zap.String("monitorID", monitorID), zap.Error(err))
		return &metric.MonitorStatsResult{
			Status: "unknown",
		}
	}

	// 聚合各探针数据
	return s.aggregateMonitorStats(latestMetrics, targetSet)
}

// aggregateMonitorStats 聚合各探针的监控数据
func (s *MetricService) aggregateMonitorStats(latestMetrics *metric.LatestMonitorMetrics, targetSet monitorTargetSet) *metric.MonitorStatsResult {
	result := &metric.MonitorStatsResult{
		Status: "unknown",
	}

	if latestMetrics.Agents.Len() == 0 {
		return result
	}

	var totalResponseTime int64
	var minResponseTime int64 = 9223372036854775807 // math.MaxInt64
	var maxResponseTime int64
	var lastCheckTime int64
	var upCount, downCount, unknownCount int
	var validCount int // 实际聚合的探针数量
	hasCert := false
	var minCertExpiryTime int64
	var minCertDaysLeft int

	for stat := range latestMetrics.Agents.Values() {
		// 只聚合目标集合内的探针数据
		if !targetSet.Contains(stat.AgentId) {
			continue
		}

		validCount++
		totalResponseTime += stat.ResponseTime

		// 计算响应时间的最小值和最大值
		if stat.ResponseTime < minResponseTime {
			minResponseTime = stat.ResponseTime
		}
		if stat.ResponseTime > maxResponseTime {
			maxResponseTime = stat.ResponseTime
		}

		if stat.CheckedAt > lastCheckTime {
			lastCheckTime = stat.CheckedAt
		}

		// 统计各状态的探针数量
		switch stat.Status {
		case "up":
			upCount++
		case "down":
			downCount++
		default:
			unknownCount++
		}

		if stat.CertExpiryTime > 0 {
			if !hasCert || stat.CertExpiryTime < minCertExpiryTime {
				minCertExpiryTime = stat.CertExpiryTime
				minCertDaysLeft = stat.CertDaysLeft
				hasCert = true
			}
		}
	}

	result.AgentCount = validCount
	if validCount > 0 {
		result.ResponseTime = totalResponseTime / int64(validCount)
	}
	result.ResponseTimeMin = minResponseTime
	result.ResponseTimeMax = maxResponseTime
	result.LastCheckTime = lastCheckTime

	// 填充探针状态分布
	result.AgentStats.Up = upCount
	result.AgentStats.Down = downCount
	result.AgentStats.Unknown = unknownCount

	// 聚合状态：只要有一个探针 up，整体就是 up
	if upCount > 0 {
		result.Status = "up"
	} else if downCount > 0 {
		result.Status = "down"
	}

	if hasCert {
		result.CertExpiryTime = minCertExpiryTime
		result.CertDaysLeft = minCertDaysLeft
	}

	return result
}
