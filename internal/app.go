package internal

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/dushixiang/pika/internal/assets"
	"github.com/dushixiang/pika/internal/config"
	"github.com/dushixiang/pika/internal/handler"
	"github.com/dushixiang/pika/internal/migrate"
	"github.com/dushixiang/pika/internal/models"
	"github.com/dushixiang/pika/internal/scheduler"
	"github.com/dushixiang/pika/internal/service"
	"github.com/dushixiang/pika/pkg/version"
	"github.com/google/uuid"

	"github.com/go-errors/errors"
	"github.com/go-orz/orz"
	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func Run(configPath string) {
	err := orz.Quick(configPath, setup)
	if err != nil {
		log.Fatal(err)
	}
}

func setup(app *orz.App) error {
	// 数据库迁移
	if err := autoMigrate(app.GetDatabase()); err != nil {
		return err
	}

	// 读取应用配置
	var appConfig config.AppConfig
	_config := app.GetConfig()
	if _config != nil {
		if err := _config.App.Unmarshal(&appConfig); err != nil {
			app.Logger().Error("读取配置失败", zap.Error(err))
			return err
		}
	}

	// 设置默认值
	if appConfig.JWT.Secret == "" {
		appConfig.JWT.Secret = uuid.NewString()
		app.Logger().Warn("未配置JWT密钥，使用随机UUID")
	}
	if appConfig.JWT.ExpiresHours == 0 {
		appConfig.JWT.ExpiresHours = 168 // 7天
	}

	// 初始化应用组件
	components, err := InitializeApp(app.Logger(), app.GetDatabase(), &appConfig)
	if err != nil {
		return err
	}

	// 自动化升级数据库
	if err := migrate.AutoMigrate(app.Logger(), app.GetDatabase(), components.PropertyService); err != nil {
		app.Logger().Warn("数据库自动升级失败", zap.Error(err))
	}

	// 初始化默认属性配置
	ctx := context.Background()
	if err := components.ApiKeyService.FillLegacyApiKeyType(ctx); err != nil {
		app.Logger().Warn("回填旧版 API Key 类型失败", zap.Error(err))
		// 不返回错误，ValidateApiKey 仍会兼容空类型旧密钥
	}
	if err := initDefaultProperties(ctx, components, app.Logger()); err != nil {
		app.Logger().Error("初始化默认属性配置失败", zap.Error(err))
		// 不返回错误，继续启动
	}
	// 初始化探针的状态全部为离线
	if err := components.AgentService.InitStatus(ctx); err != nil {
		app.Logger().Error("初始化探针状态失败", zap.Error(err))
		// 不返回错误，继续启动
	}

	// 启动WebSocket管理器
	go components.WSManager.Run(ctx)

	// 启动指标监控任务（用于告警检测）
	go startMetricsMonitoring(ctx, components, app.Logger())

	// 启动服务监控任务调度器
	monitorScheduler := scheduler.NewMonitorScheduler(components.MonitorService, app.Logger())
	// 将调度器注入到 MonitorService（避免循环依赖）
	components.MonitorService.SetScheduler(monitorScheduler)
	monitorScheduler.Start(ctx)

	// 启动流量重置检查任务(每小时检查一次)
	go startTrafficResetCheck(ctx, components, app.Logger())
	// 启动机器到期提醒检查任务(每小时检查一次)
	go startAgentExpireCheck(ctx, components, app.Logger())

	// 启动 DDNS 定时任务
	go components.DDNSService.Run(ctx)
	// 启动公网 IP 采集定时任务
	go components.PublicIPService.Run(ctx)

	// 设置API
	if err := setupApi(app, components); err != nil {
		return err
	}

	return nil
}

