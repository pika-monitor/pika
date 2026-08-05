package protocol

import "encoding/json"

// InputMessage WebSocket消息结构（主要用于接收）
type InputMessage struct {
	Type MessageType     `json:"type"`
	Data json.RawMessage `json:"data"`
}

// OutboundMessage WebSocket 出站消息结构
type OutboundMessage struct {
	Type MessageType `json:"type"`
	Data interface{} `json:"data"`
}

// RegisterRequest 注册请求
type RegisterRequest struct {
	AgentInfo AgentInfo `json:"agentInfo"`
	ApiKey    string    `json:"apiKey"`
}

// RegisterResponse 注册响应
type RegisterResponse struct {
	AgentID string `json:"agentId"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// AgentInfo 探针信息
type AgentInfo struct {
	ID       string `json:"id"`       // 探针唯一标识（持久化）
	Name     string `json:"name"`     // 探针名称
	Hostname string `json:"hostname"` // 主机名
	OS       string `json:"os"`       // 操作系统
	Arch     string `json:"arch"`     // 架构
	Version  string `json:"version"`  // 版本号
}

// MetricSample 单条指标采样
type MetricSample struct {
	Type      MetricType  `json:"type"`
	Data      interface{} `json:"data"`
	Timestamp int64       `json:"timestamp,omitempty"` // 客户端采集时间(毫秒)
}

// MetricsBatch 一次采集周期内的批量指标，是 metrics 上报的唯一载体
type MetricsBatch struct {
	Samples []MetricSample `json:"samples"`
}

type MessageType string

// 控制消息
const (
	MessageTypeRegister    MessageType = "register"
	MessageTypeRegisterAck MessageType = "register_ack"
	MessageTypeRegisterErr MessageType = "register_error"
	MessageTypeCommand     MessageType = "command"
	MessageTypeCommandResp MessageType = "command_response"
	MessageTypeCommandAck  MessageType = "command_response_ack"
	MessageTypeUninstall   MessageType = "uninstall"
	// 指标消息（批量）
	MessageTypeMetrics       MessageType = "metrics"
	MessageTypeMonitorConfig MessageType = "monitor_config"
	// 防篡改消息
	MessageTypeTamperProtect MessageType = "tamper_protect"
	MessageTypeTamperEvent   MessageType = "tamper_event"
	// DDNS 消息
	MessageTypeDDNSConfig   MessageType = "ddns_config"
	MessageTypeDDNSIPReport MessageType = "ddns_ip_report"
	// 公网 IP 采集消息
	MessageTypePublicIPConfig MessageType = "public_ip_config"
	MessageTypePublicIPReport MessageType = "public_ip_report"
	// SSH 登录监控消息
	MessageTypeSSHLoginConfig       MessageType = "ssh_login_config"
	MessageTypeSSHLoginConfigResult MessageType = "ssh_login_config_result" // Agent 反馈配置应用结果
	MessageTypeSSHLoginEvent        MessageType = "ssh_login_event"
)

type MetricType string

// 消息类型常量
const (
	MetricTypeCPU               MetricType = "cpu"
	MetricTypeMemory            MetricType = "memory"
	MetricTypeDisk              MetricType = "disk"
	MetricTypeDiskIO            MetricType = "disk_io"
	MetricTypeNetwork           MetricType = "network"
	MetricTypeNetworkConnection MetricType = "network_connection"
	MetricTypeHost              MetricType = "host"
	MetricTypeGPU               MetricType = "gpu"
	MetricTypeTemperature       MetricType = "temperature"
	MetricTypeMonitor           MetricType = "monitor"
)

// CPUData CPU数据
type CPUData struct {
	// 静态信息(不常变化,但每次都发送)
	LogicalCores  int    `json:"logicalCores"`
	PhysicalCores int    `json:"physicalCores"`
	ModelName     string `json:"modelName"`
	// 动态信息
	UsagePercent float64   `json:"usagePercent"`
	PerCore      []float64 `json:"perCore,omitempty"`
}

// MemoryData 内存数据
type MemoryData struct {
	Total        uint64  `json:"total"`
	Used         uint64  `json:"used"`
	Free         uint64  `json:"free"`
	Available    uint64  `json:"available"`
	UsagePercent float64 `json:"usagePercent"`
	Cached       uint64  `json:"cached,omitempty"`
	Buffers      uint64  `json:"buffers,omitempty"`
	SwapTotal    uint64  `json:"swapTotal,omitempty"`
	SwapUsed     uint64  `json:"swapUsed,omitempty"`
	SwapFree     uint64  `json:"swapFree,omitempty"`
}

// DiskData 磁盘数据
type DiskData struct {
	MountPoint   string  `json:"mountPoint"`
	Device       string  `json:"device"`
	Fstype       string  `json:"fstype"`
	Total        uint64  `json:"total"`
	Used         uint64  `json:"used"`
	Free         uint64  `json:"free"`
	UsagePercent float64 `json:"usagePercent"`
}

// DiskIOData 磁盘IO数据
type DiskIOData struct {
	Device         string `json:"device"`
	ReadCount      uint64 `json:"readCount"`
	WriteCount     uint64 `json:"writeCount"`
	ReadBytes      uint64 `json:"readBytes"`
	WriteBytes     uint64 `json:"writeBytes"`
	ReadBytesRate  uint64 `json:"readBytesRate"`  // 读取速率(字节/秒)
	WriteBytesRate uint64 `json:"writeBytesRate"` // 写入速率(字节/秒)
	ReadTime       uint64 `json:"readTime"`
	WriteTime      uint64 `json:"writeTime"`
	IoTime         uint64 `json:"ioTime"`
	IopsInProgress uint64 `json:"iopsInProgress"`
}

// NetworkData 网络数据
type NetworkData struct {
	Interface      string   `json:"interface"`
	MacAddress     string   `json:"macAddress,omitempty"`
	Addrs          []string `json:"addrs,omitempty"`
	BytesSentRate  uint64   `json:"bytesSentRate"`  // 发送速率(字节/秒)
	BytesRecvRate  uint64   `json:"bytesRecvRate"`  // 接收速率(字节/秒)
	BytesSentTotal uint64   `json:"bytesSentTotal"` // 累计发送字节数
	BytesRecvTotal uint64   `json:"bytesRecvTotal"` // 累计接收字节数
}

// NetworkConnectionData 网络连接统计数据
type NetworkConnectionData struct {
	Established uint32 `json:"established"` // ESTABLISHED 状态连接数
	SynSent     uint32 `json:"synSent"`     // SYN_SENT 状态连接数
	SynRecv     uint32 `json:"synRecv"`     // SYN_RECV 状态连接数
	FinWait1    uint32 `json:"finWait1"`    // FIN_WAIT1 状态连接数
	FinWait2    uint32 `json:"finWait2"`    // FIN_WAIT2 状态连接数
	TimeWait    uint32 `json:"timeWait"`    // TIME_WAIT 状态连接数
	Close       uint32 `json:"close"`       // CLOSE 状态连接数
	CloseWait   uint32 `json:"closeWait"`   // CLOSE_WAIT 状态连接数
	LastAck     uint32 `json:"lastAck"`     // LAST_ACK 状态连接数
	Listen      uint32 `json:"listen"`      // LISTEN 状态连接数
	Closing     uint32 `json:"closing"`     // CLOSING 状态连接数
	Total       uint32 `json:"total"`       // 总连接数
}

// LoadData 系统负载数据
type LoadData struct {
	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`
}

