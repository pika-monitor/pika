package repo

import (
	"context"
	"fmt"

	"github.com/dushixiang/pika/internal/models"
	"github.com/go-orz/orz"
	"gorm.io/gorm"
)

type AgentRepo struct {
	orz.Repository[models.Agent, string]
	db *gorm.DB
}

func NewAgentRepo(db *gorm.DB) *AgentRepo {
	return &AgentRepo{
		Repository: orz.NewRepository[models.Agent, string](db),
		db:         db,
	}
}

// UpdateStatus 更新探针状态
func (r *AgentRepo) UpdateStatus(ctx context.Context, agentID string, status int, lastSeenAt int64) error {
	m := map[string]interface{}{
		"status": status,
	}
	if lastSeenAt > 0 {
		m["last_seen_at"] = lastSeenAt
	}

	return r.db.WithContext(ctx).
		Model(&models.Agent{}).
		Where("id = ?", agentID).
		Updates(m).Error
}

// FindOnlineAgents 查找所有在线探针
func (r *AgentRepo) FindOnlineAgents(ctx context.Context) ([]models.Agent, error) {
	var agents []models.Agent
	err := r.db.WithContext(ctx).
		Where("status = ?", 1).
		Find(&agents).Error
	return agents, err
}

// FindByIP 根据公网IP查找探针
func (r *AgentRepo) FindByIP(ctx context.Context, ip string) (*models.Agent, error) {
	var agent models.Agent
	err := r.db.WithContext(ctx).
		Where("ip = ? or ipv4 = ? OR ipv6 = ?", ip, ip, ip).
		First(&agent).Error
	if err != nil {
		return nil, err
	}
	return &agent, nil
}

// FindByHostname 根据主机名查找探针
func (r *AgentRepo) FindByHostname(ctx context.Context, hostname string) (*models.Agent, error) {
	var agent models.Agent
	err := r.db.WithContext(ctx).
		Where("hostname = ?", hostname).
		First(&agent).Error
	if err != nil {
		return nil, err
	}
	return &agent, nil
}

// FindByHostnameAndIP 根据主机名和公网IP查找探针
func (r *AgentRepo) FindByHostnameAndIP(ctx context.Context, hostname, ip string) (*models.Agent, error) {
	var agent models.Agent
	err := r.db.WithContext(ctx).
		Where("hostname = ? AND (ipv4 = ? OR ipv6 = ?)", hostname, ip, ip).
		First(&agent).Error
	if err != nil {
		return nil, err
	}
	return &agent, nil
}

// SaveAuditResult 保存审计结果
func (r *AgentRepo) SaveAuditResult(ctx context.Context, audit *models.AuditResult) error {
	return r.db.WithContext(ctx).Create(audit).Error
}

// GetLatestAuditResult 获取最新的审计结果
func (r *AgentRepo) GetLatestAuditResult(ctx context.Context, agentID string) (*models.AuditResult, error) {
	var audit models.AuditResult
	err := r.db.WithContext(ctx).
		Where("agent_id = ?", agentID).
		Order("created_at DESC").
		First(&audit).Error
	if err != nil {
		return nil, err
	}
	return &audit, nil
}

// GetLatestAuditResultByType 根据类型获取最新的审计结果
func (r *AgentRepo) GetLatestAuditResultByType(ctx context.Context, agentID string, resultType string) (*models.AuditResult, error) {
	var audit models.AuditResult
	err := r.db.WithContext(ctx).
		Where("agent_id = ? AND type = ?", agentID, resultType).
		Order("created_at DESC").
		First(&audit).Error
	if err != nil {
		return nil, err
	}
	return &audit, nil
}

// ListAuditResults 获取审计结果列表
func (r *AgentRepo) ListAuditResults(ctx context.Context, agentID string) ([]models.AuditResult, error) {
	var audits []models.AuditResult
	err := r.db.WithContext(ctx).
		Where("agent_id = ?", agentID).
		Order("created_at DESC").
		Limit(50).
		Find(&audits).Error
	return audits, err
}

// GetStatistics 获取探针统计数据
func (r *AgentRepo) GetStatistics(ctx context.Context) (total int64, online int64, err error) {
	// 获取总数
	err = r.db.WithContext(ctx).
		Model(&models.Agent{}).
		Count(&total).Error
	if err != nil {
		return 0, 0, err
	}

	// 获取在线数量
	err = r.db.WithContext(ctx).
		Model(&models.Agent{}).
		Where("status = ?", 1).
		Count(&online).Error

	return total, online, err
}

// ListByIDs 根据ID列表获取探针
func (r *AgentRepo) ListByIDs(ctx context.Context, ids []string) ([]models.Agent, error) {
	var agents []models.Agent
	if len(ids) == 0 {
		return agents, nil
	}
	err := r.db.WithContext(ctx).
		Where("id IN ?", ids).
		Find(&agents).Error
	return agents, err
}

// DeleteAuditResults 删除探针的所有审计结果
func (r *AgentRepo) DeleteAuditResults(ctx context.Context, agentID string) error {
	return r.db.WithContext(ctx).
		Where("agent_id = ?", agentID).
		Delete(&models.AuditResult{}).Error
}

func (r *AgentRepo) CreateCommandTask(ctx context.Context, task *models.CommandTask) error {
	return r.db.WithContext(ctx).Create(task).Error
}

func (r *AgentRepo) GetCommandTask(ctx context.Context, agentID, commandID string) (*models.CommandTask, error) {
	var task models.CommandTask
	err := r.db.WithContext(ctx).
		Where("agent_id = ? AND id = ?", agentID, commandID).
		First(&task).Error
	if err != nil {
		return nil, err
	}
	return &task, nil
}