func setupApi(app *orz.App, components *AppComponents) error {
	logger := app.Logger()
	e := app.GetEcho()

	e.Use(middleware.Recover())
	e.Use(ErrorHandler(logger))

	// 自定义 IP 提取器中间件（支持任意 header）
	// 框架只支持 x-forwarded-for/x-real-ip/direct，其他配置会被 fallback 到 direct
	// 这里统一处理：从配置的 header 取 IP，并验证 trust list 防止伪造
	cfg := app.GetConfig()
	if cfg != nil && cfg.Server.IPExtractor != "" && cfg.Server.IPExtractor != "direct" {
		headerName := cfg.Server.IPExtractor

		// 解析 trust list，支持 CIDR 格式 (10.0.0.0/8) 或普通 IP (127.0.0.1 自动补 /32 或 /128)
		var trustCIDRs []*net.IPNet
		for _, s := range cfg.Server.IPTrustList {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			// 先尝试 CIDR 格式
			_, cidr, err := net.ParseCIDR(s)
			if err != nil {
				// 尝试普通 IP，自动补 /32 (IPv4) 或 /128 (IPv6)
				ip := net.ParseIP(s)
				if ip == nil {
					logger.Warn("invalid IP in trust list", zap.String("ip", s))
					continue
				}
				// 根据 IP 类型补 CIDR 后缀
				if ip.To4() != nil {
					_, cidr, err = net.ParseCIDR(s + "/32")
				} else {
					_, cidr, err = net.ParseCIDR(s + "/128")
				}
				if err != nil {
					logger.Warn("failed to parse IP as CIDR", zap.String("ip", s), zap.Error(err))
					continue
				}
			}
			trustCIDRs = append(trustCIDRs, cidr)
		}

		e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				// 验证请求来源是否在 trust list 中
				if len(trustCIDRs) > 0 {
					remoteIP, _, _ := net.SplitHostPort(c.Request().RemoteAddr)
					if remoteIP == "" {
						remoteIP = c.Request().RemoteAddr
					}
					trusted := false
					for _, cidr := range trustCIDRs {
						if cidr.Contains(net.ParseIP(remoteIP)) {
							trusted = true
							break
						}
					}
					if !trusted {
						return next(c) // 非可信来源，不信任 header
					}
				}

				// 从 header 取 IP
				ip := c.Request().Header.Get(headerName)
				if ip != "" {
					if idx := strings.IndexByte(ip, ','); idx > 0 {
						ip = strings.TrimSpace(ip[:idx])
					}
					c.Request().RemoteAddr = ip
				}
				return next(c)
			}
		})
		logger.Info("custom IP extractor configured", zap.String("header", headerName), zap.Int("trustedCIDRs", len(trustCIDRs)))
	}

	webDir := assets.WebDir()
	if err := assets.RenderUIFilesInDir(webDir, components.PropertyService); err != nil {
		return fmt.Errorf("render ui files: %w", err)
	}

	// 静态文件服务
	e.Use(middleware.StaticWithConfig(middleware.StaticConfig{
		Skipper: func(c *echo.Context) bool {
			// 不处理接口
			if strings.HasPrefix(c.Request().RequestURI, "/api") {
				return true
			}
			// 不处理WebSocket
			if strings.HasPrefix(c.Request().RequestURI, "/ws") {
				return true
			}
			// 不暴露模板文件
			if strings.HasSuffix(c.Request().RequestURI, ".tmpl") {
				return true
			}
			return false
		},
		Root:       ".",
		Index:      "index.html",
		HTML5:      true,
		Browse:     false,
		IgnoreBase: false,
		Filesystem: os.DirFS(webDir),
	}))

	customValidator := CustomValidator{Validator: validator.New()}
	if err := customValidator.TransInit(); err != nil {
		logger.Fatal("failed to init custom validator", zap.Error(err))
	}
	e.Validator = &customValidator

	// 公开接口（无需认证）
	publicApi := e.Group("/api")
	{
		// 认证相关
		publicApi.POST("/login", components.AccountHandler.Login)
		publicApi.GET("/auth/config", components.AccountHandler.GetAuthConfig)
		publicApi.GET("/auth/oidc/url", components.AccountHandler.GetOIDCAuthURL)
		publicApi.GET("/auth/github/url", components.AccountHandler.GetGitHubAuthURL)

		// Agent 版本和下载（完全公开，无需任何认证）
		publicApi.GET("/agent/version", components.AgentHandler.GetAgentVersion)
		publicApi.GET("/agent/downloads/:filename", components.AgentHandler.DownloadAgent)
		publicApi.GET("/agent/install.sh", components.AgentHandler.GetInstallScript)
	}

	// 公开接口（支持可选认证）- 已登录返回全部数据，未登录只返回公开数据
	publicApiWithOptionalAuth := e.Group("/api")
	publicApiWithOptionalAuth.Use(OptionalJWTAuthMiddleware(components.AccountHandler))
	{
		// 探针信息（公开访问，支持可选认证）- 用于公共展示页面
		publicApiWithOptionalAuth.GET("/agents", components.AgentHandler.GetAgents)
		publicApiWithOptionalAuth.GET("/agents/tags", components.AgentHandler.GetTags)
		publicApiWithOptionalAuth.GET("/agents/:id", components.AgentHandler.Get)
		publicApiWithOptionalAuth.GET("/agents/:id/metrics", components.AgentHandler.GetMetrics)
		publicApiWithOptionalAuth.GET("/agents/:id/metrics/latest", components.AgentHandler.GetLatestMetrics)
		publicApiWithOptionalAuth.GET("/agents/:id/network-interfaces", components.AgentHandler.GetAvailableNetworkInterfaces)

		// 监控统计数据（公开访问，支持可选认证）- 用于公共展示页面
		publicApiWithOptionalAuth.GET("/monitors", components.MonitorHandler.GetMonitors)
		publicApiWithOptionalAuth.GET("/monitors/:id/stats", components.MonitorHandler.GetStatsByID)
		publicApiWithOptionalAuth.GET("/monitors/:id/agents", components.MonitorHandler.GetAgentStatsByID)
		publicApiWithOptionalAuth.GET("/monitors/:id/history", components.MonitorHandler.GetHistoryByID)

		// Logo（公开访问）- 用于公共页面只获取 Logo
		publicApiWithOptionalAuth.GET("/logo", components.PropertyHandler.GetLogo)
	}

	// WebSocket 路由（探针连接）
	e.GET("/ws/agent", components.AgentHandler.HandleWebSocket)

	// 管理员 API 路由（需要认证，支持 JWT Token 或 API Key）
	adminApi := e.Group("/api/admin")
	adminApi.Use(UnifiedAuthMiddleware(components.AccountHandler, components.ApiKeyService))
	{
		adminApi.GET("/version", func(c *echo.Context) error {
			return c.JSON(http.StatusOK, orz.Map{
				"version":      version.GetVersion(),
				"agentVersion": version.GetAgentVersion(),
			})
		})
		// 账户相关
		adminApi.GET("/account/info", components.AccountHandler.GetCurrentUser)
		adminApi.POST("/logout", components.AccountHandler.Logout)

		// API密钥管理
		adminApi.GET("/api-keys", components.ApiKeyHandler.Paging)
		adminApi.POST("/api-keys", components.ApiKeyHandler.Create)
		adminApi.POST("/admin-api-keys", components.ApiKeyHandler.CreateAdmin)
		adminApi.GET("/api-keys/:id", components.ApiKeyHandler.Get)
		adminApi.GET("/api-keys/:id/raw", components.ApiKeyHandler.GetRaw)
		adminApi.PUT("/api-keys/:id", components.ApiKeyHandler.Update)
		adminApi.DELETE("/api-keys/:id", components.ApiKeyHandler.Delete)
		adminApi.POST("/api-keys/:id/enable", components.ApiKeyHandler.Enable)
		adminApi.POST("/api-keys/:id/disable", components.ApiKeyHandler.Disable)

		// 探针管理（管理员功能）
		adminApi.POST("/server-url", components.AgentHandler.GetServerUrl)
		adminApi.GET("/agents", components.AgentHandler.Paging)
		adminApi.GET("/agents/statistics", components.AgentHandler.GetStatistics)
		adminApi.GET("/agents/tags", components.AgentHandler.GetTags)
		adminApi.GET("/agents/:id", components.AgentHandler.GetForAdmin)
		adminApi.GET("/agents/:id/metrics/latest", components.AgentHandler.GetAdminLatestMetrics)
		adminApi.PUT("/agents/:id", components.AgentHandler.UpdateInfo)
		adminApi.POST("/agents/batch/tags", components.AgentHandler.BatchUpdateTags)
		adminApi.POST("/agents/batch/visibility", components.AgentHandler.BatchUpdateVisibility)
		adminApi.DELETE("/agents/:id", components.AgentHandler.Delete)
		adminApi.POST("/agents/cleanup-metrics", components.AgentHandler.CleanupOrphanedAgentMetrics) // 清理残留指标数据
		adminApi.POST("/agents/:id/command", components.AgentHandler.SendCommand)

		// 流量管理（管理员访问）
		adminApi.GET("/agents/:id/traffic", components.AgentHandler.GetTrafficStats)
		adminApi.PUT("/agents/:id/traffic-config", components.AgentHandler.UpdateTrafficConfig)
		adminApi.POST("/agents/:id/traffic-reset", components.AgentHandler.ResetAgentTraffic)

		// VPS审计结果（管理员访问）
		adminApi.GET("/agents/:id/audit/result", components.AgentHandler.GetAuditResult)
		adminApi.GET("/agents/:id/audit/results", components.AgentHandler.ListAuditResults)

		// 防篡改管理（管理员功能）
		adminApi.GET("/agents/:id/tamper/config", components.TamperHandler.GetConfig)
		adminApi.PUT("/agents/:id/tamper/config", components.TamperHandler.UpdateConfig)
		adminApi.GET("/agents/:id/tamper/events", components.TamperHandler.ListEvents)
		adminApi.DELETE("/agents/:id/tamper/events", components.TamperHandler.DeleteEvents)

		// SSH 登录监控管理（管理员功能）
		adminApi.GET("/agents/:id/ssh-login/config", components.SSHLoginHandler.GetConfig)
		adminApi.POST("/agents/:id/ssh-login/config", components.SSHLoginHandler.UpdateConfig)
		adminApi.GET("/agents/:id/ssh-login/events", components.SSHLoginHandler.ListEvents)
		adminApi.DELETE("/agents/:id/ssh-login/events", components.SSHLoginHandler.DeleteEvents)

		// 通用属性管理
		adminApi.GET("/properties/:id", components.PropertyHandler.GetProperty)
		adminApi.PUT("/properties/:id", components.PropertyHandler.SetProperty)

		// 通知渠道测试（从数据库读取配置测试）
		adminApi.POST("/notification-channels/:type/test", components.PropertyHandler.TestNotificationChannel)

		// 告警记录查询
		adminApi.GET("/alert-records", components.AlertHandler.ListAlertRecords)
		adminApi.DELETE("/alert-records", components.AlertHandler.ClearAlertRecords)

		// 服务监控配置
		adminApi.GET("/monitors", components.MonitorHandler.List)
		adminApi.POST("/monitors", components.MonitorHandler.Create)
		adminApi.GET("/monitors/:id", components.MonitorHandler.Get)
		adminApi.PUT("/monitors/:id", components.MonitorHandler.Update)
		adminApi.DELETE("/monitors/:id", components.MonitorHandler.Delete)

		// DNS Provider 管理
		adminApi.GET("/dns-providers", components.DNSProviderHandler.GetAll)
		adminApi.POST("/dns-providers", components.DNSProviderHandler.Upsert)
		adminApi.DELETE("/dns-providers/:provider", components.DNSProviderHandler.Delete)

		// DDNS 配置管理
		adminApi.GET("/ddns", components.DDNSHandler.Paging)
		adminApi.POST("/ddns", components.DDNSHandler.Create)
		adminApi.GET("/ddns/:id", components.DDNSHandler.Get)
		adminApi.PUT("/ddns/:id", components.DDNSHandler.Update)
		adminApi.DELETE("/ddns/:id", components.DDNSHandler.Delete)
		adminApi.POST("/ddns/:id/enable", components.DDNSHandler.Enable)
		adminApi.POST("/ddns/:id/disable", components.DDNSHandler.Disable)
		adminApi.GET("/ddns/:id/records", components.DDNSHandler.GetRecords)
		adminApi.POST("/ddns/:id/trigger", components.DDNSHandler.TriggerUpdate)
	}

	// OIDC 认证路由（如果启用）
	publicApi.POST("/auth/oidc/callback", components.AccountHandler.OIDCLogin)

	// GitHub 认证路由（如果启用）
	publicApi.POST("/auth/github/callback", components.AccountHandler.GitHubLogin)

	return nil
}

