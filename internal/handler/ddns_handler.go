package handler

import (
	"time"

	"github.com/dushixiang/pika/internal/models"
	"github.com/dushixiang/pika/internal/service"
	"github.com/go-orz/orz"
	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"go.uber.org/zap"
)

type DDNSHandler struct {
	logger      *zap.Logger
	ddnsService *service.DDNSService
}

func NewDDNSHandler(logger *zap.Logger, ddnsService *service.DDNSService) *DDNSHandler {
	return &DDNSHandler{
		logger:      logger,
		ddnsService: ddnsService,
	}
}

// CreateConfigRequest 创建 DDNS 配置请求
type CreateConfigRequest struct {
	AgentID       string   `json:"agentId" validate:"required"`
	Name          string   `json:"name" validate:"required"`
	Provider      string   `json:"provider" validate:"required"`
	DomainsIPv4   []string `json:"domainsIpv4"`
	DomainsIPv6   []string `json:"domainsIpv6"`
	EnableIPv4    bool     `json:"enableIpv4"`
	EnableIPv6    bool     `json:"enableIpv6"`
	IPv4GetMethod string   `json:"ipv4GetMethod"`
	IPv6GetMethod string   `json:"ipv6GetMethod"`
	IPv4GetValue  string   `json:"ipv4GetValue"`
	IPv6GetValue  string   `json:"ipv6GetValue"`
}

// UpdateConfigRequest 更新 DDNS 配置请求
type UpdateConfigRequest struct {
	Name          string   `json:"name"`
	Provider      string   `json:"provider"`
	DomainsIPv4   []string `json:"domainsIpv4"`
	DomainsIPv6   []string `json:"domainsIpv6"`
	EnableIPv4    bool     `json:"enableIpv4"`
	EnableIPv6    bool     `json:"enableIpv6"`
	IPv4GetMethod string   `json:"ipv4GetMethod"`
	IPv6GetMethod string   `json:"ipv6GetMethod"`
	IPv4GetValue  string   `json:"ipv4GetValue"`
	IPv6GetValue  string   `json:"ipv6GetValue"`
}

// Paging DDNS 配置分页查询
func (h *DDNSHandler) Paging(c *echo.Context) error {
	agentID := c.QueryParam("agentId")
	name := c.QueryParam("name")

	pr := orz.GetPageRequest(c, "created_at", "name")

	builder := orz.NewPageBuilder(h.ddnsService.ConfigRepo).
		PageRequest(pr).
		Contains("name", name)

	if agentID != "" {
		builder = builder.Equal("agent_id", agentID)
	}

	ctx := c.Request().Context()
	page, err := builder.Execute(ctx)
	if err != nil {
		return err
	}

	return orz.Ok(c, orz.Map{
		"items": page.Items,
		"total": page.Total,
	})
}

// Create 创建 DDNS 配置
func (h *DDNSHandler) Create(c *echo.Context) error {
	var req CreateConfigRequest
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Validate(&req); err != nil {
		return err
	}

	// 支持的服务商：aliyun, tencentcloud, cloudflare, huaweicloud
	validProviders := map[string]bool{
		"aliyun":       true,
		"tencentcloud": true,
		"cloudflare":   true,
		"huaweicloud":  true,
	}
	if !validProviders[req.Provider] {
		return orz.NewError(400, "不支持的 DNS 服务商")
	}

	// 验证 IP 获取配置
	if req.EnableIPv4 && req.IPv4GetMethod == "" {
		return orz.NewError(400, "IPv4 获取方式不能为空")
	}
	if req.EnableIPv6 && req.IPv6GetMethod == "" {
		return orz.NewError(400, "IPv6 获取方式不能为空")
	}
	// 验证 IP 获取方式只能是 api 或 interface
	validMethods := map[string]bool{"api": true, "interface": true}
	if req.EnableIPv4 && !validMethods[req.IPv4GetMethod] {
		return orz.NewError(400, "IPv4 获取方式只能是 api 或 interface")
	}
	if req.EnableIPv6 && !validMethods[req.IPv6GetMethod] {
		return orz.NewError(400, "IPv6 获取方式只能是 api 或 interface")
	}

	config := &models.DDNSConfig{
		ID:            uuid.New().String(),
		AgentID:       req.AgentID,
		Name:          req.Name,
		Enabled:       true, // 默认启用
		Provider:      req.Provider,
		DomainsIPv4:   req.DomainsIPv4,
		DomainsIPv6:   req.DomainsIPv6,
		EnableIPv4:    req.EnableIPv4,
		EnableIPv6:    req.EnableIPv6,
		IPv4GetMethod: req.IPv4GetMethod,
		IPv6GetMethod: req.IPv6GetMethod,
		IPv4GetValue:  req.IPv4GetValue,
		IPv6GetValue:  req.IPv6GetValue,
		CreatedAt:     time.Now().UnixMilli(),
		UpdatedAt:     time.Now().UnixMilli(),
	}

	ctx := c.Request().Context()
	if err := h.ddnsService.CreateConfig(ctx, config); err != nil {
		h.logger.Error("failed to create ddns config", zap.Error(err))
		return err
	}

	return orz.Ok(c, config)
}