// HostInfoData 主机信息
type HostInfoData struct {
	Hostname             string  `json:"hostname"`
	Uptime               uint64  `json:"uptime"`
	BootTime             uint64  `json:"bootTime"`
	Procs                uint64  `json:"procs"`
	Load1                float64 `json:"load1"`
	Load5                float64 `json:"load5"`
	Load15               float64 `json:"load15"`
	OS                   string  `json:"os"`
	Platform             string  `json:"platform"`
	PlatformFamily       string  `json:"platformFamily"`
	PlatformVersion      string  `json:"platformVersion"`
	KernelVersion        string  `json:"kernelVersion"`
	KernelArch           string  `json:"kernelArch"`
	VirtualizationSystem string  `json:"virtualizationSystem,omitempty"`
	VirtualizationRole   string  `json:"virtualizationRole,omitempty"`
}

// GPUData GPU数据
type GPUData struct {
	Index       int     `json:"index"`
	Name        string  `json:"name"`
	UUID        string  `json:"uuid,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
	Utilization float64 `json:"utilization,omitempty"`
	MemoryTotal uint64  `json:"memoryTotal,omitempty"`
	MemoryUsed  uint64  `json:"memoryUsed,omitempty"`
	MemoryFree  uint64  `json:"memoryFree,omitempty"`
	PowerUsage  float64 `json:"powerUsage,omitempty"`
	FanSpeed    float64 `json:"fanSpeed,omitempty"`
}

// TemperatureData 温度数据
type TemperatureData struct {
	SensorKey   string  `json:"sensorKey"`
	Temperature float64 `json:"temperature"`
	Type        string  `json:"type"`
}

// CommandRequest 指令请求
type CommandRequest struct {
	ID             string `json:"id"`   // 指令ID
	Type           string `json:"type"` // 指令类型: vps_audit/shell_exec/shell_cancel
	Args           string `json:"args,omitempty"`
	Command        string `json:"command,omitempty"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
}