func autoMigrate(database *gorm.DB) error {
	// 自动迁移数据库表
	return database.AutoMigrate(
		&models.Agent{},         // 探针
		&models.ApiKey{},        // ApiKey
		&models.AuditResult{},   // 审计历史
		&models.Property{},      // 系统属性
		&models.AlertRecord{},   // 告警记录
		&models.AlertState{},    // 告警状态
		&models.MonitorTask{},   // 服务监控
		&models.TamperEvent{},   // 防篡改事件
		&models.DDNSConfig{},    // DDNS 配置
		&models.DDNSRecord{},    // DDNS 记录
		&models.SSHLoginEvent{}, // SSH 登录事件
	)
}

// initDefaultProperties 初始化默认属性配置
func initDefaultProperties(ctx context.Context, components *AppComponents, logger *zap.Logger) error {
	// 使用 PropertyService 的初始化方法
	return components.PropertyService.InitializeDefaultConfigs(ctx)
}

func ErrorHandler(logger *zap.Logger) func(next echo.HandlerFunc) echo.HandlerFunc {
	var a = func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if err := next(c); err != nil {
				var he *echo.HTTPError
				if errors.As(err, &he) {
					return c.JSON(he.Code, orz.Map{
						"code":    he.Code,
						"message": err.Error(),
					})
				}

				var oe *orz.Error
				if errors.As(err, &oe) {
					return c.JSON(400, orz.Map{
						"code":    oe.Code,
						"message": err.Error(),
					})
				}

				logger.Sugar().Errorf("[ERROR] %s", err.Error())

				return c.JSON(500, orz.Map{
					"code":    500,
					"message": "Internal Server Error",
				})
			}
			return nil
		}
	}
	return a
}

