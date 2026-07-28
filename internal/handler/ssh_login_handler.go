package handler

import (
	"github.com/dushixiang/pika/internal/models"
	"github.com/dushixiang/pika/internal/service"
	"github.com/go-orz/orz"
	"github.com/labstack/echo/v5"
	"go.uber.org/zap"
)

// SSHLoginHandler SSH登录处理器
type SSHLoginHandler struct {
	logger  *zap.Logger
	service *service.SSHLoginService
}

// NewSSHLoginHandler 创建处理器
func NewSSHLoginHandler(logger *zap.Logger, service *service.SSHLoginService) *SSHLoginHandler {
	return &SSHLoginHandler{
		logger:  logger,
		service: service,
	}
}

// GetConfig 获取SSH登录监控配置
// GET /api/agents/:id/ssh-login/config
func (h *SSHLoginHandler) GetConfig(c *echo.Context) error {
	agentID := c.Param("id")
	if agentID == "" {
		return orz.NewError(400, "探针ID不能为空")
	}

	config, err := h.service.GetConfig(c.Request().Context(), agentID)
	if err != nil {
		h.logger.Error("获取SSH登录监控配置失败", zap.Error(err))
		return orz.NewError(500, "获取配置失败")
	}

	return orz.Ok(c, config)
}

// UpdateConfig 更新SSH登录监控配置
// POST /api/agents/:id/ssh-login/config
func (h *SSHLoginHandler) UpdateConfig(c *echo.Context) error {
	agentID := c.Param("id")

	var req models.SSHLoginConfigData
	if err := c.Bind(&req); err != nil {
		return err
	}

	err := h.service.UpdateConfig(c.Request().Context(), agentID, &req)
	if err != nil {
		h.logger.Error("更新SSH登录监控配置失败", zap.Error(err))
		return orz.NewError(500, "更新配置失败")
	}

	return orz.Ok(c, orz.Map{})
}

// ListEvents 查询SSH登录事件
// GET /api/agents/:id/ssh-login/events
func (h *SSHLoginHandler) ListEvents(c *echo.Context) error {
	agentID := c.Param("id")

	// 获取分页参数
	pageReq := orz.GetPageRequest(c, "createdAt")
	builder := orz.NewPageBuilder(h.service.SSHLoginEventRepo.Repository).
		PageRequest(pageReq).
		Equal("agentId", agentID).
		Equal("username", c.QueryParam("username")).
		Equal("ip", c.QueryParam("ip")).
		Equal("status", c.QueryParam("status"))

	ctx := c.Request().Context()
	page, err := builder.Execute(ctx)
	if err != nil {
		return err
	}

	return orz.Ok(c, page)
}

// GetEvent 获取单个SSH登录事件
// GET /api/ssh-login/events/:id
func (h *SSHLoginHandler) GetEvent(c *echo.Context) error {
	eventID := c.Param("id")
	if eventID == "" {
		return orz.NewError(400, "事件ID不能为空")
	}

	ctx := c.Request().Context()
	event, exists, err := h.service.SSHLoginEventRepo.FindByIdExists(ctx, eventID)
	if err != nil {
		h.logger.Error("获取SSH登录事件失败", zap.Error(err))
		return orz.NewError(500, "获取事件失败")
	}

	if !exists {
		return orz.NewError(404, "事件不存在")
	}

	return orz.Ok(c, event)
}

// DeleteEvents 删除探针的所有SSH登录事件
// DELETE /api/agents/:id/ssh-login/events
func (h *SSHLoginHandler) DeleteEvents(c *echo.Context) error {
	agentID := c.Param("id")

	ctx := c.Request().Context()
	if err := h.service.DeleteEventsByAgentID(ctx, agentID); err != nil {
		h.logger.Error("删除SSH登录事件失败", zap.Error(err))
		return orz.NewError(500, "删除失败")
	}

	return orz.Ok(c, orz.Map{})
}
