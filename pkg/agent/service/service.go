package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/dushixiang/pika/pkg/agent"
	"github.com/dushixiang/pika/pkg/agent/config"
	"github.com/dushixiang/pika/pkg/agent/sshmonitor"
	"github.com/dushixiang/pika/pkg/agent/sysutil"
	"github.com/dushixiang/pika/pkg/agent/updater"
	"github.com/kardianos/service"
)

// program 实现 service.Interface
type program struct {
	cfg    *config.Config
	agent  *Agent
	ctx    context.Context
	cancel context.CancelFunc
}

// configureICMP 配置 ICMP 权限（抽取通用逻辑）
func configureICMP() {
	if err := sysutil.ConfigureICMPPermissions(); err != nil {
		slog.Warn("配置 ICMP 权限失败", "error", err)
		slog.Info("提示: ICMP 监控可能需要 root 权限运行，或手动执行: sudo sysctl -w net.ipv4.ping_group_range=\"0 2147483647\"")
	}
}

// startAgent 启动 Agent 和自动更新（抽取通用逻辑）
func startAgent(ctx context.Context, cfg *config.Config) *Agent {
	if err := cleanupLegacyMetricsBuffer(); err != nil {
		slog.Warn("failed to clean legacy metrics buffer", "error", err)
	}

	// 创建 Agent 实例
	a := New(cfg)

	// 启动自动更新（如果启用）
	if cfg.AutoUpdate.Enabled {
		upd, err := updater.New(cfg, GetVersion())
		if err != nil {
			slog.Warn("创建更新器失败", "error", err)
		} else {
			go upd.Start(ctx)
		}
	}

	// 在后台启动 Agent
	go func() {
		if err := a.Start(ctx); err != nil {
			slog.Warn("探针运行出错", "error", err)
		}
	}()

	return a
}

// Start 启动服务
func (p *program) Start(s service.Service) error {
	// 初始化日志系统
	agent.InitLogger(&agent.LogConfig{
		Level:      p.cfg.Agent.LogLevel,
		File:       p.cfg.Agent.LogFile,
		MaxSize:    p.cfg.Agent.LogMaxSize,
		MaxBackups: p.cfg.Agent.LogMaxBackups,
		MaxAge:     p.cfg.Agent.LogMaxAge,
		Compress:   p.cfg.Agent.LogCompress,
	})

	slog.Info("Pika Agent 服务启动中...")

	// 初始化系统配置（Linux ICMP 权限等）
	configureICMP()

	// 创建 context
	p.ctx, p.cancel = context.WithCancel(context.Background())

	// 启动 Agent
	p.agent = startAgent(p.ctx, p.cfg)

	return nil
}

// Stop 停止服务
func (p *program) Stop(s service.Service) error {
	slog.Info("Pika Agent 服务停止中...")

	if p.cancel != nil {
		p.cancel()
	}

	if p.agent != nil {
		p.agent.Stop()
	}

	slog.Info("Pika Agent 服务已停止")
	return nil
}

// ServiceManager 服务管理器
type ServiceManager struct {
	cfg     *config.Config
	service service.Service
}

