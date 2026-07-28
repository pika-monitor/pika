package handler

import (
	"github.com/dushixiang/pika/internal/models"
	"github.com/dushixiang/pika/internal/service"
	"github.com/go-orz/orz"
	"github.com/labstack/echo/v5"
	"go.uber.org/zap"
)

type TamperHandler struct {
	logger        *zap.Logger
	tamperService *service.TamperService
}

func NewTamperHandler(logger *zap.Logger, tamperService *service.TamperService) *TamperHandler {
	return &TamperHandler{
		logger:        logger,
		tamperService: tamperService,
	}
}

// UpdateConfig 更新探针的防篡改配置
// POST /api/agents/:id/tamper/config
func (h *TamperHandler) UpdateConfig(c *echo.Context) error {
	agentID := c.Param("id")

	var req models.TamperProtectConfigData

	if err := c.Bind(&req); err != nil {
		return err
	}

	err := h.tamperService.UpdateConfig(c.Request().Context(), agentID, &req)
	if err != nil {
		h.logger.Error("更新防篡改配置失败", zap.Error(err), zap.String("agentId", agentID))
		return orz.NewError(500, "更新配置失败")
	}

	return orz.Ok(c, orz.Map{})
}

// GetConfig 获取探针的防篡改配置
// GET /api/agents/:id/tamper/config
func (h *TamperHandler) GetConfig(c *echo.Context) error {
	agentID := c.Param("id")

	config, err := h.tamperService.GetConfigByAgentID(c.Request().Context(), agentID)
	if err != nil {
		h.logger.Error("获取防篡改配置失败", zap.Error(err), zap.String("agentId", agentID))
		return orz.NewError(500, "获取配置失败")
	}

	return orz.Ok(c, config)
}

// ListEvents 获取探针的防篡改事件
// GET /api/agents/:id/tamper/events
func (h *TamperHandler) ListEvents(c *echo.Context) error {
	agentID := c.Param("id")

	// 获取分页参数
	pageReq := orz.GetPageRequest(c, "createdAt")
	builder := orz.NewPageBuilder(h.tamperService.TamperEventRepo.Repository).
		PageRequest(pageReq).
		Equal("agentId", agentID).
		Equal("path", c.QueryParam("path")).
		Equal("operation", c.QueryParam("operation")).
		Contains("details", c.QueryParam("details"))

	ctx := c.Request().Context()
	page, err := builder.Execute(ctx)
	if err != nil {
		return err
	}

	return orz.Ok(c, page)
}

func (h *TamperHandler) DeleteEvents(c *echo.Context) error {
	agentID := c.Param("id")
	err := h.tamperService.DeleteEventsByAgentID(c.Request().Context(), agentID)
	if err != nil {
		h.logger.Error("删除防篡改事件失败", zap.Error(err), zap.String("agentId", agentID))
		return orz.NewError(500, "删除事件失败")
	}
	return orz.Ok(c, orz.Map{})
}