// CommandResponse 指令响应
type CommandResponse struct {
	ID        string `json:"id"`                 // 指令ID
	Type      string `json:"type"`               // 指令类型
	Status    string `json:"status"`             // running/success/error/cancelled
	Error     string `json:"error,omitempty"`    // 错误信息
	Result    string `json:"result,omitempty"`   // 结果数据(JSON字符串)
	Output    string `json:"output,omitempty"`   // 命令增量输出
	Stream    string `json:"stream,omitempty"`   // stdout/stderr
	ExitCode  *int   `json:"exitCode,omitempty"` // 进程退出码
	Truncated bool   `json:"truncated,omitempty"`
}

type CommandResponseAck struct {
	ID string `json:"id"`
}

// VPSAuditResult VPS资产采集结果(Agent端只负责采集,不做安全判断)
type VPSAuditResult struct {
	// 系统信息
	SystemInfo SystemInfo `json:"systemInfo"`
	// 【核心】资产清单(Agent收集的原始数据)
	AssetInventory AssetInventory `json:"assetInventory"`
	// 统计摘要
	Statistics AuditStatistics `json:"statistics"`
	// 采集开始时间
	StartTime int64 `json:"startTime"`
	// 采集结束时间
	EndTime int64 `json:"endTime"`
	// 采集警告（权限不足、命令失败等问题）
	CollectWarnings []string `json:"collectWarnings,omitempty"`
}

// VPSAuditAnalysis VPS安全分析结果(Server端分析后的结果)
type VPSAuditAnalysis struct {
	// 关联的审计ID
	AuditID string `json:"auditId"`
	// 安全检查结果
	SecurityChecks []SecurityCheck `json:"securityChecks"`
	// 风险评分 (0-100)
	RiskScore int `json:"riskScore"`
	// 威胁等级: low/medium/high/critical
	ThreatLevel string `json:"threatLevel"`
	// 修复建议
	Recommendations []string `json:"recommendations,omitempty"`
	// 分析时间
	AnalyzedAt int64 `json:"analyzedAt"`
}

// SystemInfo 系统信息
type SystemInfo struct {
	Hostname      string `json:"hostname"`
	OS            string `json:"os"`
	KernelVersion string `json:"kernelVersion"`
	Uptime        uint64 `json:"uptime"`
	PublicIP      string `json:"publicIP,omitempty"`
}

// SecurityCheck 安全检查项
type SecurityCheck struct {
	Category string             `json:"category"` // 检查类别
	Status   string             `json:"status"`   // pass/fail/warn/skip
	Message  string             `json:"message"`  // 检查消息
	Details  []SecurityCheckSub `json:"details,omitempty"`
}

// SecurityCheckSub 安全检查子项
type SecurityCheckSub struct {
	Name     string `json:"name"`               // 子检查名称
	Status   string `json:"status"`             // pass/fail/warn/skip
	Severity string `json:"severity,omitempty"` // 严重程度: high/medium/low
	Message  string `json:"message"`            // 检查消息
	Evidence string `json:"evidence,omitempty"` // 证据信息(简化为字符串)
}

// Evidence 安全事件证据
type Evidence struct {
	FileHash    string   `json:"fileHash,omitempty"`    // 文件SHA256哈希
	ProcessTree []string `json:"processTree,omitempty"` // 进程树
	FilePath    string   `json:"filePath,omitempty"`    // 文件路径
	Timestamp   int64    `json:"timestamp,omitempty"`   // 时间戳(毫秒)
	NetworkConn string   `json:"networkConn,omitempty"` // 网络连接信息
	RiskLevel   string   `json:"riskLevel,omitempty"`   // 风险等级: low/medium/high
}

