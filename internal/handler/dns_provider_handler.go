package handler

import (
	"net/http"
	"strings"

	"github.com/dushixiang/pika/internal/models"
	"github.com/dushixiang/pika/internal/service"
	"github.com/go-orz/orz"
	"github.com/labstack/echo/v5"
	"go.uber.org/zap"
)

type DNSProviderHandler struct {
	logger          *zap.Logger
	propertyService *service.PropertyService
}

func NewDNSProviderHandler(logger *zap.Logger, propertyService *service.PropertyService) *DNSProviderHandler {
	return &DNSProviderHandler{
		logger:          logger,
		propertyService: propertyService,
	}
}

// DNSProviderRequest 创建/更新 DNS Provider 请求
type DNSProviderRequest struct {
	Provider string                 `json:"provider"` // 服务商类型
	Enabled  bool                   `json:"enabled"`  // 是否启用
	Config   map[string]interface{} `json:"config"`   // 配置对象
}

// DNSProviderResponse DNS Provider 响应（脱敏）
type DNSProviderResponse struct {
	Provider string                 `json:"provider"` // 服务商类型
	Enabled  bool                   `json:"enabled"`  // 是否启用
	Config   map[string]interface{} `json:"config"`   // 配置对象（已脱敏）
}

// maskSensitiveData 脱敏敏感信息
func maskSensitiveData(config map[string]interface{}) map[string]interface{} {
	if config == nil {
		return nil
	}

	masked := make(map[string]interface{})
	for key, value := range config {
		lowerKey := strings.ToLower(key)
		// 对所有包含敏感关键词的字段进行脱敏
		if strings.Contains(lowerKey, "secret") ||
			strings.Contains(lowerKey, "key") ||
			strings.Contains(lowerKey, "token") ||
			strings.Contains(lowerKey, "password") {
			if str, ok := value.(string); ok && str != "" {
				// 保留前后各2个字符，中间用 **** 替代
				if len(str) <= 4 {
					masked[key] = "****"
				} else {
					masked[key] = str[:2] + "****" + str[len(str)-2:]
				}
			} else {
				masked[key] = "****"
			}
		} else {
			masked[key] = value
		}
	}
	return masked
}

// GetAll 获取所有 DNS Provider 配置（脱敏）
func (h *DNSProviderHandler) GetAll(c *echo.Context) error {
	ctx := c.Request().Context()

	providers, err := h.propertyService.GetDNSProviderConfigs(ctx)
	if err != nil {
		h.logger.Error("获取 DNS Provider 配置失败", zap.Error(err))
		return echo.NewHTTPError(http.StatusInternalServerError, "获取配置失败")
	}

	// 脱敏处理
	var response []DNSProviderResponse
	for _, p := range providers {
		response = append(response, DNSProviderResponse{
			Provider: p.Provider,
			Enabled:  p.Enabled,
			Config:   maskSensitiveData(p.Config),
		})
	}

	return c.JSON(http.StatusOK, response)
}

// Upsert 创建或更新 DNS Provider 配置
func (h *DNSProviderHandler) Upsert(c *echo.Context) error {
	ctx := c.Request().Context()

	var req DNSProviderRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "请求参数错误")
	}

	// 验证 provider 类型
	validProviders := map[string]bool{
		"aliyun":       true,
		"tencentcloud": true,
		"cloudflare":   true,
		"huaweicloud":  true,
	}
	if !validProviders[req.Provider] {
		return echo.NewHTTPError(http.StatusBadRequest, "不支持的 DNS 服务商类型")
	}

	// 验证配置字段
	if err := h.validateProviderConfig(req.Provider, req.Config); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	provider := models.DNSProviderConfig{
		Provider: req.Provider,
		Enabled:  req.Enabled,
		Config:   req.Config,
	}

	if err := h.propertyService.UpsertDNSProvider(ctx, provider); err != nil {
		h.logger.Error("保存 DNS Provider 配置失败", zap.Error(err))
		return echo.NewHTTPError(http.StatusInternalServerError, "保存配置失败")
	}

	return c.JSON(http.StatusOK, orz.Map{})
}

// Delete 删除 DNS Provider 配置
func (h *DNSProviderHandler) Delete(c *echo.Context) error {
	ctx := c.Request().Context()
	provider := c.Param("provider")

	if provider == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "provider 参数不能为空")
	}

	if err := h.propertyService.DeleteDNSProvider(ctx, provider); err != nil {
		h.logger.Error("删除 DNS Provider 配置失败", zap.Error(err))
		return echo.NewHTTPError(http.StatusInternalServerError, "删除配置失败")
	}

	return c.JSON(http.StatusOK, orz.Map{})
}

// validateProviderConfig 验证不同服务商的配置字段
func (h *DNSProviderHandler) validateProviderConfig(provider string, config map[string]interface{}) error {
	switch provider {
	case "aliyun":
		if config["accessKeyId"] == nil || config["accessKeyId"] == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "accessKeyId 不能为空")
		}
		if config["accessKeySecret"] == nil || config["accessKeySecret"] == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "accessKeySecret 不能为空")
		}
	case "tencentcloud":
		if config["secretId"] == nil || config["secretId"] == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "secretId 不能为空")
		}
		if config["secretKey"] == nil || config["secretKey"] == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "secretKey 不能为空")
		}
	case "cloudflare":
		if config["apiToken"] == nil || config["apiToken"] == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "apiToken 不能为空")
		}
	case "huaweicloud":
		if config["accessKeyId"] == nil || config["accessKeyId"] == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "accessKeyId 不能为空")
		}
		if config["secretAccessKey"] == nil || config["secretAccessKey"] == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "secretAccessKey 不能为空")
		}
		// region 可选，提供默认值
		if config["region"] == nil || config["region"] == "" {
			config["region"] = "cn-south-1"
		}
	}
	return nil
}