// systemd 自定义模板（支持自定义 RestartSec）
const systemdScript = `[Unit]
Description={{.Description}}
ConditionFileIsExecutable={{.Path|cmdEscape}}
{{range $i, $dep := .Dependencies}} 
{{$dep}} {{end}}

[Service]
StartLimitInterval=5
StartLimitBurst=10
ExecStart={{.Path|cmdEscape}}{{range .Arguments}} {{.|cmd}}{{end}}
{{if .ChRoot}}RootDirectory={{.ChRoot|cmd}}{{end}}
{{if .WorkingDirectory}}WorkingDirectory={{.WorkingDirectory|cmdEscape}}{{end}}
{{if .UserName}}User={{.UserName}}{{end}}
{{if .ReloadSignal}}ExecReload=/bin/kill -{{.ReloadSignal}} "$MAINPID"{{end}}
{{if .PIDFile}}PIDFile={{.PIDFile|cmd}}{{end}}
{{if and .LogOutput .HasOutputFileSupport -}}
StandardOutput=file:{{.LogDirectory}}/{{.Name}}.out
StandardError=file:{{.LogDirectory}}/{{.Name}}.err
{{- end}}
{{if gt .LimitNOFILE -1 }}LimitNOFILE={{.LimitNOFILE}}{{end}}
{{if .Restart}}Restart={{.Restart}}{{end}}
{{if .SuccessExitStatus}}SuccessExitStatus={{.SuccessExitStatus}}{{end}}
RestartSec=5
EnvironmentFile=-/etc/sysconfig/{{.Name}}

{{range $k, $v := .EnvVars -}}
Environment={{$k}}={{$v}}
{{end -}}

[Install]
WantedBy=multi-user.target
`

// NewServiceManager 创建服务管理器
func NewServiceManager(cfg *config.Config) (*ServiceManager, error) {
	// 获取可执行文件路径
	execPath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("get executable path failed: %w", err)
	}

	var options = service.KeyValue{
		// 其他 Unix 系统 (upstart/launchd)
		"KeepAlive": true, // 保持运行
		"RunAtLoad": true, // 启动时运行
	}

	switch runtime.GOOS {
	case "darwin":
	case "linux":
		// 使用自定义 systemd 模板（支持自定义 RestartSec=5）
		options["SystemdScript"] = systemdScript
	case "windows":
		// 失败动作: 重启服务
		options["OnFailure"] = "restart"

		// 重启延迟: 单位为毫秒 (Milliseconds)
		// 设置为 "0" 表示立即重启，设置为 "1000" 表示 1秒后重启
		// 建议保留至少 1秒 (1000ms) 的缓冲，防止极端情况下的 CPU 飙升
		options["RestartDelay"] = "1000"

		// 重置失败计数的时间: 单位为秒 (Seconds)
		// 意思是：如果服务连续运行了 24小时(86400秒)没有崩溃，那么之前的失败计数就会清零
		options["ResetPeriod"] = "86400"
	}

	// 配置服务
	svcConfig := &service.Config{
		Name:        "pika-agent",
		DisplayName: "Pika Agent",
		Description: "Pika 监控探针 - 采集系统性能指标并上报到服务端",
		Arguments:   []string{"run", "--config", cfg.Path},
		Executable:  execPath,
		Option:      options,
	}

	// 创建 program
	prg := &program{
		cfg: cfg,
	}

	// 创建服务
	s, err := service.New(prg, svcConfig)
	if err != nil {
		return nil, fmt.Errorf("create service failed: %w", err)
	}

	return &ServiceManager{
		cfg:     cfg,
		service: s,
	}, nil
}

// Install 安装服务
func (m *ServiceManager) Install() error {
	// 先尝试直接安装
	err := m.service.Install()
	if err == nil {
		return nil
	}

	// 只有当是"服务已存在"的错误时才清理重装
	if strings.Contains(err.Error(), "already exists") {
		slog.Info("服务已存在，先清理旧服务再重新安装...")
		_ = m.service.Stop()
		_ = m.service.Uninstall()
		// 再次尝试安装
		return m.service.Install()
	}

	// 其他错误直接返回
	return err
}

// Uninstall 卸载服务
func (m *ServiceManager) Uninstall() error {
	// 先停止服务
	_ = m.service.Stop()

	return m.service.Uninstall()
}

// Start 启动服务
func (m *ServiceManager) Start() error {
	return m.service.Start()
}

// Stop 停止服务
func (m *ServiceManager) Stop() error {
	return m.service.Stop()
}

// Restart 重启服务
func (m *ServiceManager) Restart() error {
	return m.service.Restart()
}