// MonitorData 监控数据
type MonitorData struct {
	AgentId      string `json:"agentId"`                // 探针 ID
	AgentName    string `json:"agentName"`              // 探针名称
	MonitorId    string `json:"monitorId"`              // 监控项ID
	MonitorName  string `json:"monitorName,omitempty"`  // 监控项名称
	Type         string `json:"type"`                   // 监控类型: http, tcp
	Target       string `json:"target,omitempty"`       // 监控目标
	Status       string `json:"status"`                 // 状态: up, down
	StatusCode   int    `json:"statusCode,omitempty"`   // HTTP 状态码
	ResponseTime int64  `json:"responseTime"`           // 响应时间(毫秒)
	Error        string `json:"error,omitempty"`        // 错误信息
	CheckedAt    int64  `json:"checkedAt"`              // 检测时间(毫秒时间戳)
	Message      string `json:"message,omitempty"`      // 附加信息
	ContentMatch bool   `json:"contentMatch,omitempty"` // 内容匹配结果
	// TLS 证书信息（仅用于 HTTPS）
	CertExpiryTime int64 `json:"certExpiryTime,omitempty"` // 证书过期时间(毫秒时间戳)
	CertDaysLeft   int   `json:"certDaysLeft,omitempty"`   // 证书剩余天数
}

// TamperProtectConfig 防篡改保护配置（增量更新）
type TamperProtectConfig struct {
	Added   []string `json:"added,omitempty"`   // 新增保护的目录
	Removed []string `json:"removed,omitempty"` // 移除保护的目录
}

// TamperProtectResponse 防篡改保护响应
type TamperProtectResponse struct {
	Success bool     `json:"success"`           // 是否成功
	Message string   `json:"message"`           // 响应消息
	Paths   []string `json:"paths"`             // 当前保护的目录列表
	Added   []string `json:"added,omitempty"`   // 新增的目录
	Removed []string `json:"removed,omitempty"` // 移除的目录
}

// TamperEventData 防篡改事件数据
type TamperEventData struct {
	Path      string `json:"path"`               // 被修改的路径
	Operation string `json:"operation"`          // 操作类型: write, remove, rename, chmod, create, attr_tamper
	Timestamp int64  `json:"timestamp"`          // 事件时间(毫秒)
	Details   string `json:"details"`            // 详细信息
	Restored  bool   `json:"restored,omitempty"` // 是否已自动恢复（仅用于 attr_tamper 操作）
}

// TamperAlertData 防篡改属性告警数据
type TamperAlertData struct {
	Path      string `json:"path"`      // 被篡改的路径
	Timestamp int64  `json:"timestamp"` // 检测时间(毫秒)
	Details   string `json:"details"`   // 详细信息(如: "不可变属性被移除")
	Restored  bool   `json:"restored"`  // 是否已自动恢复
}

// ==================== 资产清单相关数据结构 ====================

// AssetInventory 资产清单
type AssetInventory struct {
	NetworkAssets *NetworkAssets `json:"networkAssets,omitempty"` // 网络资产
	ProcessAssets *ProcessAssets `json:"processAssets,omitempty"` // 进程资产
	UserAssets    *UserAssets    `json:"userAssets,omitempty"`    // 用户资产
	FileAssets    *FileAssets    `json:"fileAssets,omitempty"`    // 文件资产
	KernelAssets  *KernelAssets  `json:"kernelAssets,omitempty"`  // 内核资产
	LoginAssets   *LoginAssets   `json:"loginAssets,omitempty"`   // 登录资产
}

// AuditStatistics 审计统计摘要
type AuditStatistics struct {
	NetworkStats *NetworkStatistics `json:"networkStats,omitempty"` // 网络统计
	ProcessStats *ProcessStatistics `json:"processStats,omitempty"` // 进程统计
	UserStats    *UserStatistics    `json:"userStats,omitempty"`    // 用户统计
	FileStats    *FileStatistics    `json:"fileStats,omitempty"`    // 文件统计
	LoginStats   *LoginStatistics   `json:"loginStats,omitempty"`   // 登录统计
}