func (r *AgentRepo) ListCommandTasks(ctx context.Context, agentID string, limit int) ([]models.CommandTask, error) {
	var tasks []models.CommandTask
	err := r.db.WithContext(ctx).
		Select("id", "agent_id", "command", "status", "error", "exit_code", "timeout_seconds", "truncated", "started_at", "finished_at", "created_at", "updated_at").
		Where("agent_id = ?", agentID).
		Order("created_at DESC").
		Limit(limit).
		Find(&tasks).Error
	return tasks, err
}

func (r *AgentRepo) UpdateCommandTask(ctx context.Context, commandID string, values map[string]interface{}) error {
	return r.db.WithContext(ctx).
		Model(&models.CommandTask{}).
		Where("id = ?", commandID).
		Updates(values).Error
}

func (r *AgentRepo) TransitionCommandTaskStatus(ctx context.Context, agentID, commandID string, from []string, to string) (bool, error) {
	result := r.db.WithContext(ctx).
		Model(&models.CommandTask{}).
		Where("agent_id = ? AND id = ? AND status IN ?", agentID, commandID, from).
		Update("status", to)
	return result.RowsAffected == 1, result.Error
}

func (r *AgentRepo) ExpireStaleCommandTasks(ctx context.Context, agentID string, now int64) error {
	return r.db.WithContext(ctx).
		Model(&models.CommandTask{}).
		Where("agent_id = ? AND status IN ? AND created_at + timeout_seconds * 1000 + 30000 < ?",
			agentID, []string{"pending", "running", "cancelling"}, now).
		Updates(map[string]interface{}{
			"status":      "error",
			"error":       "任务超过执行时限且未收到 Agent 最终状态",
			"finished_at": now,
		}).Error
}

func (r *AgentRepo) DeleteCommandTasks(ctx context.Context, agentID string) error {
	return r.db.WithContext(ctx).
		Where("agent_id = ?", agentID).
		Delete(&models.CommandTask{}).Error
}

// FindPublicAgents 查找所有公开可见的探针
func (r *AgentRepo) FindPublicAgents(ctx context.Context) ([]models.Agent, error) {
	var agents []models.Agent
	err := r.db.WithContext(ctx).
		Where("visibility = ?", "public").
		Find(&agents).Error
	return agents, err
}

// FindPublicAgentByID 查找指定ID的公开可见探针
func (r *AgentRepo) FindPublicAgentByID(ctx context.Context, id string) (*models.Agent, error) {
	var agent models.Agent
	err := r.db.WithContext(ctx).
		Where("id = ? AND visibility = ?", id, "public").
		First(&agent).Error
	if err != nil {
		return nil, err
	}
	return &agent, nil
}

// FindByShortID 通过ID前缀查找探针（短ID匹配）
func (r *AgentRepo) FindByShortID(ctx context.Context, shortID string) (models.Agent, error) {
	var agents []models.Agent
	err := r.db.WithContext(ctx).
		Where("id LIKE ?", shortID+"-%").
		Limit(2).
		Find(&agents).Error
	if err != nil {
		return models.Agent{}, err
	}
	if len(agents) == 0 {
		return models.Agent{}, gorm.ErrRecordNotFound
	}
	if len(agents) > 1 {
		return models.Agent{}, fmt.Errorf("短ID匹配到多个探针，请使用完整ID")
	}
	return agents[0], nil
}

// FindPublicAgentByShortID 通过ID前缀查找公开可见探针（短ID匹配）
func (r *AgentRepo) FindPublicAgentByShortID(ctx context.Context, shortID string) (*models.Agent, error) {
	var agents []models.Agent
	err := r.db.WithContext(ctx).
		Where("id LIKE ? AND visibility = ?", shortID+"-%", "public").
		Limit(2).
		Find(&agents).Error
	if err != nil {
		return nil, err
	}
	if len(agents) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	if len(agents) > 1 {
		return nil, fmt.Errorf("短ID匹配到多个探针，请使用完整ID")
	}
	return &agents[0], nil
}

// GetAllTags 获取所有探针的标签（去重）
func (r *AgentRepo) GetAllTags(ctx context.Context) ([]string, error) {
	var agents []models.Agent
	err := r.db.WithContext(ctx).
		Select("tags").
		Find(&agents).Error
	if err != nil {
		return nil, err
	}

	// 使用map去重
	tagMap := make(map[string]bool)
	for _, agent := range agents {
		if agent.Tags != nil {
			for _, tag := range agent.Tags {
				if tag != "" {
					tagMap[tag] = true
				}
			}
		}
	}

	// 转换为切片
	tags := make([]string, 0, len(tagMap))
	for tag := range tagMap {
		tags = append(tags, tag)
	}

	return tags, nil
}

// FindAgentsWithTrafficReset 查询配置了流量自动重置的探针
func (r *AgentRepo) FindAgentsWithTrafficReset(ctx context.Context) ([]models.Agent, error) {
	var allAgents []models.Agent
	err := r.db.WithContext(ctx).Find(&allAgents).Error
	if err != nil {
		return nil, err
	}

	// 在应用层过滤配置了流量重置的探针
	var agents []models.Agent
	for _, agent := range allAgents {
		stats := agent.TrafficStats.Data()
		if stats.ResetDay > 0 {
			agents = append(agents, agent)
		}
	}
	return agents, nil
}
