package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/dushixiang/pika/pkg/agent/utils"
	"gopkg.in/yaml.v3"
)

// Config Agent 配置
type Config struct {
	// 配置文件路径
	Path string `yaml:"-"`

	// 服务器配置
	Server ServerConfig `yaml:"server"`

	// Agent 配置
	Agent AgentConfig `yaml:"agent"`

	// 采集配置
	Collector CollectorConfig `yaml:"collector"`

	// 自动更新配置
	AutoUpdate AutoUpdateConfig `yaml:"auto_update"`
}

// ServerConfig 服务器配置
type ServerConfig struct {
	// 服务器地址（如：http://localhost:18888 或 https://your-server.com）
	Endpoint string `yaml:"endpoint"`

	// API Key
	APIKey string `yaml:"api_key"`

	// 是否跳过 TLS 证书验证（仅用于测试环境，生产环境不建议开启）
	InsecureSkipVerify bool `yaml:"insecure_skip_verify"`
}

// AgentConfig Agent 配置
type AgentConfig struct {
	// Agent 名称（默认使用主机名）
	Name string `yaml:"name"`

	// 日志等级（debug, info, warn, error，默认为 info）
	LogLevel string `yaml:"log_level"`

	// 日志文件路径（默认为空，输出到控制台）
	LogFile string `yaml:"log_file"`

	// 日志文件最大大小（MB，默认 100MB）
	LogMaxSize int `yaml:"log_max_size"`

	// 日志文件最大备份数（默认 3）
	LogMaxBackups int `yaml:"log_max_backups"`

	// 日志文件最大保留天数（默认 28 天）
	LogMaxAge int `yaml:"log_max_age"`

	// 是否压缩旧日志文件（默认 true）
	LogCompress bool `yaml:"log_compress"`
}

// CollectorConfig 采集器配置
type CollectorConfig struct {
	// 网络采集包含的网卡列表（白名单，支持正则表达式）
	// 如果配置了此项，则只采集匹配的网卡，忽略 NetworkExclude
	// 例如: ["^eth0$", "^en0$", "^ens.*"]
	NetworkInclude []string `yaml:"network_include"`

	// 网络采集排除的网卡列表（黑名单，支持正则表达式）
	// 仅当 NetworkInclude 为空时生效
	// 如果为空，使用默认排除规则（虚拟网卡、回环地址等）
	NetworkExclude []string `yaml:"network_exclude"`

	// 磁盘采集包含的挂载点列表（白名单）
	// 如果为空，默认采集系统主分区（Linux/macOS: "/"，Windows: "C:\"）
	// 例如:
	//   Linux/macOS: ["/", "/data", "/home"]
	//   Windows: ["C:", "D:"]
	DiskInclude []string `yaml:"disk_include"`
}

// AutoUpdateConfig 自动更新配置
type AutoUpdateConfig struct {
	// 是否启用自动更新
	Enabled bool `yaml:"enabled"`

	// 检查更新间隔
	CheckInterval string `yaml:"check_interval"`
}

// DefaultConfig 返回默认配置
func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Endpoint:           "http://localhost:8080",
			APIKey:             "",
			InsecureSkipVerify: false,
		},
		Agent: AgentConfig{
			Name:          "",
			LogLevel:      "info",
			LogFile:       "",
			LogMaxSize:    100,
			LogMaxBackups: 3,
			LogMaxAge:     28,
			LogCompress:   true,
		},
		Collector: CollectorConfig{},
		AutoUpdate: AutoUpdateConfig{
			Enabled:       true,
			CheckInterval: "10m",
		},
	}
}

// GetDefaultConfigPath 获取默认配置文件路径
func GetDefaultConfigPath() string {
	return filepath.Join(GetDataDir(), "agent.yaml")
}

// GetDataDir 获取 Agent 数据目录
func GetDataDir() string {
	return filepath.Join(utils.GetSafeHomeDir(), ".pika")
}

