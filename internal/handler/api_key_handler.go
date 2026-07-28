package handler

import (
	"net/http"

	"github.com/dushixiang/pika/internal/service"
	"github.com/go-orz/orz"
	"github.com/labstack/echo/v5"
	"go.uber.org/zap"
)

type ApiKeyHandler struct {
	logger        *zap.Logger
	apiKeyService *service.ApiKeyService
}

func NewApiKeyHandler(logger *zap.Logger, apiKeyService *service.ApiKeyService) *ApiKeyHandler {
	return &ApiKeyHandler{
		logger:        logger,
		apiKeyService: apiKeyService,
	}
}

// GenerateApiKeyRequest 生成API密钥请求
type GenerateApiKeyRequest struct {
	Name string `json:"name" validate:"required"`
	Type string `json:"type"`
}

// UpdateApiKeyNameRequest 更新API密钥名称请求
type UpdateApiKeyNameRequest struct {
	Name string `json:"name" validate:"required"`
}

// Paging API密钥分页查询
func (r ApiKeyHandler) Paging(c *echo.Context) error {
	name := c.QueryParam("name")
	apiKeyType := c.QueryParam("type")

	pr := orz.GetPageRequest(c, "created_at", "name")

	builder := orz.NewPageBuilder(r.apiKeyService.ApiKeyRepo).
		PageRequest(pr).
		Contains("name", name)

	if apiKeyType != "" {
		builder = builder.Equal("type", apiKeyType)
	}

	ctx := c.Request().Context()
	page, err := builder.Execute(ctx)
	if err != nil {
		return err
	}

	// 遮蔽密钥值，完整密钥仅在创建时返回一次
	for i := range page.Items {
		page.Items[i].Key = maskApiKey(page.Items[i].Key)
	}

	return orz.Ok(c, page)
}

// Create 生成API密钥（仅限通信密钥 type=agent）
func (r ApiKeyHandler) Create(c *echo.Context) error {
	// API Key 不能创建其他 API Key
	if c.Get("authType") == "api_key" {
		return echo.NewHTTPError(http.StatusForbidden, "API Key 不能创建其他 API Key")
	}

	var req GenerateApiKeyRequest
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Validate(&req); err != nil {
		return err
	}

	// 从上下文获取用户ID
	userID := c.Get("userID").(string)

	// 固定类型为 agent（通信密钥）
	apiKeyType := "agent"

	ctx := c.Request().Context()
	apiKey, err := r.apiKeyService.GenerateApiKey(ctx, req.Name, userID, apiKeyType)
	if err != nil {
		r.logger.Error("failed to generate api key", zap.Error(err))
		return err
	}

	return orz.Ok(c, apiKey)
}

// CreateAdmin 生成管理 API Key（type=admin，完整 key 仅创建时返回一次）
func (r ApiKeyHandler) CreateAdmin(c *echo.Context) error {
	// API Key 不能创建其他 API Key
	if c.Get("authType") == "api_key" {
		return echo.NewHTTPError(http.StatusForbidden, "API Key 不能创建其他 API Key")
	}

	var req GenerateApiKeyRequest
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Validate(&req); err != nil {
		return err
	}

	userID := c.Get("userID").(string)

	ctx := c.Request().Context()
	apiKey, err := r.apiKeyService.GenerateApiKey(ctx, req.Name, userID, "admin")
	if err != nil {
		r.logger.Error("failed to generate admin api key", zap.Error(err))
		return err
	}

	return orz.Ok(c, apiKey)
}

// Get 获取API密钥详情
func (r ApiKeyHandler) Get(c *echo.Context) error {
	// API Key 不能查看其他 API Key 的完整值
	if c.Get("authType") == "api_key" {
		return echo.NewHTTPError(http.StatusForbidden, "API Key 不能查看其他 API Key")
	}

	id := c.Param("id")
	ctx := c.Request().Context()

	apiKey, err := r.apiKeyService.GetApiKey(ctx, id)
	if err != nil {
		r.logger.Error("failed to get api key", zap.Error(err))
		return err
	}

	// 遮蔽密钥值
	apiKey.Key = maskApiKey(apiKey.Key)
	return orz.Ok(c, apiKey)
}