// ==================== 网络资产 ====================

// NetworkAssets 网络资产
type NetworkAssets struct {
	ListeningPorts []ListeningPort     `json:"listeningPorts,omitempty"` // 监听端口
	Connections    []NetworkConnection `json:"connections,omitempty"`    // 网络连接
	Interfaces     []NetworkInterface  `json:"interfaces,omitempty"`     // 网卡接口
	RoutingTable   []RouteEntry        `json:"routingTable,omitempty"`   // 路由表
	FirewallRules  *FirewallInfo       `json:"firewallRules,omitempty"`  // 防火墙规则
	DNSServers     []string            `json:"dnsServers,omitempty"`     // DNS服务器
	ARPTable       []ARPEntry          `json:"arpTable,omitempty"`       // ARP表
	Statistics     *NetworkStatistics  `json:"statistics,omitempty"`     // 统计信息
}

// ListeningPort 监听端口
type ListeningPort struct {
	Protocol    string `json:"protocol"`              // tcp/udp
	Address     string `json:"address"`               // 0.0.0.0/127.0.0.1/::
	Port        uint32 `json:"port"`                  // 端口号
	ProcessPID  int32  `json:"processPid"`            // 进程PID
	ProcessName string `json:"processName,omitempty"` // 进程名
	ProcessPath string `json:"processPath,omitempty"` // 进程路径
	IsPublic    bool   `json:"isPublic"`              // 是否公网监听
}

// NetworkConnection 网络连接
type NetworkConnection struct {
	Protocol    string `json:"protocol"`              // tcp/udp
	LocalAddr   string `json:"localAddr"`             // 本地地址
	LocalPort   uint32 `json:"localPort"`             // 本地端口
	RemoteAddr  string `json:"remoteAddr"`            // 远程地址
	RemotePort  uint32 `json:"remotePort"`            // 远程端口
	State       string `json:"state"`                 // ESTABLISHED/LISTEN/...
	ProcessPID  int32  `json:"processPid"`            // 进程PID
	ProcessName string `json:"processName,omitempty"` // 进程名
}

// NetworkInterface 网卡接口
type NetworkInterface struct {
	Name       string   `json:"name"`                // 接口名
	MacAddress string   `json:"macAddress"`          // MAC地址
	Addresses  []string `json:"addresses,omitempty"` // IP地址列表
	MTU        int      `json:"mtu"`                 // MTU
	IsUp       bool     `json:"isUp"`                // 是否启用
	Flags      []string `json:"flags,omitempty"`     // 标志
}

// RouteEntry 路由表条目
type RouteEntry struct {
	Destination string `json:"destination"` // 目标网络
	Gateway     string `json:"gateway"`     // 网关
	Genmask     string `json:"genmask"`     // 子网掩码
	Interface   string `json:"interface"`   // 接口
	Metric      int    `json:"metric"`      // 优先级
}

// FirewallInfo 防火墙信息
type FirewallInfo struct {
	Type   string         `json:"type"`            // iptables/ufw/firewalld
	Status string         `json:"status"`          // active/inactive
	Rules  []FirewallRule `json:"rules,omitempty"` // 规则列表
}

// FirewallRule 防火墙规则
type FirewallRule struct {
	Chain    string `json:"chain"`              // 链名
	Target   string `json:"target"`             // 目标动作
	Protocol string `json:"protocol,omitempty"` // 协议
	Source   string `json:"source,omitempty"`   // 源地址
	Dest     string `json:"dest,omitempty"`     // 目标地址
	Port     string `json:"port,omitempty"`     // 端口
}

// ARPEntry ARP表条目
type ARPEntry struct {
	IPAddress  string `json:"ipAddress"`  // IP地址
	MacAddress string `json:"macAddress"` // MAC地址
	Interface  string `json:"interface"`  // 接口
}

// NetworkStatistics 网络统计
type NetworkStatistics struct {
	TotalListeningPorts  int            `json:"totalListeningPorts"`          // 总监听端口数
	PublicListeningPorts int            `json:"publicListeningPorts"`         // 公网监听端口数
	ActiveConnections    int            `json:"activeConnections"`            // 活跃连接数
	ConnectionsByState   map[string]int `json:"connectionsByState,omitempty"` // 连接状态分布
	InterfaceCount       int            `json:"interfaceCount"`               // 网卡数量
}