// startMetricsMonitoring 启动指标监控任务（用于告警检测）
func startMetricsMonitoring(ctx context.Context, components *AppComponents, logger *zap.Logger) {
	logger.Info("启动指标监控任务")

	ticker := time.NewTicker(30 * time.Second) // 每30秒检查一次
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("指标监控任务已停止")
			return
		case <-ticker.C:
			// 检查所有在线探针的最新指标
			agents, err := components.AgentService.ListOnlineAgents(ctx)
			if err != nil {
				logger.Error("获取在线探针失败", zap.Error(err))
				continue
			}

			for _, agent := range agents {
				// 获取最新指标
				latest, ok := components.MetricService.GetLatestMetrics(agent.ID)
				if !ok {
					logger.Debug("获取探针最新指标失败", zap.String("agentId", agent.ID))
					continue
				}
				if latest == nil {
					logger.Debug("探针最新指标为空", zap.String("agentId", agent.ID))
					continue
				}

				// 提取 CPU、内存、磁盘使用率、网速
				var cpuUsage, memoryUsage, diskUsage, networkSpeed float64

				if latest.CPU != nil {
					cpuUsage = latest.CPU.UsagePercent
				}

				if latest.Memory != nil {
					memoryUsage = latest.Memory.UsagePercent
				}

				if latest.Disk != nil {
					diskUsage = latest.Disk.UsagePercent
				}

				if latest.Network != nil {
					// 网速 = (发送速率 + 接收速率) / 1024 / 1024 (转换为 MB/s)
					networkSpeed = float64(latest.Network.TotalBytesSentRate+latest.Network.TotalBytesRecvRate) / 1024 / 1024
				}

				// 检查告警规则
				if err := components.AlertService.CheckMetrics(ctx, agent.ID, cpuUsage, memoryUsage, diskUsage, networkSpeed); err != nil {
					logger.Error("检查告警规则失败", zap.String("agentId", agent.ID), zap.Error(err))
				}
			}

			// 检查监控相关告警（证书和服务下线）
			if err := components.AlertService.CheckMonitorAlerts(ctx); err != nil {
				logger.Error("检查监控告警失败", zap.Error(err))
			}
		}
	}
}