// GetRaw 获取API密钥完整值（仅在生成安装命令等场景使用）
func (r ApiKeyHandler) GetRaw(c *echo.Context) error {
	// 仅 JWT 认证可用
	if c.Get("authType") == "api_key" {
		return echo.NewHTTPError(http.StatusForbidden, "API Key 不能查看其他 API Key")
	}

	id := c.Param("id")
	ctx := c.Request().Context()

	apiKey, err := r.apiKeyService.GetApiKey(ctx, id)
	if err != nil {
		r.logger.Error("failed to get api key", zap.Error(err))
		return err
	}

	// admin 类型密钥永远不可查看
	if apiKey.Type == "admin" {
		return echo.NewHTTPError(http.StatusForbidden, "管理 API Key 不允许查看完整值")
	}

	return orz.Ok(c, orz.Map{
		"key": apiKey.Key,
	})
}

// Update 更新API密钥名称
func (r ApiKeyHandler) Update(c *echo.Context) error {
	// API Key 不能修改其他 API Key
	if c.Get("authType") == "api_key" {
		return echo.NewHTTPError(http.StatusForbidden, "API Key 不能修改其他 API Key")
	}

	id := c.Param("id")

	var req UpdateApiKeyNameRequest
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Validate(&req); err != nil {
		return err
	}

	ctx := c.Request().Context()
	if err := r.apiKeyService.UpdateApiKeyName(ctx, id, req.Name); err != nil {
		r.logger.Error("failed to update api key name", zap.Error(err))
		return err
	}

	return orz.Ok(c, orz.Map{})
}

// Delete 删除API密钥
func (r ApiKeyHandler) Delete(c *echo.Context) error {
	// API Key 不能删除其他 API Key
	if c.Get("authType") == "api_key" {
		return echo.NewHTTPError(http.StatusForbidden, "API Key 不能删除其他 API Key")
	}

	id := c.Param("id")
	ctx := c.Request().Context()

	if err := r.apiKeyService.DeleteApiKey(ctx, id); err != nil {
		r.logger.Error("failed to delete api key", zap.Error(err))
		return err
	}

	return orz.Ok(c, orz.Map{})
}

// Enable 启用API密钥
func (r ApiKeyHandler) Enable(c *echo.Context) error {
	// API Key 不能启用/禁用其他 API Key
	if c.Get("authType") == "api_key" {
		return echo.NewHTTPError(http.StatusForbidden, "API Key 不能启用/禁用其他 API Key")
	}

	id := c.Param("id")
	ctx := c.Request().Context()

	if err := r.apiKeyService.EnableApiKey(ctx, id); err != nil {
		r.logger.Error("failed to enable api key", zap.Error(err))
		return err
	}

	return orz.Ok(c, orz.Map{})
}

// Disable 禁用API密钥
func (r ApiKeyHandler) Disable(c *echo.Context) error {
	// API Key 不能启用/禁用其他 API Key
	if c.Get("authType") == "api_key" {
		return echo.NewHTTPError(http.StatusForbidden, "API Key 不能启用/禁用其他 API Key")
	}

	id := c.Param("id")
	ctx := c.Request().Context()

	if err := r.apiKeyService.DisableApiKey(ctx, id); err != nil {
		r.logger.Error("failed to disable api key", zap.Error(err))
		return err
	}

	return orz.Ok(c, orz.Map{})
}

// maskApiKey 遮蔽API密钥，只显示前4位和后4位
func maskApiKey(key string) string {
	if len(key) <= 12 {
		return "****"
	}
	return key[:4] + "..." + key[len(key)-4:]
}
