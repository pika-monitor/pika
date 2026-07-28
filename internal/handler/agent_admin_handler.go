package handler

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dushixiang/pika/internal/models"
	"github.com/dushixiang/pika/internal/protocol"
	"github.com/go-orz/orz"
	"github.com/labstack/echo/v5"
	"go.uber.org/zap"
)

func SortAgents(agents []models.Agent) {
	// 复用排序逻辑
	slices.SortFunc(agents, func(a, b models.Agent) int {
		// 先按照状态排序
		if a.Status != b.Status {
			return b.Status - a.Status
		}
		// 再按权重排序（数字越大越靠前）
		if a.Weight != b.Weight {
			return b.Weight - a.Weight
		}
		// 权重相同时按名称排序
		return strings.Compare(a.Name, b.Name)
	})
}

// Paging 探针分页查询（返回完整列表，前端实现过滤）
func (h *AgentHandler) Paging(c *echo.Context) error {
	ctx := c.Request().Context()

	// 获取所有探针（管理员接口）
	agents, err := h.agentService.AgentRepo.FindAll(ctx)
	if err != nil {
		return err
	}

	// 排序
	SortAgents(agents)

	return orz.Ok(c, agents)
}

// GetForAdmin 获取探针详情（管理员接口，显示完整信息）
func (h *AgentHandler) GetForAdmin(c *echo.Context) error {
	id := c.Param("id")
	ctx := c.Request().Context()

	agent, err := h.agentService.GetAgent(ctx, id)
	if err != nil {
		return err
	}

	return orz.Ok(c, agent)
}

// GetAdminLatestMetrics 获取探针最新指标（管理员接口，显示完整信息）
func (h *AgentHandler) GetAdminLatestMetrics(c *echo.Context) error {
	id := c.Param("id")

	metrics, ok := h.metricService.GetLatestMetrics(id)
	if !ok {
		return orz.NewError(404, "探针最新指标不存在")
	}
	return orz.Ok(c, metrics)
}

// SendCommand 向探针发送指令
func (h *AgentHandler) SendCommand(c *echo.Context) error {
	agentID := c.Param("id")
	cmdType := c.QueryParam("type")

	if cmdType == "" {
		return orz.NewError(400, "指令类型不能为空")
	}

	// 检查agent是否在线
	_, exists := h.wsManager.GetClient(agentID)
	if !exists {
		return orz.NewError(400, "探针未连接")
	}

	// 生成指令ID
	cmdID := fmt.Sprintf("%s_%d", cmdType, time.Now().UnixMilli())

	// 构建指令请求
	cmdReq := protocol.CommandRequest{
		ID:   cmdID,
		Type: cmdType,
	}

	msgData, err := json.Marshal(protocol.OutboundMessage{
		Type: protocol.MessageTypeCommand,
		Data: cmdReq,
	})
	if err != nil {
		return err
	}

	// 发送指令
	if err := h.wsManager.SendToClient(agentID, msgData); err != nil {
		return orz.NewError(500, "发送指令失败")
	}

	h.logger.Info("command sent", zap.String("agentID", agentID), zap.String("cmdID", cmdID), zap.String("type", cmdType))

	return orz.Ok(c, orz.Map{
		"commandId": cmdID,
		"status":    "sent",
	})
}

// GetAuditResult 获取审计结果(原始数据)
func (h *AgentHandler) GetAuditResult(c *echo.Context) error {
	agentID := c.Param("id")
	ctx := c.Request().Context()

	result, err := h.agentService.GetAuditResult(ctx, agentID)
	if err != nil {
		return err
	}

	return orz.Ok(c, result)
}

// ListAuditResults 获取审计结果列表
func (h *AgentHandler) ListAuditResults(c *echo.Context) error {
	agentID := c.Param("id")
	ctx := c.Request().Context()

	results, err := h.agentService.ListAuditResults(ctx, agentID)
	if err != nil {
		return err
	}

	return orz.Ok(c, orz.Map{
		"items": results,
		"total": len(results),
	})
}

// UpdateInfo 更新探针信息（名称、标签、到期时间、可见性、权重、备注）
func (h *AgentHandler) UpdateInfo(c *echo.Context) error {
	agentID := c.Param("id")

	var req struct {
		Name       string   `json:"name"`
		Tags       []string `json:"tags"`
		ExpireTime int64    `json:"expireTime"`
		Visibility string   `json:"visibility"`
		Weight     int      `json:"weight"`
		Remark     string   `json:"remark"`
	}
	if err := c.Bind(&req); err != nil {
		return orz.NewError(400, "请求参数错误")
	}

	ctx := c.Request().Context()
	agent, err := h.agentService.AgentRepo.FindById(ctx, agentID)
	if err != nil {
		return err
	}
	// 更新字段
	agent.Name = req.Name
	agent.Tags = req.Tags
	agent.ExpireTime = req.ExpireTime
	agent.Visibility = req.Visibility
	agent.Weight = req.Weight
	agent.Remark = req.Remark
	agent.UpdatedAt = time.Now().UnixMilli()

	if err := h.agentService.AgentRepo.Save(ctx, &agent); err != nil {
		return err
	}

	return orz.Ok(c, orz.Map{})
}

// GetStatistics 获取探针统计数据
func (h *AgentHandler) GetStatistics(c *echo.Context) error {
	ctx := c.Request().Context()
	stats, err := h.agentService.GetStatistics(ctx)
	if err != nil {
		return err
	}

	return orz.Ok(c, stats)
}