// startTrafficResetCheck 启动流量重置检查定时任务
func startTrafficResetCheck(ctx context.Context, components *AppComponents, logger *zap.Logger) {
	logger.Info("启动流量重置检查任务")

	ticker := time.NewTicker(1 * time.Hour) // 每小时检查一次
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("流量重置检查任务已停止")
			return
		case <-ticker.C:
			if err := components.TrafficService.CheckAndResetTraffic(ctx); err != nil {
				logger.Error("流量重置检查失败", zap.Error(err))
			}
		}
	}
}

// startAgentExpireCheck 启动机器到期提醒定时任务
func startAgentExpireCheck(ctx context.Context, components *AppComponents, logger *zap.Logger) {
	logger.Info("启动机器到期提醒检查任务")

	ticker := time.NewTicker(1 * time.Hour) // 每小时检查一次
	defer ticker.Stop()

	check := func() {
		if err := components.AlertService.CheckAgentExpireAlerts(ctx); err != nil {
			logger.Error("机器到期提醒检查失败", zap.Error(err))
		}
	}

	check()
	for {
		select {
		case <-ctx.Done():
			logger.Info("机器到期提醒检查任务已停止")
			return
		case <-ticker.C:
			check()
		}
	}
}

// JWTAuthMiddleware JWT 认证中间件（必须登录）
func JWTAuthMiddleware(accountHandler *handler.AccountHandler) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			// 从 Authorization header 获取 token
			authHeader := c.Request().Header.Get("Authorization")
			if authHeader == "" {
				return echo.NewHTTPError(http.StatusUnauthorized, "未提供认证令牌")
			}

			// 检查 Bearer 前缀
			const bearerPrefix = "Bearer "
			if len(authHeader) < len(bearerPrefix) || authHeader[:len(bearerPrefix)] != bearerPrefix {
				return echo.NewHTTPError(http.StatusUnauthorized, "认证令牌格式错误")
			}

			tokenString := authHeader[len(bearerPrefix):]

			// 验证 token
			claims, err := accountHandler.ValidateToken(tokenString)
			if err != nil {
				return echo.NewHTTPError(http.StatusUnauthorized, "认证令牌无效: "+err.Error())
			}

			// 将用户信息存入 context
			c.Set("userID", claims.UserID)
			c.Set("username", claims.Username)
			c.Set("authenticated", true)

			return next(c)
		}
	}
}

