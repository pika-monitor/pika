package handler

import (
	"github.com/dushixiang/pika/internal/service"
	"github.com/dushixiang/pika/internal/utils"
	"github.com/go-orz/orz"
	"github.com/labstack/echo/v5"
	"go.uber.org/zap"
)

type MonitorHandler struct {
	logger         *zap.Logger
	monitorService *service.MonitorService
	metricService  *service.MetricService
	agentService   *service.AgentService
}

func NewMonitorHandler(logger *zap.Logger, monitorService *service.MonitorService, metricService *service.MetricService, agentService *service.AgentService) *MonitorHandler {
	return &MonitorHandler{
		logger:         logger,
		monitorService: monitorService,
		metricService:  metricService,
		agentService:   agentService,
	}
}

func (h *MonitorHandler) List(c *echo.Context) error {
	keyword := c.QueryParam("keyword")
	enabled := c.QueryParam("enabled")

	pr := orz.GetPageRequest(c, "name")

	builder := orz.NewPageBuilder(h.monitorService.MonitorRepo).
		PageRequest(pr).
		Keyword([]string{"name", "target", "type"}, keyword)

	// 处理启用状态筛选
	if enabled == "true" {
		builder.Equal("enabled", "1")
	} else if enabled == "false" {
		builder.Equal("enabled", "0")
	}

	ctx := c.Request().Context()
	page, err := builder.Execute(ctx)
	if err != nil {
		return err
	}

	var agentIds []string
	for _, item := range page.Items {
		if len(item.AgentIds) > 0 {
			agentIds = append(agentIds, item.AgentIds...)
		}
	}
	if len(agentIds) > 0 {
		agents, err := h.agentService.AgentRepo.FindByIdIn(ctx, agentIds)
		if err != nil {
			return err
		}

		var agentNameMap = make(map[string]string)
		for _, agent := range agents {
			agentNameMap[agent.ID] = agent.Name
		}

		for i, monitor := range page.Items {
			if len(monitor.AgentIds) == 0 {
				continue
			}
			for _, agentId := range monitor.AgentIds {
				page.Items[i].AgentNames = append(page.Items[i].AgentNames, agentNameMap[agentId])
			}
		}
	}
	return orz.Ok(c, page)
}

func (h *MonitorHandler) Create(c *echo.Context) error {
	var req service.MonitorTaskRequest
	if err := c.Bind(&req); err != nil {
		return orz.NewError(400, "请求参数错误")
	}

	ctx := c.Request().Context()
	item, err := h.monitorService.CreateMonitor(ctx, &req)
	if err != nil {
		return err
	}

	return orz.Ok(c, item)
}

func (h *MonitorHandler) Get(c *echo.Context) error {
	id := c.Param("id")

	ctx := c.Request().Context()
	item, err := h.monitorService.FindById(ctx, id)
	if err != nil {
		return err
	}

	return orz.Ok(c, item)
}

func (h *MonitorHandler) Update(c *echo.Context) error {
	id := c.Param("id")

	var req service.MonitorTaskRequest
	if err := c.Bind(&req); err != nil {
		return orz.NewError(400, "请求参数错误")
	}

	ctx := c.Request().Context()
	item, err := h.monitorService.UpdateMonitor(ctx, id, &req)
	if err != nil {
		return err
	}

	return orz.Ok(c, item)
}

func (h *MonitorHandler) Delete(c *echo.Context) error {
	id := c.Param("id")

	ctx := c.Request().Context()
	// 删除服务监控在 VictoriaMetrics 中的历史指标数据
	if err := h.metricService.DeleteMonitorMetrics(ctx, id); err != nil {
		h.logger.Error("删除服务监控 VictoriaMetrics 数据失败",
			zap.String("monitorID", id),
			zap.Error(err))
		return orz.NewError(500, "删除服务监控指标数据失败")
	}

	if err := h.monitorService.DeleteMonitor(ctx, id); err != nil {
		return err
	}

	return nil
}

// GetMonitors 获取所有监控统计数据
func (h *MonitorHandler) GetMonitors(c *echo.Context) error {
	ctx := c.Request().Context()
	stats, err := h.monitorService.ListByAuth(ctx, utils.IsAuthenticated(c))
	if err != nil {
		return err
	}

	return orz.Ok(c, stats)
}

// GetStatsByID 获取指定监控任务的统计数据（公开接口，已登录返回全部，未登录返回公开可见）
func (h *MonitorHandler) GetStatsByID(c *echo.Context) error {
	id := c.Param("id")
	ctx := c.Request().Context()

	// 验证监控任务访问权限
	if _, err := h.monitorService.GetMonitorByAuth(ctx, id, utils.IsAuthenticated(c)); err != nil {
		return err
	}

	stats, err := h.monitorService.GetMonitorStatsByID(ctx, id)
	if err != nil {
		return err
	}

	return orz.Ok(c, stats)
}

// GetAgentStatsByID 获取指定监控任务各探针的统计数据（公开接口，已登录返回全部，未登录返回公开可见）
func (h *MonitorHandler) GetAgentStatsByID(c *echo.Context) error {
	id := c.Param("id")
	ctx := c.Request().Context()

	// 验证监控任务访问权限
	if _, err := h.monitorService.GetMonitorByAuth(ctx, id, utils.IsAuthenticated(c)); err != nil {
		return err
	}

	stats := h.metricService.GetMonitorAgentStats(ctx, id)
	for i := range stats {
		stats[i].Target = "" // 隐藏目标地址
	}
	return orz.Ok(c, stats)
}

// GetHistoryByID 获取指定监控任务的历史响应时间数据（公开接口，已登录返回全部，未登录返回公开可见）
func (h *MonitorHandler) GetHistoryByID(c *echo.Context) error {
	id := c.Param("id")
	ctx := c.Request().Context()

	// 验证监控任务访问权限
	if _, err := h.monitorService.GetMonitorByAuth(ctx, id, utils.IsAuthenticated(c)); err != nil {
		return err
	}

	timeRange := c.QueryParam("range")
	startParam := c.QueryParam("start")
	endParam := c.QueryParam("end")
	aggregation := normalizeAggregation(c.QueryParam("aggregation"))

	// 默认时间范围为 5 分钟
	if timeRange == "" && startParam == "" && endParam == "" {
		timeRange = "5m"
	}

	start, end, err := parseTimeRangeOrStartEnd(timeRange, startParam, endParam)
	if err != nil {
		return orz.NewError(400, err.Error())
	}

	history, err := h.metricService.GetMonitorHistory(ctx, id, start, end, aggregation)
	if err != nil {
		return err
	}

	return orz.Ok(c, history)
}
