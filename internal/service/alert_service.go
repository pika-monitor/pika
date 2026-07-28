package service

import (
	"context"
	"fmt"
	"time"

	"github.com/dushixiang/pika/internal/models"
	"github.com/dushixiang/pika/internal/protocol"
	"github.com/dushixiang/pika/internal/repo"
	"github.com/go-orz/orz"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const (
	agentExpireAlertType         = "agent_expire"
	agentExpireReminderThreshold = 7
)

// AlertService 告警服务
type AlertService struct {
	Service           *orz.Service
	AlertRecordRepo   *repo.AlertRecordRepo
	AlertStateRepo    *repo.AlertStateRepo
	agentRepo         *repo.AgentRepo
	monitorService    *MonitorService
	propertyService   *PropertyService
	notifier          *Notifier
	notificationQueue *NotificationQueue
	logger            *zap.Logger
}

func NewAlertService(logger *zap.Logger, db *gorm.DB, propertyService *PropertyService, monitorService *MonitorService, notifier *Notifier) *AlertService {
	service := &AlertService{
		Service:         orz.NewService(db),
		AlertRecordRepo: repo.NewAlertRecordRepo(db),
		AlertStateRepo:  repo.NewAlertStateRepo(db),
		agentRepo:       repo.NewAgentRepo(db),
		monitorService:  monitorService,
		propertyService: propertyService,
		notifier:        notifier,
		logger:          logger,
	}

	service.notificationQueue = NewNotificationQueue(db, service, service.AlertRecordRepo, logger)
	service.notificationQueue.Start()

	return service
}

// Clear 清空告警记录
func (s *AlertService) Clear(ctx context.Context) error {
	return s.Service.Transaction(ctx, func(ctx context.Context) error {
		// 清空告警记录
		if err := s.AlertRecordRepo.Clear(ctx); err != nil {
			s.logger.Error("清空告警记录失败", zap.Error(err))
			return err
		}

		// 清空告警状态
		if err := s.AlertStateRepo.Clear(ctx); err != nil {
			s.logger.Error("清空告警状态失败", zap.Error(err))
			return err
		}

		return nil
	})
}

// Shutdown 关闭告警服务
func (s *AlertService) Shutdown() {
	s.logger.Info("关闭告警服务")
	s.notificationQueue.Shutdown()
}

// CheckMetrics 检查指标并触发告警
func (s *AlertService) CheckMetrics(ctx context.Context, agentID string, cpu, memory, disk, networkSpeed float64) error {
	// 获取全局告警配置
	alertConfig, err := s.propertyService.GetAlertConfig(ctx)
	if err != nil {
		s.logger.Error("获取全局告警配置失败", zap.Error(err))
		return err
	}

	// 如果全局告警未启用，直接返回
	if !alertConfig.Enabled {
		return nil
	}

	// 获取探针信息（用于发送通知）
	agent, err := s.agentRepo.FindById(ctx, agentID)
	if err != nil {
		s.logger.Error("获取探针信息失败", zap.Error(err))
		return err
	}

	now := time.Now().UnixMilli()

	// 检查 CPU 告警
	if alertConfig.Rules.CPUEnabled {
		s.checkAlert(ctx, alertConfig, &agent, "cpu", cpu, alertConfig.Rules.CPUThreshold, alertConfig.Rules.CPUDuration, now)
	}

	// 检查内存告警
	if alertConfig.Rules.MemoryEnabled {
		s.checkAlert(ctx, alertConfig, &agent, "memory", memory, alertConfig.Rules.MemoryThreshold, alertConfig.Rules.MemoryDuration, now)
	}

	// 检查磁盘告警
	if alertConfig.Rules.DiskEnabled {
		s.checkAlert(ctx, alertConfig, &agent, "disk", disk, alertConfig.Rules.DiskThreshold, alertConfig.Rules.DiskDuration, now)
	}

	// 检查网速告警
	if alertConfig.Rules.NetworkEnabled {
		s.checkAlert(ctx, alertConfig, &agent, "network", networkSpeed, alertConfig.Rules.NetworkThreshold, alertConfig.Rules.NetworkDuration, now)
	}

	return nil
}

// checkAlert 检查单个告警规则
func (s *AlertService) checkAlert(ctx context.Context, config *models.AlertConfig, agent *models.Agent, alertType string, currentValue, threshold float64, duration int, now int64) {
	stateKey := fmt.Sprintf("%s:global:%s", agent.ID, alertType)

	var shouldFire, shouldResolve bool

	// 从数据库加载状态
	state, err := s.AlertStateRepo.GetAlertState(ctx, stateKey)
	if err != nil {
		// 状态不存在，创建新状态
		state = &models.AlertState{
			ID:        stateKey,
			AgentID:   agent.ID,
			AlertType: alertType,
		}
	}

	// 按探针维度更新最新阈值/持续时间，支持配置变更
	state.AgentID = agent.ID
	state.AlertType = alertType
	state.Threshold = threshold
	state.Duration = duration
	state.Value = currentValue
	state.LastCheckTime = now

	if currentValue >= threshold {
		if state.StartTime == 0 {
			state.StartTime = now
		}

		elapsedSeconds := (now - state.StartTime) / 1000
		if elapsedSeconds >= int64(duration) && !state.IsFiring {
			shouldFire = true
			state.IsFiring = true
		}
	} else {
		if state.IsFiring {
			shouldResolve = true
		}
		state.StartTime = 0
	}

	// 保存状态到数据库
	if err := s.AlertStateRepo.SaveAlertState(ctx, state); err != nil {
		s.logger.Error("保存告警状态失败", zap.Error(err))
		return
	}

	if shouldFire {
		s.fireAlert(ctx, config, agent, state)
	}

	if shouldResolve {
		s.resolveAlert(ctx, config, agent, state)
	}
}

// fireAlert 触发告警
func (s *AlertService) fireAlert(ctx context.Context, config *models.AlertConfig, agent *models.Agent, state *models.AlertState) {
	s.logger.Info("触发告警",
		zap.String("agentId", agent.ID),
		zap.String("agentName", agent.Name),
		zap.String("alertType", state.AlertType),
		zap.Float64("value", state.Value),
		zap.Float64("threshold", state.Threshold),
	)

	now := time.Now().UnixMilli()

	// 创建告警记录
	record := &models.AlertRecord{
		AgentID:     agent.ID,
		AgentName:   agent.Name,
		AlertType:   state.AlertType,
		Message:     s.buildAlertMessage(state),
		Threshold:   state.Threshold,
		ActualValue: state.Value,
		Level:       s.calculateLevel(state.Value, state.Threshold),
		Status:      "firing",
		FiredAt:     now,
		CreatedAt:   now,
	}

	err := s.AlertRecordRepo.CreateAlertRecord(ctx, record)
	if err != nil {
		s.logger.Error("创建告警记录失败", zap.Error(err))
		// 不回滚 IsFiring 状态,避免下次检查时重复触发
		// 记录创建失败不影响状态机,下次检查时会重试
		return
	}

	// 更新状态
	state.LastRecordID = record.ID
	if err := s.AlertStateRepo.SaveAlertState(ctx, state); err != nil {
		s.logger.Error("保存告警状态失败", zap.Error(err))
		return
	}

	// 发送通知 - 使用新的 context 避免父 context 取消影响通知发送
	go s.sendAlertNotification(record, agent)
}

// resolveAlert 恢复告警
func (s *AlertService) resolveAlert(ctx context.Context, config *models.AlertConfig, agent *models.Agent, state *models.AlertState) {
	s.logger.Info("告警恢复",
		zap.String("agentId", agent.ID),
		zap.String("agentName", agent.Name),
		zap.String("alertType", state.AlertType),
		zap.Float64("value", state.Value),
	)

	if state.LastRecordID > 0 {
		existingRecord, err := s.AlertRecordRepo.GetAlertRecordByID(ctx, state.LastRecordID)
		if err != nil {
			s.logger.Error("获取告警记录失败", zap.Error(err))
		} else if existingRecord != nil {
			// 只有当记录状态为 firing 时才更新为 resolved
			if existingRecord.Status != "firing" {
				s.logger.Warn("告警记录状态异常,跳过恢复",
					zap.Int64("recordId", existingRecord.ID),
					zap.String("status", existingRecord.Status),
				)
			} else {
				now := time.Now().UnixMilli()
				existingRecord.Status = "resolved"
				existingRecord.ResolvedValue = state.Value
				existingRecord.ResolvedAt = now
				existingRecord.UpdatedAt = now

				err = s.AlertRecordRepo.UpdateAlertRecord(ctx, existingRecord)
				if err != nil {
					s.logger.Error("更新告警记录失败", zap.Error(err))
				} else {
					// 发送恢复通知
					go s.sendAlertNotification(existingRecord, agent)
				}
			}
		}
	}

	// 更新状态
	state.IsFiring = false
	state.LastRecordID = 0
	if err := s.AlertStateRepo.SaveAlertState(ctx, state); err != nil {
		s.logger.Error("保存告警状态失败", zap.Error(err))
	}
}

// buildAlertMessage 构建告警消息
func (s *AlertService) buildAlertMessage(state *models.AlertState) string {
	var alertTypeName string
	switch state.AlertType {
	case "cpu":
		alertTypeName = "CPU使用率"
	case "memory":
		alertTypeName = "内存使用率"
	case "disk":
		alertTypeName = "磁盘使用率"
	case "network":
		return fmt.Sprintf("网速持续%d秒超过%.2fMB/s，当前值%.2fMB/s",
			state.Duration,
			state.Threshold,
			state.Value,
		)
	case "cert":
		return fmt.Sprintf("HTTPS证书剩余天数%.0f天，低于阈值%.0f天", state.Value, state.Threshold)
	case "service":
		return fmt.Sprintf("服务持续离线%d秒", state.Duration)
	default:
		alertTypeName = state.AlertType
	}

	return fmt.Sprintf("%s持续%d秒超过%.2f%%，当前值%.2f%%",
		alertTypeName,
		state.Duration,
		state.Threshold,
		state.Value,
	)
}

// calculateLevel 计算告警级别
func (s *AlertService) calculateLevel(value, threshold float64) string {
	diff := value - threshold

	if diff < 20 {
		return "info"
	} else if diff < 50 {
		return "warning"
	} else {
		return "critical"
	}
}

// sendAlertNotification 发送告警通知(通过队列异步发送)
func (s *AlertService) sendAlertNotification(record *models.AlertRecord, agent *models.Agent) {
	s.notificationQueue.Enqueue(record.ID, agent)
}

// sendAlertNotificationSync 同步发送告警通知(供队列worker调用)
func (s *AlertService) sendAlertNotificationSync(ctx context.Context, record *models.AlertRecord, agent *models.Agent) error {
	alertConfig, err := s.propertyService.GetAlertConfig(ctx)
	if err != nil {
		s.logger.Error("获取告警配置失败", zap.Error(err))
		return fmt.Errorf("获取告警配置失败: %w", err)
	}

	channelConfigs, err := s.propertyService.GetNotificationChannelConfigs(ctx)
	if err != nil {
		s.logger.Error("获取通知渠道配置失败", zap.Error(err))
		return fmt.Errorf("获取通知渠道配置失败: %w", err)
	}

	var enabledChannels []models.NotificationChannelConfig
	for _, channel := range channelConfigs {
		if channel.Enabled {
			enabledChannels = append(enabledChannels, channel)
		}
	}

	if len(enabledChannels) == 0 {
		return fmt.Errorf("没有启用的通知渠道")
	}

	return s.notifier.SendNotificationByConfigs(ctx, enabledChannels, record, agent, alertConfig.MaskIP)
}

// CheckAgentExpireAlerts 检查机器到期提醒
func (s *AlertService) CheckAgentExpireAlerts(ctx context.Context) error {
	alertConfig, err := s.propertyService.GetAlertConfig(ctx)
	if err != nil {
		s.logger.Error("获取全局告警配置失败", zap.Error(err))
		return err
	}

	if !alertConfig.Enabled {
		return nil
	}

	now := time.Now().UnixMilli()
	agents, err := s.agentRepo.FindAll(ctx)
	if err != nil {
		return err
	}

	for _, agent := range agents {
		if err := s.checkAgentExpireAlert(ctx, &agent, now); err != nil {
			s.logger.Error("检查机器过期提醒失败",
				zap.String("agentId", agent.ID),
				zap.Error(err),
			)
		}
	}

	return nil
}

func (s *AlertService) checkAgentExpireAlert(ctx context.Context, agent *models.Agent, now int64) error {
	stateKey := fmt.Sprintf("%s:global:%s:%s", agent.ID, agentExpireAlertType, agent.ID)
	state, err := s.AlertStateRepo.GetAlertState(ctx, stateKey)
	if err != nil {
		state = &models.AlertState{
			ID:        stateKey,
			AgentID:   agent.ID,
			AlertType: agentExpireAlertType,
		}
	}

	daysLeft := agentExpireDaysLeft(agent.ExpireTime, now)
	state.AgentID = agent.ID
	state.AlertType = agentExpireAlertType
	state.Threshold = float64(agentExpireReminderThreshold)
	state.Duration = 0
	state.Value = daysLeft
	state.LastCheckTime = now

	dayMs := int64(24 * time.Hour / time.Millisecond)
	var shouldFire, shouldResolve bool
	if agent.ExpireTime > 0 && agent.ExpireTime-now <= int64(agentExpireReminderThreshold)*dayMs {
		if !state.IsFiring {
			shouldFire = true
			state.IsFiring = true
		}
	} else {
		if state.IsFiring {
			shouldResolve = true
		}
	}

	if err := s.AlertStateRepo.SaveAlertState(ctx, state); err != nil {
		return fmt.Errorf("保存告警状态失败: %w", err)
	}

	if shouldFire {
		s.fireAgentExpireAlert(ctx, agent, state, now)
	}
	if shouldResolve {
		s.resolveAgentExpireAlert(ctx, agent, state)
	}

	return nil
}

func (s *AlertService) fireAgentExpireAlert(ctx context.Context, agent *models.Agent, state *models.AlertState, now int64) {
	s.logger.Info("触发机器过期提醒",
		zap.String("agentId", agent.ID),
		zap.String("agentName", agent.Name),
		zap.Int64("expireTime", agent.ExpireTime),
		zap.Float64("daysLeft", state.Value),
	)

	record := &models.AlertRecord{
		AgentID:     agent.ID,
		AgentName:   agent.Name,
		AlertType:   agentExpireAlertType,
		Message:     s.buildAgentExpireMessage(agent, now),
		Threshold:   float64(agentExpireReminderThreshold),
		ActualValue: state.Value,
		Level:       s.calculateAgentExpireLevel(state.Value),
		Status:      "firing",
		FiredAt:     now,
		CreatedAt:   now,
	}

	if err := s.AlertRecordRepo.CreateAlertRecord(ctx, record); err != nil {
		s.logger.Error("创建机器过期提醒记录失败", zap.Error(err))
		return
	}

	state.LastRecordID = record.ID
	if err := s.AlertStateRepo.SaveAlertState(ctx, state); err != nil {
		s.logger.Error("保存告警状态失败", zap.Error(err))
		return
	}

	go s.sendAlertNotification(record, agent)
}

func (s *AlertService) resolveAgentExpireAlert(ctx context.Context, agent *models.Agent, state *models.AlertState) {
	s.logger.Info("机器过期提醒恢复",
		zap.String("agentId", agent.ID),
		zap.String("agentName", agent.Name),
		zap.Int64("expireTime", agent.ExpireTime),
		zap.Float64("daysLeft", state.Value),
	)

	if state.LastRecordID > 0 {
		existingRecord, err := s.AlertRecordRepo.GetAlertRecordByID(ctx, state.LastRecordID)
		if err != nil {
			s.logger.Error("获取机器过期提醒记录失败", zap.Error(err))
		} else if existingRecord != nil && existingRecord.Status == "firing" {
			now := time.Now().UnixMilli()
			existingRecord.Status = "resolved"
			existingRecord.ResolvedValue = state.Value
			existingRecord.ResolvedAt = now
			existingRecord.UpdatedAt = now

			if err := s.AlertRecordRepo.UpdateAlertRecord(ctx, existingRecord); err != nil {
				s.logger.Error("更新机器过期提醒记录失败", zap.Error(err))
			} else {
				go s.sendAlertNotification(existingRecord, agent)
			}
		}
	}

	state.IsFiring = false
	state.LastRecordID = 0
	if err := s.AlertStateRepo.SaveAlertState(ctx, state); err != nil {
		s.logger.Error("保存告警状态失败", zap.Error(err))
	}
}

func (s *AlertService) buildAgentExpireMessage(agent *models.Agent, now int64) string {
	agentName := agent.Name
	if agentName == "" {
		agentName = agent.Hostname
	}
	if agentName == "" {
		agentName = agent.ID
	}

	expireAt := time.UnixMilli(agent.ExpireTime).Format("2006-01-02 15:04:05")
	if agent.ExpireTime <= now {
		return fmt.Sprintf("机器 %s 已于 %s 过期", agentName, expireAt)
	}

	remaining := agent.ExpireTime - now
	dayMs := int64(24 * time.Hour / time.Millisecond)
	if remaining < dayMs {
		return fmt.Sprintf("机器 %s 将于 %s 过期，剩余不足1天", agentName, expireAt)
	}

	days := remaining / dayMs
	if remaining%dayMs != 0 {
		days++
	}
	return fmt.Sprintf("机器 %s 将于 %s 过期，剩余%d天", agentName, expireAt, days)
}

func (s *AlertService) calculateAgentExpireLevel(daysLeft float64) string {
	if daysLeft <= 0 {
		return "critical"
	}
	if daysLeft <= 3 {
		return "warning"
	}
	return "info"
}

func agentExpireDaysLeft(expireTime int64, now int64) float64 {
	if expireTime <= 0 {
		return 0
	}
	return float64(expireTime-now) / float64(24*time.Hour/time.Millisecond)
}

// CheckMonitorAlerts 检查监控相关告警（证书和服务下线）
func (s *AlertService) CheckMonitorAlerts(ctx context.Context) error {
	// 获取全局告警配置
	alertConfig, err := s.propertyService.GetAlertConfig(ctx)
	if err != nil {
		s.logger.Error("获取全局告警配置失败", zap.Error(err))
		return err
	}

	// 如果全局告警未启用，直接返回
	if !alertConfig.Enabled {
		return nil
	}

	now := time.Now().UnixMilli()

	// 检查证书告警
	if alertConfig.Rules.CertEnabled {
		if err := s.checkCertificateAlerts(ctx, alertConfig, now); err != nil {
			s.logger.Error("检查证书告警失败", zap.Error(err))
		}
	}

	// 检查服务下线告警
	if alertConfig.Rules.ServiceEnabled {
		if err := s.checkServiceDownAlerts(ctx, alertConfig, now); err != nil {
			s.logger.Error("检查服务下线告警失败", zap.Error(err))
		}
	}

	// 检查探针离线告警
	if alertConfig.Rules.AgentOfflineEnabled {
		if err := s.checkAgentOfflineAlerts(ctx, alertConfig, now); err != nil {
			s.logger.Error("检查探针离线告警失败", zap.Error(err))
		}
	}

	return nil
}

// checkCertificateAlerts 检查证书告警
func (s *AlertService) checkCertificateAlerts(ctx context.Context, config *models.AlertConfig, now int64) error {
	// 获取所有最新的监控指标（仅HTTPS类型）
	// 这里需要查询最新的 monitor_metrics 记录，获取证书剩余天数
	monitors, err := s.monitorService.GetLatestMonitorMetricsByType(ctx, "http")
	if err != nil {
		return err
	}

	// 收集所有 agentIds，批量查询探针信息
	agentIdSet := make(map[string]bool)
	for _, monitor := range monitors {
		if monitor.AgentId != "" {
			agentIdSet[monitor.AgentId] = true
		}
	}

	agentIds := make([]string, 0, len(agentIdSet))
	for id := range agentIdSet {
		agentIds = append(agentIds, id)
	}

	agentMap := make(map[string]*models.Agent)
	if len(agentIds) > 0 {
		agents, err := s.agentRepo.ListByIDs(ctx, agentIds)
		if err != nil {
			s.logger.Error("批量获取探针信息失败", zap.Error(err))
			return err
		}
		for i := range agents {
			agentMap[agents[i].ID] = &agents[i]
		}
	}

	for _, monitor := range monitors {
		// 如果证书不存在或已过期，跳过
		if monitor.CertExpiryTime == 0 {
			continue
		}

		certDaysLeft := float64(monitor.CertDaysLeft)

		// 从 map 中获取探针信息
		agent, exists := agentMap[monitor.AgentId]
		if !exists {
			s.logger.Error("探针信息不存在", zap.String("agentId", monitor.AgentId))
			continue
		}

		// 检查证书剩余天数是否低于阈值
		if certDaysLeft <= config.Rules.CertThreshold && certDaysLeft >= 0 {
			// 触发告警（证书告警不需要持续时间，直接触发）
			s.checkCertAlert(ctx, config, agent, &monitor, certDaysLeft, now)
		} else {
			// 恢复告警（如果之前触发过）
			s.resolveCertAlert(ctx, config, agent, &monitor, certDaysLeft)
		}
	}

	return nil
}

// checkCertAlert 检查并触发证书告警
func (s *AlertService) checkCertAlert(ctx context.Context, config *models.AlertConfig, agent *models.Agent, monitor *protocol.MonitorData, certDaysLeft float64, now int64) {
	stateKey := fmt.Sprintf("%s:global:cert:%s", agent.ID, monitor.MonitorId)

	// 从数据库加载状态
	state, err := s.AlertStateRepo.GetAlertState(ctx, stateKey)
	if err != nil {
		// 状态不存在，创建新状态
		state = &models.AlertState{
			ID:        stateKey,
			AgentID:   agent.ID,
			AlertType: "cert",
		}
	}
	state.AgentID = agent.ID
	state.AlertType = "cert"
	state.Threshold = config.Rules.CertThreshold
	state.Duration = 0
	state.Value = certDaysLeft
	state.LastCheckTime = now

	shouldFire := certDaysLeft <= config.Rules.CertThreshold && !state.IsFiring

	if shouldFire {
		state.IsFiring = true
	}

	// 保存状态到数据库
	if err := s.AlertStateRepo.SaveAlertState(ctx, state); err != nil {
		s.logger.Error("保存告警状态失败", zap.Error(err))
		return
	}

	if !shouldFire {
		return
	}

	s.logger.Info("触发证书告警",
		zap.String("agentId", agent.ID),
		zap.String("monitorId", monitor.MonitorId),
		zap.String("monitorName", monitor.MonitorName),
		zap.String("target", monitor.Target),
		zap.Float64("certDaysLeft", certDaysLeft),
		zap.Float64("threshold", config.Rules.CertThreshold),
	)

	// 构建告警消息，优先使用监控任务名称
	var message string
	if monitor.MonitorName != "" {
		message = fmt.Sprintf("监控项 %s (%s) 的HTTPS证书剩余天数%.0f天，低于阈值%.0f天", monitor.MonitorName, monitor.Target, certDaysLeft, config.Rules.CertThreshold)
	} else {
		message = fmt.Sprintf("监控项 %s 的HTTPS证书剩余天数%.0f天，低于阈值%.0f天", monitor.Target, certDaysLeft, config.Rules.CertThreshold)
	}

	record := &models.AlertRecord{
		AgentID:     agent.ID,
		AgentName:   agent.Name,
		AlertType:   "cert",
		Message:     message,
		Threshold:   config.Rules.CertThreshold,
		ActualValue: certDaysLeft,
		Level:       s.calculateCertLevel(certDaysLeft),
		Status:      "firing",
		FiredAt:     now,
		CreatedAt:   now,
	}

	err = s.AlertRecordRepo.CreateAlertRecord(ctx, record)
	if err != nil {
		s.logger.Error("创建证书告警记录失败", zap.Error(err))
		return
	}

	state.LastRecordID = record.ID
	if err := s.AlertStateRepo.SaveAlertState(ctx, state); err != nil {
		s.logger.Error("保存告警状态失败", zap.Error(err))
		return
	}

	// 发送通知
	go s.sendAlertNotification(record, agent)
}

// resolveCertAlert 恢复证书告警
func (s *AlertService) resolveCertAlert(ctx context.Context, config *models.AlertConfig, agent *models.Agent, monitor *protocol.MonitorData, certDaysLeft float64) {
	stateKey := fmt.Sprintf("%s:global:cert:%s", agent.ID, monitor.MonitorId)

	state, err := s.AlertStateRepo.GetAlertState(ctx, stateKey)
	if err != nil || !state.IsFiring {
		return
	}

	s.logger.Info("证书告警恢复",
		zap.String("agentId", agent.ID),
		zap.String("monitorId", monitor.MonitorId),
		zap.String("target", monitor.Target),
		zap.Float64("certDaysLeft", certDaysLeft),
	)

	if state.LastRecordID > 0 {
		existingRecord, err := s.AlertRecordRepo.GetAlertRecordByID(ctx, state.LastRecordID)
		if err != nil {
			s.logger.Error("获取证书告警记录失败", zap.Error(err))
		} else if existingRecord != nil && existingRecord.Status == "firing" {
			now := time.Now().UnixMilli()
			existingRecord.Status = "resolved"
			existingRecord.ResolvedValue = certDaysLeft
			existingRecord.ResolvedAt = now
			existingRecord.UpdatedAt = now

			err = s.AlertRecordRepo.UpdateAlertRecord(ctx, existingRecord)
			if err != nil {
				s.logger.Error("更新证书告警记录失败", zap.Error(err))
			} else {
				// 发送恢复通知
				go s.sendAlertNotification(existingRecord, agent)
			}
		}
	}

	state.IsFiring = false
	state.LastRecordID = 0
	if err := s.AlertStateRepo.SaveAlertState(ctx, state); err != nil {
		s.logger.Error("保存告警状态失败", zap.Error(err))
		return
	}
}

// calculateCertLevel 计算证书告警级别
func (s *AlertService) calculateCertLevel(daysLeft float64) string {
	if daysLeft <= 7 {
		return "critical"
	} else if daysLeft <= 30 {
		return "warning"
	} else {
		return "info"
	}
}

// checkServiceDownAlerts 检查服务下线告警
func (s *AlertService) checkServiceDownAlerts(ctx context.Context, config *models.AlertConfig, now int64) error {
	// 获取所有最新的监控指标
	monitors, err := s.monitorService.GetAllLatestMonitorMetrics(ctx)
	if err != nil {
		return err
	}

	// 收集所有 agentIds，批量查询探针信息
	agentIdSet := make(map[string]bool)
	for _, monitor := range monitors {
		if monitor.AgentId != "" {
			agentIdSet[monitor.AgentId] = true
		}
	}

	agentIds := make([]string, 0, len(agentIdSet))
	for id := range agentIdSet {
		agentIds = append(agentIds, id)
	}

	agentMap := make(map[string]*models.Agent)
	if len(agentIds) > 0 {
		agents, err := s.agentRepo.ListByIDs(ctx, agentIds)
		if err != nil {
			s.logger.Error("批量获取探针信息失败", zap.Error(err))
			return err
		}
		for i := range agents {
			agentMap[agents[i].ID] = &agents[i]
		}
	}

	for _, monitor := range monitors {
		// 从 map 中获取探针信息
		agent, exists := agentMap[monitor.AgentId]
		if !exists {
			s.logger.Error("探针信息不存在", zap.String("agentId", monitor.AgentId))
			continue
		}

		stateKey := fmt.Sprintf("%s:global:service:%s", agent.ID, monitor.MonitorId)

		var shouldFire, shouldResolve bool

		// 从数据库加载状态
		state, err := s.AlertStateRepo.GetAlertState(ctx, stateKey)
		if err != nil {
			// 状态不存在，创建新状态
			state = &models.AlertState{
				ID:        stateKey,
				AgentID:   agent.ID,
				AlertType: "service",
			}
		}
		state.AgentID = agent.ID
		state.AlertType = "service"
		state.Duration = config.Rules.ServiceDuration
		state.LastCheckTime = now

		if monitor.Status == "down" {
			if state.StartTime == 0 {
				state.StartTime = monitor.CheckedAt
			}

			elapsedSeconds := (now - state.StartTime) / 1000
			if elapsedSeconds >= int64(config.Rules.ServiceDuration) && !state.IsFiring {
				shouldFire = true
				state.IsFiring = true
			}
		} else {
			if state.IsFiring {
				shouldResolve = true
			}
			state.StartTime = 0
		}

		// 保存状态到数据库
		if err := s.AlertStateRepo.SaveAlertState(ctx, state); err != nil {
			s.logger.Error("保存告警状态失败", zap.Error(err))
			continue
		}

		if shouldFire {
			s.fireServiceDownAlert(ctx, config, agent, &monitor, state, now)
		}

		if shouldResolve {
			s.resolveServiceDownAlert(ctx, config, agent, &monitor, state)
		}
	}

	return nil
}

// fireServiceDownAlert 触发服务下线告警
func (s *AlertService) fireServiceDownAlert(ctx context.Context, config *models.AlertConfig, agent *models.Agent, monitor *protocol.MonitorData, state *models.AlertState, now int64) {
	s.logger.Info("触发服务下线告警",
		zap.String("agentId", agent.ID),
		zap.String("monitorId", monitor.MonitorId),
		zap.String("monitorName", monitor.MonitorName),
		zap.String("target", monitor.Target),
		zap.Int("duration", state.Duration),
	)

	// 构建告警消息，优先使用监控任务名称
	var message string
	if monitor.MonitorName != "" {
		message = fmt.Sprintf("监控项 %s (%s) 持续离线%d秒", monitor.MonitorName, monitor.Target, state.Duration)
	} else {
		message = fmt.Sprintf("监控项 %s 持续离线%d秒", monitor.Target, state.Duration)
	}

	// 创建告警记录
	record := &models.AlertRecord{
		AgentID:     agent.ID,
		AgentName:   agent.Name,
		AlertType:   "service",
		Message:     message,
		Threshold:   0,
		ActualValue: float64(state.Duration),
		Level:       "critical",
		Status:      "firing",
		FiredAt:     now,
		CreatedAt:   now,
	}

	err := s.AlertRecordRepo.CreateAlertRecord(ctx, record)
	if err != nil {
		s.logger.Error("创建服务下线告警记录失败", zap.Error(err))
		return
	}

	state.LastRecordID = record.ID
	if err := s.AlertStateRepo.SaveAlertState(ctx, state); err != nil {
		s.logger.Error("保存告警状态失败", zap.Error(err))
		return
	}

	// 发送通知
	go s.sendAlertNotification(record, agent)
}

// resolveServiceDownAlert 恢复服务下线告警
func (s *AlertService) resolveServiceDownAlert(ctx context.Context, config *models.AlertConfig, agent *models.Agent, monitor *protocol.MonitorData, state *models.AlertState) {
	s.logger.Info("服务下线告警恢复",
		zap.String("agentId", agent.ID),
		zap.String("monitorId", monitor.MonitorId),
		zap.String("target", monitor.Target),
	)

	if state.LastRecordID > 0 {
		existingRecord, err := s.AlertRecordRepo.GetAlertRecordByID(ctx, state.LastRecordID)
		if err != nil {
			s.logger.Error("获取服务下线告警记录失败", zap.Error(err))
		} else if existingRecord != nil && existingRecord.Status == "firing" {
			now := time.Now().UnixMilli()
			existingRecord.Status = "resolved"
			existingRecord.ResolvedValue = 0 // 服务已恢复在线
			existingRecord.ResolvedAt = now
			existingRecord.UpdatedAt = now

			err = s.AlertRecordRepo.UpdateAlertRecord(ctx, existingRecord)
			if err != nil {
				s.logger.Error("更新服务下线告警记录失败", zap.Error(err))
			} else {
				// 发送恢复通知
				go s.sendAlertNotification(existingRecord, agent)
			}
		}
	}

	state.IsFiring = false
	state.LastRecordID = 0
	if err := s.AlertStateRepo.SaveAlertState(ctx, state); err != nil {
		s.logger.Error("保存告警状态失败", zap.Error(err))
		return
	}
}

// checkAgentOfflineAlerts 检查探针离线告警
func (s *AlertService) checkAgentOfflineAlerts(ctx context.Context, config *models.AlertConfig, now int64) error {
	// 获取所有探针
	agents, err := s.agentRepo.FindAll(ctx)
	if err != nil {
		return err
	}

	for _, agent := range agents {
		stateKey := fmt.Sprintf("%s:global:agent_offline:%s", agent.ID, agent.ID)

		// 防止时钟回拨导致负数
		offlineSeconds := int64(0)
		if now > agent.LastSeenAt {
			offlineSeconds = (now - agent.LastSeenAt) / 1000
		}

		// 从数据库加载状态
		state, err := s.AlertStateRepo.GetAlertState(ctx, stateKey)
		if err != nil {
			// 状态不存在，创建新状态
			state = &models.AlertState{
				ID:        stateKey,
				AgentID:   agent.ID,
				AlertType: "agent_offline",
			}
		}

		state.AgentID = agent.ID
		state.AlertType = "agent_offline"
		state.Duration = config.Rules.AgentOfflineDuration
		state.Threshold = float64(config.Rules.AgentOfflineDuration)
		state.Value = float64(offlineSeconds)
		state.LastCheckTime = now

		var shouldFire, shouldResolve bool

		if offlineSeconds >= int64(config.Rules.AgentOfflineDuration) {
			if !state.IsFiring {
				shouldFire = true
				state.IsFiring = true
			}
		} else {
			if state.IsFiring {
				shouldResolve = true
			}
		}

		// 保存状态到数据库
		if err := s.AlertStateRepo.SaveAlertState(ctx, state); err != nil {
			s.logger.Error("保存告警状态失败", zap.Error(err))
			continue
		}

		if shouldFire {
			s.fireAgentOfflineAlert(ctx, config, &agent, state, offlineSeconds, now)
		}

		if shouldResolve {
			s.resolveAgentOfflineAlert(ctx, config, &agent, state)
		}
	}

	return nil
}

// fireAgentOfflineAlert 触发探针离线告警
func (s *AlertService) fireAgentOfflineAlert(ctx context.Context, config *models.AlertConfig, agent *models.Agent, state *models.AlertState, offlineSeconds int64, now int64) {
	s.logger.Info("触发探针离线告警",
		zap.String("agentId", agent.ID),
		zap.String("agentName", agent.Name),
		zap.Int64("offlineSeconds", offlineSeconds),
		zap.Int("threshold", state.Duration),
	)

	// 创建告警记录
	record := &models.AlertRecord{
		AgentID:     agent.ID,
		AgentName:   agent.Name,
		AlertType:   "agent_offline",
		Message:     fmt.Sprintf("探针 %s 已离线%d秒，超过阈值%d秒", agent.Name, offlineSeconds, state.Duration),
		Threshold:   float64(state.Duration),
		ActualValue: float64(offlineSeconds),
		Level:       "critical",
		Status:      "firing",
		FiredAt:     now,
		CreatedAt:   now,
	}

	err := s.AlertRecordRepo.CreateAlertRecord(ctx, record)
	if err != nil {
		s.logger.Error("创建探针离线告警记录失败", zap.Error(err))
		return
	}

	state.LastRecordID = record.ID
	if err := s.AlertStateRepo.SaveAlertState(ctx, state); err != nil {
		s.logger.Error("保存告警状态失败", zap.Error(err))
		return
	}

	// 发送通知
	go s.sendAlertNotification(record, agent)
}

// resolveAgentOfflineAlert 恢复探针离线告警
func (s *AlertService) resolveAgentOfflineAlert(ctx context.Context, config *models.AlertConfig, agent *models.Agent, state *models.AlertState) {
	s.logger.Info("探针离线告警恢复",
		zap.String("agentId", agent.ID),
		zap.String("agentName", agent.Name),
	)

	// 更新告警记录状态
	if state.LastRecordID > 0 {
		existingRecord, err := s.AlertRecordRepo.GetAlertRecordByID(ctx, state.LastRecordID)
		if err != nil {
			s.logger.Error("获取探针离线告警记录失败", zap.Error(err))
		} else if existingRecord != nil && existingRecord.Status == "firing" {
			now := time.Now().UnixMilli()
			existingRecord.Status = "resolved"
			existingRecord.ResolvedValue = 0 // 探针已恢复在线
			existingRecord.ResolvedAt = now
			existingRecord.UpdatedAt = now

			err = s.AlertRecordRepo.UpdateAlertRecord(ctx, existingRecord)
			if err != nil {
				s.logger.Error("更新探针离线告警记录失败", zap.Error(err))
			} else {
				// 发送恢复通知
				go s.sendAlertNotification(existingRecord, agent)
			}
		}
	}

	state.IsFiring = false
	state.LastRecordID = 0
	if err := s.AlertStateRepo.SaveAlertState(ctx, state); err != nil {
		s.logger.Error("保存告警状态失败", zap.Error(err))
	}
}