// OptionalJWTAuthMiddleware 可选 JWT 认证中间件（尝试解析 token，但不强制要求）
func OptionalJWTAuthMiddleware(accountHandler *handler.AccountHandler) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			// 从 Authorization header 获取 token
			authHeader := c.Request().Header.Get("Authorization")
			if authHeader != "" {
				// 检查 Bearer 前缀
				const bearerPrefix = "Bearer "
				if len(authHeader) >= len(bearerPrefix) && authHeader[:len(bearerPrefix)] == bearerPrefix {
					tokenString := authHeader[len(bearerPrefix):]

					// 尝试验证 token
					claims, err := accountHandler.ValidateToken(tokenString)
					if err == nil {
						// token 有效，将用户信息存入 context
						c.Set("userID", claims.UserID)
						c.Set("username", claims.Username)
						c.Set("authenticated", true)
					}
				}
			}

			// 无论 token 是否有效，都继续处理请求
			return next(c)
		}
	}
}

// APIKeyAuthMiddleware 使用 API Key 进行认证
func APIKeyAuthMiddleware(apiKeyService *service.ApiKeyService) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			authHeader := c.Request().Header.Get("Authorization")
			if authHeader == "" {
				return echo.NewHTTPError(http.StatusUnauthorized, "未提供认证令牌")
			}
			const bearerPrefix = "Bearer "
			if len(authHeader) < len(bearerPrefix) || authHeader[:len(bearerPrefix)] != bearerPrefix {
				return echo.NewHTTPError(http.StatusUnauthorized, "认证令牌格式错误")
			}
			key := authHeader[len(bearerPrefix):]
			apiKey, err := apiKeyService.ValidateApiKey(c.Request().Context(), key, "admin")
			if err != nil {
				return echo.NewHTTPError(http.StatusUnauthorized, "API Key 无效")
			}
			c.Set("userID", apiKey.CreatedBy)
			c.Set("username", apiKey.CreatedBy)
			c.Set("authenticated", true)
			return next(c)
		}
	}
}

// UnifiedAuthMiddleware 统一认证中间件：优先尝试 JWT Token，失败后尝试 API Key
func UnifiedAuthMiddleware(accountHandler *handler.AccountHandler, apiKeyService *service.ApiKeyService) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			authHeader := c.Request().Header.Get("Authorization")
			if authHeader == "" {
				return echo.NewHTTPError(http.StatusUnauthorized, "未提供认证令牌")
			}
			const bearerPrefix = "Bearer "
			if len(authHeader) < len(bearerPrefix) || authHeader[:len(bearerPrefix)] != bearerPrefix {
				return echo.NewHTTPError(http.StatusUnauthorized, "认证令牌格式错误")
			}
			token := authHeader[len(bearerPrefix):]

			// 先尝试 JWT Token
			claims, err := accountHandler.ValidateToken(token)
			if err == nil {
				c.Set("userID", claims.UserID)
				c.Set("username", claims.Username)
				c.Set("authType", "jwt")
				c.Set("authenticated", true)
				return next(c)
			}

			// 再尝试 API Key（仅 admin 类型可用于管理接口）
			apiKey, err := apiKeyService.ValidateApiKey(c.Request().Context(), token, "admin")
			if err != nil {
				return echo.NewHTTPError(http.StatusUnauthorized, "认证令牌无效")
			}
			c.Set("userID", apiKey.CreatedBy)
			c.Set("username", apiKey.CreatedBy)
			c.Set("authType", "api_key")
			c.Set("authenticated", true)
			return next(c)
		}
	}
}