// ==================== 进程资产 ====================

// ProcessAssets 进程资产
type ProcessAssets struct {
	RunningProcesses    []ProcessInfo      `json:"runningProcesses,omitempty"`    // 所有运行进程(可选)
	TopCPUProcesses     []ProcessInfo      `json:"topCpuProcesses,omitempty"`     // CPU占用TOP进程
	TopMemoryProcesses  []ProcessInfo      `json:"topMemoryProcesses,omitempty"`  // 内存占用TOP进程
	SuspiciousProcesses []ProcessInfo      `json:"suspiciousProcesses,omitempty"` // 可疑进程(如已删除exe)
	Statistics          *ProcessStatistics `json:"statistics,omitempty"`          // 统计信息
}

// ProcessInfo 进程信息
type ProcessInfo struct {
	PID        int32   `json:"pid"`                // 进程ID
	Name       string  `json:"name"`               // 进程名
	Cmdline    string  `json:"cmdline,omitempty"`  // 命令行
	Exe        string  `json:"exe,omitempty"`      // 可执行文件路径
	PPID       int32   `json:"ppid"`               // 父进程ID
	Username   string  `json:"username,omitempty"` // 用户名
	CPUPercent float64 `json:"cpuPercent"`         // CPU使用率
	MemPercent float32 `json:"memPercent"`         // 内存使用率
	MemoryMB   uint64  `json:"memoryMb"`           // 内存占用(MB)
	Status     string  `json:"status,omitempty"`   // 状态
	CreateTime int64   `json:"createTime"`         // 创建时间(毫秒)
	ExeDeleted bool    `json:"exeDeleted"`         // 可执行文件是否已删除
}

// ProcessStatistics 进程统计
type ProcessStatistics struct {
	TotalProcesses    int `json:"totalProcesses"`    // 进程总数
	RunningProcesses  int `json:"runningProcesses"`  // 运行中进程
	SleepingProcesses int `json:"sleepingProcesses"` // 睡眠进程
	ZombieProcesses   int `json:"zombieProcesses"`   // 僵尸进程
	ThreadCount       int `json:"threadCount"`       // 线程总数
}

// ==================== 用户资产 ====================

// UserAssets 用户资产
type UserAssets struct {
	SystemUsers   []UserInfo      `json:"systemUsers,omitempty"`   // 系统用户
	LoginHistory  []LoginRecord   `json:"loginHistory,omitempty"`  // 登录历史
	CurrentLogins []LoginSession  `json:"currentLogins,omitempty"` // 当前登录
	SSHKeys       []SSHKeyInfo    `json:"sshKeys,omitempty"`       // SSH密钥
	SudoUsers     []SudoUserInfo  `json:"sudoUsers,omitempty"`     // Sudo用户
	SSHConfig     *SSHConfig      `json:"sshConfig,omitempty"`     // SSH配置
	Statistics    *UserStatistics `json:"statistics,omitempty"`    // 统计信息
}

// UserInfo 用户信息
type UserInfo struct {
	Username    string `json:"username"`              // 用户名
	UID         string `json:"uid"`                   // UID
	GID         string `json:"gid"`                   // GID
	HomeDir     string `json:"homeDir,omitempty"`     // 家目录
	Shell       string `json:"shell,omitempty"`       // Shell
	IsLoginable bool   `json:"isLoginable"`           // 是否可登录
	IsRootEquiv bool   `json:"isRootEquiv,omitempty"` // 是否UID=0
	HasPassword bool   `json:"hasPassword"`           // 是否有密码
}

// LoginRecord 登录记录
type LoginRecord struct {
	Username  string `json:"username"`           // 用户名
	IP        string `json:"ip,omitempty"`       // IP地址
	Location  string `json:"location,omitempty"` // IP归属地
	Terminal  string `json:"terminal"`           // 终端
	Timestamp int64  `json:"timestamp"`          // 时间戳(毫秒)
	Status    string `json:"status,omitempty"`   // success/failed
}

// LoginSession 登录会话
type LoginSession struct {
	Username  string `json:"username"`           // 用户名
	Terminal  string `json:"terminal"`           // 终端
	IP        string `json:"ip"`                 // IP地址
	Location  string `json:"location,omitempty"` // IP归属地
	LoginTime int64  `json:"loginTime"`          // 登录时间(毫秒)
	IdleTime  int    `json:"idleTime"`           // 空闲时间(秒)
}

