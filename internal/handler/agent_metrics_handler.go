package handler

import (
	"fmt"
	"strconv"
	"time"

	"github.com/dushixiang/pika/internal/utils"
	"github.com/go-orz/orz"
	"github.com/labstack/echo/v5"
)

var validMetricTypes = map[string]struct{}{
	"cpu": {}, "memory": {}, "disk": {}, "network": {}, "network_connection": {},
	"disk_io": {}, "gpu": {}, "temperature": {}, "monitor": {},
}

var timeRangeMilliseconds = map[string]int64{
	"1m":  int64(time.Minute / time.Millisecond),
	"2m":  int64(2 * time.Minute / time.Millisecond),
	"5m":  int64(5 * time.Minute / time.Millisecond),
	"15m": int64(15 * time.Minute / time.Millisecond),
	"30m": int64(30 * time.Minute / time.Millisecond),
	"1h":  int64(time.Hour / time.Millisecond),
	"3h":  int64(3 * time.Hour / time.Millisecond),
	"6h":  int64(6 * time.Hour / time.Millisecond),
	"12h": int64(12 * time.Hour / time.Millisecond),
	"1d":  int64(24 * time.Hour / time.Millisecond),
	"24h": int64(24 * time.Hour / time.Millisecond),
	"3d":  int64(3 * 24 * time.Hour / time.Millisecond),
	"7d":  int64(7 * 24 * time.Hour / time.Millisecond),
	"30d": int64(30 * 24 * time.Hour / time.Millisecond),
}

// parseTimeRange 解析时间范围参数，返回起始和结束时间（毫秒）
func parseTimeRange(rangeParam string) (start, end int64, err error) {
	end = time.Now().UnixMilli()

	if rangeParam == "" {
		rangeParam = "1h" // 默认1小时
	}

	durationMs, ok := timeRangeMilliseconds[rangeParam]
	if !ok {
		return 0, 0, fmt.Errorf("无效的时间范围，支持: 1m, 2m, 5m, 15m, 30m, 1h, 3h, 6h, 12h, 1d/24h, 3d, 7d, 30d")
	}
	start = end - durationMs

	return start, end, nil
}

func parseTimeRangeOrStartEnd(rangeParam, startParam, endParam string) (start, end int64, err error) {
	if startParam != "" || endParam != "" {
		if startParam == "" || endParam == "" {
			return 0, 0, fmt.Errorf("start 和 end 必须同时提供")
		}

		start, err = strconv.ParseInt(startParam, 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("无效的 start 时间戳")
		}

		end, err = strconv.ParseInt(endParam, 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("无效的 end 时间戳")
		}

		if start >= end {
			return 0, 0, fmt.Errorf("start 必须小于 end")
		}

		return start, end, nil
	}

	return parseTimeRange(rangeParam)
}

func normalizeInterfaceName(name string) string {
	if name == "" {
		return "all"
	}
	return name
}

func validateMetricType(metricType string) error {
	if metricType == "" {
		return orz.NewError(400, "指标类型不能为空")
	}
	if _, ok := validMetricTypes[metricType]; !ok {
		return orz.NewError(400, "无效的指标类型")
	}
	return nil
}

// GetMetrics 获取探针聚合指标（公开接口，已登录返回全部，未登录返回公开可见）
func (h *AgentHandler) GetMetrics(c *echo.Context) error {
	shortID := c.Param("id")
	ctx := c.Request().Context()

	// 验证探针访问权限并获取完整探针信息
	agent, err := h.agentService.GetAgentByAuth(ctx, shortID, utils.IsAuthenticated(c))
	if err != nil {
		return err
	}
	agentID := agent.ID

	metricType := c.QueryParam("type")
	rangeParam := c.QueryParam("range")
	startParam := c.QueryParam("start")
	endParam := c.QueryParam("end")
	interfaceName := normalizeInterfaceName(c.QueryParam("interface"))
	aggregation := normalizeAggregation(c.QueryParam("aggregation"))

	if err := validateMetricType(metricType); err != nil {
		return err
	}

	// 解析时间范围
	start, end, err := parseTimeRangeOrStartEnd(rangeParam, startParam, endParam)
	if err != nil {
		return orz.NewError(400, err.Error())
	}

	// GetMetrics 内部会自动计算最优聚合间隔
	metrics, err := h.metricService.GetMetrics(ctx, agentID, metricType, start, end, interfaceName, aggregation)
	if err != nil {
		return err
	}

	// 直接返回 GetMetricsResponse，避免额外嵌套
	return orz.Ok(c, metrics)
}

// GetLatestMetrics 获取探针最新指标（公开接口，已登录返回全部，未登录返回公开可见）
func (h *AgentHandler) GetLatestMetrics(c *echo.Context) error {
	shortID := c.Param("id")
	ctx := c.Request().Context()

	// 验证探针访问权限并获取完整探针信息
	isAuthenticated := utils.IsAuthenticated(c)
	agent, err := h.agentService.GetAgentByAuth(ctx, shortID, isAuthenticated)
	if err != nil {
		return err
	}
	id := agent.ID

	metrics, ok := h.metricService.GetLatestMetrics(id)
	if !ok {
		return orz.NewError(404, "探针最新指标不存在")
	}
	// metrics 已经是独立 snapshot，未登录时直接在副本上抹掉敏感字段即可
	if !isAuthenticated {
		metrics.NetworkInterfaces = nil
	}
	return orz.Ok(c, metrics)
}

// GetAvailableNetworkInterfaces 获取探针的可用网卡列表（公开接口，已登录返回全部，未登录返回公开可见）
func (h *AgentHandler) GetAvailableNetworkInterfaces(c *echo.Context) error {
	shortID := c.Param("id")
	ctx := c.Request().Context()

	// 验证探针访问权限并获取完整探针信息
	agent, err := h.agentService.GetAgentByAuth(ctx, shortID, utils.IsAuthenticated(c))
	if err != nil {
		return err
	}
	id := agent.ID

	interfaces, err := h.metricService.GetAvailableNetworkInterfaces(ctx, id)
	if err != nil {
		return err
	}

	return orz.Ok(c, orz.Map{
		"interfaces": interfaces,
	})
}