// Load 加载配置文件
func Load(path string) (*Config, error) {
	// 如果路径为空，使用默认路径
	if path == "" {
		path = GetDefaultConfigPath()
	}

	// 读取配置文件
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// 配置文件不存在，创建默认配置
			cfg := DefaultConfig()
			if err := cfg.Save(path); err != nil {
				return nil, fmt.Errorf("创建默认配置文件失败: %w", err)
			}
			return cfg, nil
		}
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	// 解析配置
	cfg := DefaultConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	// 验证配置
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("配置验证失败: %w", err)
	}

	cfg.Path = path
	return cfg, nil
}

// Save 保存配置到文件
func (c *Config) Save(path string) error {
	// 如果路径为空，使用默认路径
	if path == "" {
		path = GetDefaultConfigPath()
	}

	// 确保目录存在
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}

	// 序列化配置
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}

	// 写入文件
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("写入配置文件失败: %w", err)
	}

	return nil
}

// Validate 验证配置
func (c *Config) Validate() error {
	if c.AutoUpdate.Enabled {
		if _, err := time.ParseDuration(c.AutoUpdate.CheckInterval); err != nil {
			return fmt.Errorf("更新检查间隔格式错误: %w", err)
		}
	}

	// 验证日志等级
	if c.Agent.LogLevel == "" {
		c.Agent.LogLevel = "info"
	}
	validLogLevels := map[string]bool{
		"debug": true,
		"info":  true,
		"warn":  true,
		"error": true,
	}
	if !validLogLevels[c.Agent.LogLevel] {
		return fmt.Errorf("无效的日志等级: %s (可选值: debug, info, warn, error)", c.Agent.LogLevel)
	}

	return nil
}

// GetUpdateCheckInterval 获取更新检查间隔时长
func (c *Config) GetUpdateCheckInterval() time.Duration {
	duration, _ := time.ParseDuration(c.AutoUpdate.CheckInterval)
	return duration
}

// GetWebSocketURL 获取 WebSocket 连接地址
func (c *Config) GetWebSocketURL() string {
	u, err := url.Parse(c.Server.Endpoint)
	if err != nil {
		// 解析失败时，使用默认的 ws:// 协议
		return "ws://" + c.Server.Endpoint + "/ws/agent"
	}

	// 根据 HTTP 协议转换为对应的 WebSocket 协议
	scheme := "ws"
	if u.Scheme == "https" {
		scheme = "wss"
	}

	return fmt.Sprintf("%s://%s/ws/agent", scheme, u.Host)
}

// GetLatestVersionURL 获取更新检查地址
func (c *Config) GetLatestVersionURL() string {
	return c.Endpoint() + "/api/agent/version"
}

func (c *Config) GetDownloadURL() string {
	var filename = fmt.Sprintf("agent-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		filename += ".exe"
	}
	return c.Endpoint() + "/api/agent/downloads/" + filename + "?key=" + c.Server.APIKey
}

func (c *Config) Endpoint() string {
	u, err := url.Parse(c.Server.Endpoint)
	if err != nil {
		return c.Server.Endpoint
	}
	var endpoint = fmt.Sprintf("%s://%s", u.Scheme, u.Host)
	return endpoint
}

func DefaultNetworkExcludePatterns() []string {
	return []string{
		// 回环地址
		"^lo$",
		"^lo0$",
		// Linux 虚拟接口
		"^docker.*",  // Docker 网卡
		"^veth.*",    // 虚拟以太网接口
		"^br-.*",     // 网桥接口
		"^virbr.*",   // KVM/libvirt 网桥
		"^flannel.*", // Kubernetes Flannel
		"^cni.*",     // Container Network Interface
		"^tap.*",     // PVE/KVM 虚拟机 TAP 网卡
		// macOS 虚拟接口
		"^anpi\\d+$",   // Apple Network Process Interface
		"^ap\\d+$",     // Apple Wireless Access Point
		"^awdl\\d+$",   // Apple Wireless Direct Link (AirDrop)
		"^llw\\d+$",    // Low Latency WLAN
		"^bridge\\d+$", // 桥接网络
		"^gif\\d+$",    // Generic Tunnel Interface
		"^stf\\d+$",    // 6to4 tunnel interface
		"^utun\\d+$",   // User Tunnel (VPN)
		"^vmenet\\d+$", // 虚拟机网络 (VMware/Parallels)
		"^vmnet.*",     // 虚拟机网络 (VMware/Parallels/Fusion)
		"^pktap\\d+$",  // Packet capture interface
		"^ipsec\\d+$",  // IPSec interface
		"^feth\\d+$",   // Fake ethernet interface
		// Windows 虚拟接口
		"^Loopback.*",
		"^vEthernet.*",
		".*[Tt]eredo.*", // Teredo 隧道伪接口 (IPv6 over IPv4)
	}
}