// SSHKeyInfo SSH密钥信息
type SSHKeyInfo struct {
	Username    string `json:"username"`            // 用户名
	KeyType     string `json:"keyType"`             // 密钥类型
	Fingerprint string `json:"fingerprint"`         // 指纹
	Comment     string `json:"comment,omitempty"`   // 注释
	FilePath    string `json:"filePath"`            // 文件路径
	AddedTime   int64  `json:"addedTime,omitempty"` // 添加时间(毫秒)
}

// SudoUserInfo Sudo用户信息
type SudoUserInfo struct {
	Username string `json:"username"`        // 用户名
	Rules    string `json:"rules,omitempty"` // 规则
	NoPasswd bool   `json:"noPasswd"`        // 是否免密
}

// SSHConfig SSH配置信息
type SSHConfig struct {
	Port                   int    `json:"port"`                          // SSH端口
	PermitRootLogin        string `json:"permitRootLogin"`               // 是否允许root登录 (yes/no/prohibit-password)
	PasswordAuthentication bool   `json:"passwordAuthentication"`        // 是否允许密码认证
	PubkeyAuthentication   bool   `json:"pubkeyAuthentication"`          // 是否允许公钥认证
	PermitEmptyPasswords   bool   `json:"permitEmptyPasswords"`          // 是否允许空密码
	Protocol               string `json:"protocol,omitempty"`            // 协议版本
	MaxAuthTries           int    `json:"maxAuthTries,omitempty"`        // 最大认证尝试次数
	ClientAliveInterval    int    `json:"clientAliveInterval,omitempty"` // 客户端保活间隔
	ClientAliveCountMax    int    `json:"clientAliveCountMax,omitempty"` // 客户端保活最大次数
	X11Forwarding          bool   `json:"x11Forwarding,omitempty"`       // 是否允许X11转发
	UsePAM                 bool   `json:"usePAM,omitempty"`              // 是否使用PAM
	ConfigFilePath         string `json:"configFilePath,omitempty"`      // 配置文件路径
}

// UserStatistics 用户统计
type UserStatistics struct {
	TotalUsers          int `json:"totalUsers"`          // 用户总数
	LoginableUsers      int `json:"loginableUsers"`      // 可登录用户数
	RootEquivalentUsers int `json:"rootEquivalentUsers"` // Root权限用户数
	RecentLoginCount    int `json:"recentLoginCount"`    // 近期登录次数
	FailedLoginCount    int `json:"failedLoginCount"`    // 失败登录次数
}

// ==================== 文件资产 ====================

// FileAssets 文件资产
type FileAssets struct {
	CronJobs        []CronJob        `json:"cronJobs,omitempty"`        // 定时任务
	SystemdServices []SystemdService `json:"systemdServices,omitempty"` // Systemd服务
	StartupScripts  []StartupScript  `json:"startupScripts,omitempty"`  // 启动脚本
	RecentModified  []FileInfo       `json:"recentModified,omitempty"`  // 最近修改文件
	LargeFiles      []FileInfo       `json:"largeFiles,omitempty"`      // 大文件
	TmpExecutables  []FileInfo       `json:"tmpExecutables,omitempty"`  // 临时目录可执行文件
	Statistics      *FileStatistics  `json:"statistics,omitempty"`      // 统计信息
}

// CronJob 定时任务
type CronJob struct {
	User     string `json:"user"`               // 用户
	Schedule string `json:"schedule"`           // 计划
	Command  string `json:"command"`            // 命令
	FilePath string `json:"filePath,omitempty"` // 文件路径
}

// SystemdService Systemd服务
type SystemdService struct {
	Name        string `json:"name"`                  // 服务名
	State       string `json:"state,omitempty"`       // 状态
	Enabled     bool   `json:"enabled"`               // 是否开机启动
	ExecStart   string `json:"execStart,omitempty"`   // 启动命令
	Description string `json:"description,omitempty"` // 描述
	UnitFile    string `json:"unitFile,omitempty"`    // Unit文件路径
}

// StartupScript 启动脚本
type StartupScript struct {
	Type    string `json:"type"`    // init.d/rc.local/systemd
	Path    string `json:"path"`    // 路径
	Name    string `json:"name"`    // 名称
	Enabled bool   `json:"enabled"` // 是否启用
}