// Get 获取 DDNS 配置详情
func (h *DDNSHandler) Get(c *echo.Context) error {
	id := c.Param("id")
	ctx := c.Request().Context()

	config, err := h.ddnsService.GetConfig(ctx, id)
	if err != nil {
		h.logger.Error("failed to get ddns config", zap.Error(err))
		return err
	}

	return orz.Ok(c, config)
}

// Update 更新 DDNS 配置
func (h *DDNSHandler) Update(c *echo.Context) error {
	id := c.Param("id")

	var req UpdateConfigRequest
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Validate(&req); err != nil {
		return err
	}

	ctx := c.Request().Context()

	// 检查配置是否存在
	existing, err := h.ddnsService.GetConfig(ctx, id)
	if err != nil {
		h.logger.Error("failed to get ddns config", zap.Error(err))
		return err
	}

	// 更新字段
	existing.Name = req.Name
	existing.Provider = req.Provider
	existing.DomainsIPv4 = req.DomainsIPv4
	existing.DomainsIPv6 = req.DomainsIPv6
	existing.EnableIPv4 = req.EnableIPv4
	existing.EnableIPv6 = req.EnableIPv6
	existing.IPv4GetMethod = req.IPv4GetMethod
	existing.IPv6GetMethod = req.IPv6GetMethod
	existing.IPv4GetValue = req.IPv4GetValue
	existing.IPv6GetValue = req.IPv6GetValue
	existing.UpdatedAt = time.Now().UnixMilli()

	if err := h.ddnsService.UpdateConfig(ctx, existing); err != nil {
		h.logger.Error("failed to update ddns config", zap.Error(err))
		return err
	}

	return orz.Ok(c, orz.Map{})
}

// Delete 删除 DDNS 配置
func (h *DDNSHandler) Delete(c *echo.Context) error {
	id := c.Param("id")
	ctx := c.Request().Context()

	if err := h.ddnsService.DeleteConfig(ctx, id); err != nil {
		h.logger.Error("failed to delete ddns config", zap.Error(err))
		return err
	}

	return orz.Ok(c, orz.Map{})
}

// Enable 启用 DDNS 配置
func (h *DDNSHandler) Enable(c *echo.Context) error {
	id := c.Param("id")
	ctx := c.Request().Context()

	if err := h.ddnsService.UpdateEnabled(ctx, id, true); err != nil {
		h.logger.Error("failed to enable ddns config", zap.Error(err))
		return err
	}

	return orz.Ok(c, orz.Map{})
}

// Disable 禁用 DDNS 配置
func (h *DDNSHandler) Disable(c *echo.Context) error {
	id := c.Param("id")
	ctx := c.Request().Context()

	if err := h.ddnsService.UpdateEnabled(ctx, id, false); err != nil {
		h.logger.Error("failed to disable ddns config", zap.Error(err))
		return err
	}

	return orz.Ok(c, orz.Map{})
}

// GetRecords 获取 DDNS 更新记录
func (h *DDNSHandler) GetRecords(c *echo.Context) error {
	id := c.Param("id")
	ctx := c.Request().Context()

	limit := 100 // 默认返回最近 100 条
	records, err := h.ddnsService.ListRecords(ctx, id, limit)
	if err != nil {
		h.logger.Error("failed to get ddns records", zap.Error(err))
		return err
	}

	return orz.Ok(c, orz.Map{
		"items": records,
		"total": len(records),
	})
}

// TriggerUpdate 手动触发 DDNS 更新
func (h *DDNSHandler) TriggerUpdate(c *echo.Context) error {
	id := c.Param("id")
	ctx := c.Request().Context()

	if err := h.ddnsService.TriggerUpdate(ctx, id); err != nil {
		h.logger.Error("failed to trigger ddns update", zap.Error(err))
		return err
	}

	return orz.Ok(c, orz.Map{})
}