// GetNetworkIncludePatterns 获取网络包含的正则表达式列表（白名单）
func (c *Config) GetNetworkIncludePatterns() ([]*regexp.Regexp, error) {
	patterns := c.Collector.NetworkInclude
	if len(patterns) == 0 {
		return nil, nil
	}

	var regexps []*regexp.Regexp
	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("编译网络包含规则 '%s' 失败: %w", pattern, err)
		}
		regexps = append(regexps, re)
	}

	return regexps, nil
}

// GetNetworkExcludePatterns 获取网络排除的正则表达式列表
// 如果配置为空，返回默认排除规则（回环地址和常见虚拟网卡）
func (c *Config) GetNetworkExcludePatterns() ([]*regexp.Regexp, error) {
	patterns := c.Collector.NetworkExclude

	// 如果没有配置，使用默认排除规则
	if len(patterns) == 0 {
		patterns = DefaultNetworkExcludePatterns()
	}

	var regexps []*regexp.Regexp
	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("编译网络排除规则 '%s' 失败: %w", pattern, err)
		}
		regexps = append(regexps, re)
	}

	return regexps, nil
}

// ShouldExcludeNetworkInterface 检查网卡是否应该被排除
// 逻辑：
// 1. 如果配置了 NetworkInclude（白名单），则只保留匹配白名单的网卡
// 2. 如果没有配置 NetworkInclude，则使用 NetworkExclude（黑名单）规则
func (c *Config) ShouldExcludeNetworkInterface(interfaceName string) bool {
	// 优先检查白名单
	includePatterns, err := c.GetNetworkIncludePatterns()
	if err != nil {
		// 白名单编译失败，记录错误但继续使用黑名单逻辑
		// 这里可以考虑记录日志
	} else if len(includePatterns) > 0 {
		// 配置了白名单，检查是否匹配
		for _, pattern := range includePatterns {
			if pattern.MatchString(interfaceName) {
				// 匹配白名单，不排除
				return false
			}
		}
		// 不在白名单中，排除
		return true
	}

	// 没有配置白名单，使用黑名单逻辑
	excludePatterns, err := c.GetNetworkExcludePatterns()
	if err != nil {
		// 如果正则编译失败，使用默认规则
		return interfaceName == "lo" || interfaceName == "lo0"
	}

	for _, pattern := range excludePatterns {
		if pattern.MatchString(interfaceName) {
			return true
		}
	}

	return false
}

// GetDiskInclude 获取磁盘包含的挂载点列表（白名单）
// 返回 nil 表示未配置白名单，采集所有挂载点
func (c *Config) GetDiskInclude() []string {
	if len(c.Collector.DiskInclude) == 0 {
		return nil
	}
	return c.Collector.DiskInclude
}

// ShouldIncludeDiskMountPoint 检查挂载点是否应该被采集
// 未配置白名单时采集所有挂载点
func (c *Config) ShouldIncludeDiskMountPoint(mountPoint string) bool {
	includeMounts := c.GetDiskInclude()
	if len(includeMounts) == 0 {
		return true // 未配置白名单，采集所有
	}
	for _, mount := range includeMounts {
		if mountPoint == mount {
			return true
		}
	}
	return false
}
