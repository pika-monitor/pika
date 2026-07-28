package handler

import (
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/dushixiang/pika/internal/assets"
	"github.com/dushixiang/pika/internal/models"
	"github.com/dushixiang/pika/internal/service"
	"github.com/go-orz/orz"
	"github.com/labstack/echo/v5"
	"go.uber.org/zap"
)

type PropertyHandler struct {
	logger   *zap.Logger
	service  *service.PropertyService
	notifier *service.Notifier
}

func NewPropertyHandler(logger *zap.Logger, service *service.PropertyService, notifier *service.Notifier) *PropertyHandler {
	return &PropertyHandler{
		logger:   logger,
		service:  service,
		notifier: notifier,
	}
}

// GetProperty 获取属性（返回 JSON 值）
func (h *PropertyHandler) GetProperty(c *echo.Context) error {
	id := c.Param("id")

	property, err := h.service.Get(c.Request().Context(), id)
	if err != nil {
		h.logger.Error("获取属性失败", zap.String("id", id), zap.Error(err))
		return orz.NewError(500, "获取属性失败")
	}

	// 解析 JSON 值
	var value interface{}
	if property.Value != "" {
		if err := json.Unmarshal([]byte(property.Value), &value); err != nil {
			h.logger.Error("解析属性值失败", zap.String("id", id), zap.Error(err))
			return orz.NewError(500, "解析属性值失败")
		}
	}

	return orz.Ok(c, map[string]interface{}{
		"id":    property.ID,
		"name":  property.Name,
		"value": value,
	})
}

// SetProperty 设置属性
func (h *PropertyHandler) SetProperty(c *echo.Context) error {
	id := c.Param("id")

	var req struct {
		Name  string      `json:"name"`
		Value interface{} `json:"value"`
	}

	if err := c.Bind(&req); err != nil {
		return orz.NewError(400, "无效的请求参数")
	}

	// 特殊校验：系统配置
	if id == service.PropertyIDSystemConfig {
		// 将 map 转为 SystemConfig 结构体进行校验 (或者直接校验 map)
		// 这里 req.Value 是 map[string]interface{}
		if valMap, ok := req.Value.(map[string]interface{}); ok {
			var nameZh, nameEn string
			if v, ok := valMap["systemNameZh"].(string); ok {
				nameZh = v
			}
			if v, ok := valMap["systemNameEn"].(string); ok {
				nameEn = v
			}

			if nameZh == "" && nameEn == "" {
				return orz.NewError(400, "系统名称（中文）和系统名称（英文）不能同时为空")
			}
		}
	}

	if err := h.service.Set(c.Request().Context(), id, req.Name, req.Value); err != nil {
		h.logger.Error("设置属性失败", zap.String("id", id), zap.Error(err))
		return orz.NewError(500, "设置属性失败")
	}
	if id == service.PropertyIDSystemConfig {
		if err := assets.RenderUIFiles(h.service); err != nil {
			h.logger.Error("渲染前端模板失败", zap.String("id", id), zap.Error(err))
			return orz.NewError(500, "渲染前端模板失败")
		}
	}

	return orz.Ok(c, orz.Map{})
}

// GetLogo 获取系统 Logo（公开访问，返回图片文件流）
func (h *PropertyHandler) GetLogo(c *echo.Context) error {
	sysConfig, err := h.service.GetSystemConfig(c.Request().Context())
	if err != nil {
		// 如果配置不存在，返回 404
		return echo.NewHTTPError(http.StatusNotFound, "Logo 不存在")
	}

	// 提取 logoBase64 字段
	logoBase64 := sysConfig.LogoBase64
	if logoBase64 == "" {
		return echo.NewHTTPError(http.StatusNotFound, "Logo 不存在")
	}

	// 解析 base64 数据
	// 格式: data:image/png;base64,iVBORw0KGgoAAAANS...
	var imageData []byte
	var contentType string

	// 检查是否包含 data URI 前缀
	if len(logoBase64) > 0 && logoBase64[:5] == "data:" {
		// 查找逗号位置
		commaIndex := -1
		for i := 0; i < len(logoBase64) && i < 100; i++ {
			if logoBase64[i] == ',' {
				commaIndex = i
				break
			}
		}

		if commaIndex == -1 {
			return echo.NewHTTPError(http.StatusInternalServerError, "无效的图片数据格式")
		}

		// 提取 MIME 类型
		header := logoBase64[5:commaIndex]
		if len(header) > 7 && header[len(header)-7:] == ";base64" {
			contentType = header[:len(header)-7]
		} else {
			contentType = "image/png" // 默认类型
		}

		// 解码 base64
		base64Data := logoBase64[commaIndex+1:]
		var decodeErr error
		imageData, decodeErr = base64.StdEncoding.DecodeString(base64Data)
		if decodeErr != nil {
			h.logger.Error("解码 base64 失败", zap.Error(decodeErr))
			return echo.NewHTTPError(http.StatusInternalServerError, "解码图片数据失败")
		}
	} else {
		// 直接是 base64 字符串
		var decodeErr error
		imageData, decodeErr = base64.StdEncoding.DecodeString(logoBase64)
		if decodeErr != nil {
			h.logger.Error("解码 base64 失败", zap.Error(decodeErr))
			return echo.NewHTTPError(http.StatusInternalServerError, "解码图片数据失败")
		}
		contentType = "image/png" // 默认类型
	}

	// 设置响应头
	c.Response().Header().Set("Content-Type", contentType)
	c.Response().Header().Set("Cache-Control", "public, max-age=600") // 缓存 10 分钟

	return c.Blob(http.StatusOK, contentType, imageData)
}

// TestNotificationChannel 测试通知渠道（从数据库读取配置）
func (h *PropertyHandler) TestNotificationChannel(c *echo.Context) error {
	channelType := c.Param("type")
	if channelType == "" {
		return orz.NewError(400, "缺少渠道类型参数")
	}

	ctx := c.Request().Context()

	channels, err := h.service.GetNotificationChannelConfigs(c.Request().Context())
	if err != nil {
		h.logger.Error("获取通知渠道配置失败", zap.Error(err))
		return orz.NewError(500, "获取通知渠道配置失败")
	}

	// 查找指定类型的渠道
	var targetChannel *models.NotificationChannelConfig
	for i := range channels {
		if channels[i].Type == channelType {
			targetChannel = &channels[i]
			break
		}
	}

	if targetChannel == nil {
		return orz.NewError(404, "通知渠道不存在，请先配置")
	}

	if !targetChannel.Enabled {
		return orz.NewError(400, "通知渠道未启用")
	}

	// 发送测试消息（动态匹配通知渠道类型）
	message := "这是一条测试通知消息"
	sendErr := h.notifier.SendTestNotification(ctx, targetChannel.Type, targetChannel.Config, message)

	if sendErr != nil {
		h.logger.Error("发送测试通知失败", zap.String("type", channelType), zap.Error(sendErr))
		return orz.NewError(500, "发送测试通知失败: "+sendErr.Error())
	}

	return orz.Ok(c, orz.Map{})
}