// Status 查看服务状态
func (m *ServiceManager) Status() (string, error) {
	status, err := m.service.Status()
	if err != nil {
		return "", err
	}

	var statusStr string
	switch status {
	case service.StatusRunning:
		statusStr = "运行中 (Running)"
	case service.StatusStopped:
		statusStr = "已停止 (Stopped)"
	case service.StatusUnknown:
		statusStr = "未知 (Unknown)"
	default:
		statusStr = fmt.Sprintf("状态: %d", status)
	}

	return statusStr, nil
}

// Run 运行服务（用于 service run 命令）
func (m *ServiceManager) Run() error {
	// 检查是否在服务模式下运行
	interactive := service.Interactive()

	if !interactive {
		// 在服务管理器控制下运行
		return m.service.Run()
	}

	// 交互模式（前台运行）
	// 初始化日志系统
	agent.InitLogger(&agent.LogConfig{
		Level:      m.cfg.Agent.LogLevel,
		File:       m.cfg.Agent.LogFile,
		MaxSize:    m.cfg.Agent.LogMaxSize,
		MaxBackups: m.cfg.Agent.LogMaxBackups,
		MaxAge:     m.cfg.Agent.LogMaxAge,
		Compress:   m.cfg.Agent.LogCompress,
	})

	slog.Info("配置加载成功",
		"server_endpoint", m.cfg.Server.Endpoint)

	// 初始化系统配置（Linux ICMP 权限等）
	configureICMP()

	// 创建 context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 监听系统信号
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)

	// 启动 Agent
	a := startAgent(ctx, m.cfg)

	// 等待中断信号
	<-interrupt
	slog.Info("收到中断信号，正在关闭...")
	cancel()

	// 等待 Agent 停止
	a.Stop()
	slog.Info("探针已停止")

	return nil
}

// UninstallAgent 执行探针卸载操作（可被复用）
func UninstallAgent(cfgPath string) error {
	// 加载配置
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config failed: %w", err)
	}

	// 创建服务管理器
	mgr, err := NewServiceManager(cfg)
	if err != nil {
		return fmt.Errorf("create service manager failed: %w", err)
	}

	// 检查服务状态，如果在运行则停止
	status, err := mgr.Status()
	if err != nil {
		slog.Warn("获取服务状态失败", "error", err)
	} else if status != "已停止 (Stopped)" {
		if err := mgr.Stop(); err != nil {
			return fmt.Errorf("stop service failed: %w", err)
		}
	}

	// 卸载服务
	if err := mgr.Uninstall(); err != nil {
		return fmt.Errorf("uninstall service failed: %w", err)
	}

	// 清理 SSH 监控配置
	monitor := sshmonitor.NewMonitor()
	return cleanupAgentArtifacts(cfgPath, monitor.Uninstall)
}

func cleanupAgentArtifacts(cfgPath string, uninstallSSHMonitor func() error) error {
	var cleanupErr error

	if err := uninstallSSHMonitor(); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("uninstall SSH monitor failed: %w", err))
	}

	// SSH 清理失败不应阻止配置、ID、日志和历史缓存的删除。
	if err := removeAgentData(cfgPath); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove agent data failed: %w", err))
	}

	return cleanupErr
}

func removeAgentData(cfgPath string) error {
	if cfgPath == "" {
		cfgPath = config.GetDefaultConfigPath()
	}

	dataDir := filepath.Clean(config.GetDataDir())
	cleanCfgPath := filepath.Clean(cfgPath)

	// 自定义配置可能位于数据目录之外，只删除配置文件本身。
	if cleanCfgPath != dataDir && !strings.HasPrefix(cleanCfgPath, dataDir+string(os.PathSeparator)) {
		if err := os.Remove(cleanCfgPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove config file %s: %w", cleanCfgPath, err)
		}
	}

	if err := os.RemoveAll(dataDir); err != nil {
		return fmt.Errorf("remove data directory %s: %w", dataDir, err)
	}

	return nil
}