// FileInfo 文件信息
type FileInfo struct {
	Path         string `json:"path"`                  // 路径
	Size         int64  `json:"size"`                  // 大小(字节)
	ModTime      int64  `json:"modTime"`               // 修改时间(毫秒)
	Permissions  string `json:"permissions,omitempty"` // 权限
	Owner        string `json:"owner,omitempty"`       // 所有者
	Group        string `json:"group,omitempty"`       // 组
	IsExecutable bool   `json:"isExecutable"`          // 是否可执行
}

// FileStatistics 文件统计
type FileStatistics struct {
	CronJobsCount        int `json:"cronJobsCount"`        // 定时任务数量
	SystemdServicesCount int `json:"systemdServicesCount"` // Systemd服务数量
	ActiveServicesCount  int `json:"activeServicesCount"`  // 活跃服务数量
	RecentFilesCount     int `json:"recentFilesCount"`     // 最近修改文件数量
	LargeFilesCount      int `json:"largeFilesCount"`      // 大文件数量
}

// ==================== 内核资产 ====================

// KernelAssets 内核资产
type KernelAssets struct {
	LoadedModules    []KernelModule      `json:"loadedModules,omitempty"`    // 已加载内核模块
	KernelParameters map[string]string   `json:"kernelParameters,omitempty"` // 内核参数
	SecurityModules  *SecurityModuleInfo `json:"securityModules,omitempty"`  // 安全模块
}

// KernelModule 内核模块
type KernelModule struct {
	Name   string `json:"name"`   // 模块名
	Size   int    `json:"size"`   // 大小
	UsedBy int    `json:"usedBy"` // 被引用次数
}

// SecurityModuleInfo 安全模块信息
type SecurityModuleInfo struct {
	SELinuxStatus   string `json:"selinuxStatus,omitempty"`   // SELinux状态
	AppArmorStatus  string `json:"apparmorStatus,omitempty"`  // AppArmor状态
	SecureBootState string `json:"secureBootState,omitempty"` // 安全启动状态
}

// ==================== 登录资产 ====================

// LoginAssets 登录资产
type LoginAssets struct {
	SuccessfulLogins []LoginRecord    `json:"successfulLogins,omitempty"` // 成功登录记录
	FailedLogins     []LoginRecord    `json:"failedLogins,omitempty"`     // 失败登录记录
	CurrentSessions  []LoginSession   `json:"currentSessions,omitempty"`  // 当前登录会话
	Statistics       *LoginStatistics `json:"statistics,omitempty"`       // 统计信息
}

// LoginStatistics 登录统计
type LoginStatistics struct {
	TotalLogins      int            `json:"totalLogins"`                // 总登录次数
	FailedLogins     int            `json:"failedLogins"`               // 失败登录次数
	CurrentSessions  int            `json:"currentSessions"`            // 当前会话数
	UniqueIPs        map[string]int `json:"uniqueIPs,omitempty"`        // 唯一IP统计
	UniqueUsers      map[string]int `json:"uniqueUsers,omitempty"`      // 唯一用户统计
	HighFrequencyIPs map[string]int `json:"highFrequencyIPs,omitempty"` // 高频IP (登录次数>10)
}

// ==================== SSH 登录监控相关数据结构 ====================

// SSHLoginConfig SSH登录监控配置
type SSHLoginConfig struct {
	Enabled bool `json:"enabled"` // 是否启用监控
}

// SSHLoginConfigResult SSH登录监控配置应用结果（Agent 反馈）
type SSHLoginConfigResult struct {
	Success bool   `json:"success"` // 配置应用是否成功
	Enabled bool   `json:"enabled"` // 当前启用状态
	Message string `json:"message"` // 结果描述信息
}

// SSHLoginEvent SSH登录事件
type SSHLoginEvent struct {
	Username  string `json:"username"`            // 登录用户名
	IP        string `json:"ip"`                  // 来源IP
	Port      string `json:"port,omitempty"`      // 来源端口
	Timestamp int64  `json:"timestamp"`           // 登录时间（毫秒时间戳）
	Status    string `json:"status"`              // success/failed
	TTY       string `json:"tty,omitempty"`       // 终端
	SessionID string `json:"sessionId,omitempty"` // 会话ID
}