// Delete 删除探针
func (h *AgentHandler) Delete(c *echo.Context) error {
	agentID := c.Param("id")
	ctx := c.Request().Context()

	// 检查探针是否存在
	agent, err := h.agentService.GetAgent(ctx, agentID)
	if err != nil {
		return err
	}

	// 如果探针在线，先发送卸载指令，然后断开连接
	if client, exists := h.wsManager.GetClient(agentID); exists {
		// 构建卸载消息
		uninstallMsg, err := json.Marshal(protocol.OutboundMessage{
			Type: protocol.MessageTypeUninstall,
			Data: struct{}{}, // 空数据，卸载指令不需要额外参数
		})
		if err == nil {
			// 发送卸载指令（忽略发送错误，继续删除流程）
			if err := h.wsManager.SendToClient(agentID, uninstallMsg); err != nil {
				h.logger.Warn("发送卸载指令失败",
					zap.String("agentID", agentID),
					zap.Error(err))
			} else {
				h.logger.Info("已向探针发送卸载指令",
					zap.String("agentID", agentID),
					zap.String("name", agent.Name))
				// 等待一小段时间让探针处理卸载消息
				time.Sleep(500 * time.Millisecond)
			}
		}

		// 断开连接
		client.Conn.Close()
	}

	// 删除探针及其所有相关数据
	if err := h.agentService.DeleteAgent(ctx, agentID); err != nil {
		h.logger.Error("删除探针失败",
			zap.String("agentID", agentID),
			zap.String("name", agent.Name),
			zap.Error(err))
		return err
	}

	h.logger.Info("探针已删除",
		zap.String("agentID", agentID),
		zap.String("name", agent.Name))

	return orz.Ok(c, orz.Map{})
}

// CleanupOrphanedAgentMetrics 手动清除所有已删除探针残留的指标数据
func (h *AgentHandler) CleanupOrphanedAgentMetrics(c *echo.Context) error {
	ctx := c.Request().Context()

	h.logger.Info("开始手动清理残留探针指标数据")

	if err := h.metricService.CleanOrphanedAgentMetrics(ctx); err != nil {
		h.logger.Error("清除残留探针指标数据失败", zap.Error(err))
		return orz.NewError(500, "清除残留探针指标数据失败: "+err.Error())
	}

	h.logger.Info("完成残留探针指标数据清理")

	return orz.Ok(c, orz.Map{
		"message": "已成功清理残留探针指标数据",
	})
}

// BatchUpdateTags 批量更新探针标签
func (h *AgentHandler) BatchUpdateTags(c *echo.Context) error {
	var req struct {
		AgentIDs  []string `json:"agentIds"`
		Tags      []string `json:"tags"`
		Operation string   `json:"operation"` // "add", "remove", "replace"
	}

	if err := c.Bind(&req); err != nil {
		return orz.NewError(400, "请求参数错误")
	}

	if len(req.AgentIDs) == 0 {
		return orz.NewError(400, "探针ID列表不能为空")
	}

	// 验证操作类型
	validOperations := map[string]bool{"add": true, "remove": true, "replace": true}
	if req.Operation == "" {
		req.Operation = "replace"
	}
	if !validOperations[req.Operation] {
		return orz.NewError(400, "不支持的操作类型")
	}

	ctx := c.Request().Context()
	if err := h.agentService.BatchUpdateTags(ctx, req.AgentIDs, req.Tags, req.Operation); err != nil {
		return err
	}

	return orz.Ok(c, orz.Map{
		"message": "批量更新标签成功",
		"count":   len(req.AgentIDs),
	})
}

// BatchUpdateVisibility 批量更新探针可见性
func (h *AgentHandler) BatchUpdateVisibility(c *echo.Context) error {
	var req struct {
		AgentIDs   []string `json:"agentIds"`
		Visibility string   `json:"visibility"`
	}

	if err := c.Bind(&req); err != nil {
		return orz.NewError(400, "请求参数错误")
	}

	if len(req.AgentIDs) == 0 {
		return orz.NewError(400, "探针ID列表不能为空")
	}

	// 验证可见性参数
	if req.Visibility != "public" && req.Visibility != "private" {
		return orz.NewError(400, "可见性参数错误，必须是 public 或 private")
	}

	ctx := c.Request().Context()
	if err := h.agentService.BatchUpdateVisibility(ctx, req.AgentIDs, req.Visibility); err != nil {
		return err
	}

	return orz.Ok(c, orz.Map{
		"message": "批量修改可见性成功",
		"count":   len(req.AgentIDs),
	})
}

// UpdateTrafficConfig 更新流量配置(管理员)
func (h *AgentHandler) UpdateTrafficConfig(c *echo.Context) error {
	agentID := c.Param("id")

	var req struct {
		Enabled  bool   `json:"enabled"`
		Type     string `json:"type"` // 流量类型: recv/send/both
		Limit    uint64 `json:"limit"`
		ResetDay int    `json:"resetDay"`
	}

	if err := c.Bind(&req); err != nil {
		return orz.NewError(400, "请求参数错误")
	}

	ctx := c.Request().Context()
	if err := h.trafficService.UpdateTrafficConfig(ctx, agentID, req.Enabled, req.Type, req.Limit, req.ResetDay); err != nil {
		return err
	}

	return orz.Ok(c, orz.Map{})
}

// GetTrafficStats 查询流量统计(管理员接口)
func (h *AgentHandler) GetTrafficStats(c *echo.Context) error {
	agentID := c.Param("id")
	ctx := c.Request().Context()

	stats, err := h.trafficService.GetTrafficStats(ctx, agentID)
	if err != nil {
		return err
	}

	return orz.Ok(c, stats)
}

// ResetAgentTraffic 手动重置流量(管理员)
func (h *AgentHandler) ResetAgentTraffic(c *echo.Context) error {
	agentID := c.Param("id")
	ctx := c.Request().Context()

	if err := h.trafficService.ResetAgentTraffic(ctx, agentID); err != nil {
		return err
	}

	return orz.Ok(c, orz.Map{})
}
