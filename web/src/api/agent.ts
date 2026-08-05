import {del, get, post, put} from './request';
import type {
    Agent,
    LatestMetrics,
    SSHLoginConfig,
    SSHLoginEvent,
    TrafficStats,
    UpdateSSHLoginConfigRequest,
    UpdateTrafficConfigRequest
} from '@/types';
import qs from "qs";

export type MetricsAggregation = 'avg' | 'max' | 'raw';

export interface GetAgentMetricsRequest {
    agentId: string;
    type: 'cpu' | 'memory' | 'disk' | 'network' | 'network_connection' | 'disk_io' | 'gpu' | 'temperature' | 'monitor';
    range?: string; // 时间范围，如 '15m', '1h', '1d' 等，从后端配置获取
    start?: number; // 自定义开始时间（毫秒时间戳）
    end?: number; // 自定义结束时间（毫秒时间戳）
    interface?: string; // 网卡过滤参数（仅对 network 类型有效）
    // aggregation 缺省时后端返回 avg+max 两条 series（band 模式）；显式传值锁定为单条
    aggregation?: MetricsAggregation;
}

// 新的统一数据格式
export interface MetricDataPoint {
    timestamp: number;
    value: number;
}

export interface MetricSeries {
    name: string;
    labels?: Record<string, string>;
    data: MetricDataPoint[];
}

export interface GetAgentMetricsResponse {
    agentId: string;
    type: string;
    range: string;
    series: MetricSeries[];
}

// 管理员接口 - 获取所有探针（需要认证）
export const listAgentsByAdmin = () => {
    return get<Agent[]>('/admin/agents');
};

export const listAgents = () => {
    return get<Agent[]>('/agents');
};

export const getAgent = (id: string) => {
    return get<Agent>(`/agents/${id}`);
};

// 管理员接口 - 获取探针详情（显示完整信息）
export const getAgentForAdmin = (id: string) => {
    return get<Agent>(`/admin/agents/${id}`);
};

export const getAgentLatestMetricsForAdmin = (agentId: string) => {
    return get<LatestMetrics>(`/admin/agents/${agentId}/metrics/latest`);
};

export const getAgentMetrics = (params: GetAgentMetricsRequest) => {
    const {agentId, type, range = '1h', start, end, interface: interfaceName, aggregation} = params;
    const query = new URLSearchParams();
    query.append('type', type);
    if (start !== undefined && end !== undefined) {
        query.append('start', start.toString());
        query.append('end', end.toString());
    } else {
        query.append('range', range);
    }
    if (interfaceName) {
        query.append('interface', interfaceName);
    }
    if (aggregation) {
        query.append('aggregation', aggregation);
    }
    return get<GetAgentMetricsResponse>(`/agents/${agentId}/metrics?${query.toString()}`);
};

export const getAgentLatestMetrics = (agentId: string) => {
    return get<LatestMetrics>(`/agents/${agentId}/metrics/latest`);
};

// 获取探针的可用网卡列表
export interface GetNetworkInterfacesResponse {
    interfaces: string[];
}

export const getAvailableNetworkInterfaces = (agentId: string) => {
    return get<GetNetworkInterfacesResponse>(`/agents/${agentId}/network-interfaces`);
};

export interface GetNetworkMetricsByInterfaceRequest {
    agentId: string;
    range?: '1m' | '5m' | '15m' | '30m' | '1h' | '3h' | '6h' | '12h' | '1d' | '24h' | '3d' | '7d' | '30d';
}

export interface NetworkMetricByInterface {
    timestamp: number;
    interface: string;
    maxSentRate: number;
    maxRecvRate: number;
}

export interface GetNetworkMetricsByInterfaceResponse {
    agentId: string;
    type: string;
    range: string;
    start: number;
    end: number;
    interval: number;
    metrics: NetworkMetricByInterface[];
}

export const getNetworkMetricsByInterface = (params: GetNetworkMetricsByInterfaceRequest) => {
    const {agentId, range = '1h'} = params;
    const query = new URLSearchParams();
    query.append('range', range);
    return get<GetNetworkMetricsByInterfaceResponse>(`/agents/${agentId}/metrics/network-by-interface?${query.toString()}`);
};

// VPS 安全审计相关接口

export interface SystemInfo {
    hostname: string;
    os: string;
    kernelVersion: string;
    uptime: number;
    publicIP?: string;
}

export interface SecurityCheckSub {
    name?: string;
    status: 'pass' | 'fail' | 'warn' | 'skip';
    severity?: 'high' | 'medium' | 'low';
    message: string;
    evidence?: string; // 简化为字符串
}

export interface SecurityCheck {
    category: string;
    status: 'pass' | 'fail' | 'warn' | 'skip';
    message: string;
    details?: SecurityCheckSub[];
}

// 资产清单相关类型
export interface ListeningPort {
    protocol: string;
    address: string;
    port: number;
    processName?: string;
    processPid?: number;
    processPath?: string;
    isPublic: boolean;
}

export interface NetworkConnection {
    protocol: string;
    localAddr: string;
    localPort: number;
    remoteAddr: string;
    remotePort: number;
    state: string;
    processPid?: number;
    processName?: string;
}

export interface NetworkInterface {
    name: string;
    macAddress: string;
    addresses?: string[];
    mtu: number;
    isUp: boolean;
    flags?: string[];
}

export interface RouteEntry {
    destination: string;
    gateway: string;
    genmask: string;
    interface: string;
    metric: number;
}

export interface FirewallRule {
    chain: string;
    target: string;
    protocol?: string;
    source?: string;
    dest?: string;
    port?: string;
}

export interface FirewallInfo {
    type: string;
    status: string;
    rules?: FirewallRule[];
}

export interface ARPEntry {
    ipAddress: string;
    macAddress: string;
    interface: string;
}

export interface NetworkAssets {
    listeningPorts?: ListeningPort[];
    connections?: NetworkConnection[];
    interfaces?: NetworkInterface[];
    routingTable?: RouteEntry[];
    firewallRules?: FirewallInfo;
    dnsServers?: string[];
    arpTable?: ARPEntry[];
}

export interface ProcessInfo {
    pid: number;
    name: string;
    cmdline?: string;
    exe?: string;
    username?: string;
    cpuPercent: number;
    memPercent: number;
    memoryMb: number;
    exeDeleted?: boolean;
}

export interface ProcessAssets {
    runningProcesses?: ProcessInfo[];
    topCpuProcesses?: ProcessInfo[];
    topMemoryProcesses?: ProcessInfo[];
    suspiciousProcesses?: ProcessInfo[];
}

export interface UserInfo {
    username: string;
    uid: string;
    gid: string;
    homeDir?: string;
    shell?: string;
    isLoginable: boolean;
    isRootEquiv: boolean;
    hasPassword: boolean;
}

export interface LoginRecord {
    username: string;
    ip?: string;
    location?: string;
    terminal: string;
    timestamp: number;
    status?: string;
}

export interface LoginSession {
    username: string;
    terminal: string;
    ip: string;
    location?: string;
    loginTime: number;
    idleTime: number;
}

export interface SSHKeyInfo {
    username: string;
    keyType: string;
    fingerprint: string;
    comment?: string;
    filePath: string;
    addedTime?: number;
}

export interface SudoUserInfo {
    username: string;
    rules?: string;
    noPasswd: boolean;
}

export interface SSHConfig {
    port: number;
    permitRootLogin: string;
    passwordAuthentication: boolean;
    pubkeyAuthentication: boolean;
    permitEmptyPasswords: boolean;
    protocol?: string;
    maxAuthTries?: number;
    clientAliveInterval?: number;
    clientAliveCountMax?: number;
    x11Forwarding?: boolean;
    usePAM?: boolean;
    configFilePath?: string;
}

export interface UserAssets {
    systemUsers?: UserInfo[];
    loginHistory?: LoginRecord[];
    currentLogins?: LoginSession[];
    sshKeys?: SSHKeyInfo[];
    sudoUsers?: SudoUserInfo[];
    sshConfig?: SSHConfig;
}

export interface LoginAssets {
    successfulLogins?: LoginRecord[];
    failedLogins?: LoginRecord[];
    currentSessions?: LoginSession[];
}

export interface FileInfo {
    path: string;
    size: number;
    modTime: number;
    permissions?: string;
    owner?: string;
    group?: string;
    isExecutable: boolean;
}

export interface CronJob {
    user: string;
    schedule: string;
    command: string;
    filePath?: string;
}

export interface SystemdService {
    name: string;
    state?: string;
    enabled: boolean;
    execStart?: string;
    description?: string;
    unitFile?: string;
}

export interface StartupScript {
    type: string;
    path: string;
    name: string;
    enabled: boolean;
}

export interface FileAssets {
    cronJobs?: CronJob[];
    systemdServices?: SystemdService[];
    startupScripts?: StartupScript[];
    recentModified?: FileInfo[];
    largeFiles?: FileInfo[];
    tmpExecutables?: FileInfo[];
}

export interface KernelModule {
    name: string;
    size: number;
    usedBy: number;
}

export interface SecurityModuleInfo {
    selinuxStatus?: string;
    apparmorStatus?: string;
    secureBootState?: string;
}

export interface KernelAssets {
    loadedModules?: KernelModule[];
    kernelParameters?: Record<string, string>;
    securityModules?: SecurityModuleInfo;
}

export interface AssetInventory {
    networkAssets?: NetworkAssets;
    processAssets?: ProcessAssets;
    userAssets?: UserAssets;
    fileAssets?: FileAssets;
    kernelAssets?: KernelAssets;
    loginAssets?: LoginAssets;
}

export interface AuditStatistics {
    networkStats?: any;
    processStats?: any;
    userStats?: any;
    fileStats?: any;
}

// VPS审计结果(Agent端收集的原始数据)
export interface VPSAuditResult {
    systemInfo: SystemInfo;
    assetInventory: AssetInventory;
    statistics: AuditStatistics;
    startTime: number;
    endTime: number;
    collectWarnings?: string[];
}

// VPS安全分析结果(Server端分析后的结果)
export interface VPSAuditAnalysis {
    auditId: string;
    securityChecks: SecurityCheck[];
    riskScore: number;
    threatLevel: 'low' | 'medium' | 'high' | 'critical';
    recommendations?: string[];
    analyzedAt: number;
}

export interface AuditResultSummary {
    id: number;
    agentId: string;
    type: string;
    startTime: number;
    endTime: number;
    createdAt: number;
    passCount: number;
    failCount: number;
    warnCount: number;
    totalCount: number;
    systemInfo: SystemInfo;
}

export interface SendCommandResponse {
    commandId: string;
    status: string;
}

export interface CommandTask {
    id: string;
    agentId: string;
    command: string;
    status: 'pending' | 'running' | 'cancelling' | 'success' | 'error' | 'cancelled';
    output?: string;
    error?: string;
    exitCode?: number;
    timeoutSeconds: number;
    truncated: boolean;
    startedAt?: number;
    finishedAt?: number;
    createdAt: number;
    updatedAt: number;
}

export const createCommandTask = (agentId: string, command: string, timeoutSeconds: number) => {
    return post<CommandTask>(`/admin/agents/${agentId}/commands`, {command, timeoutSeconds});
};

export const listCommandTasks = (agentId: string) => {
    return get<CommandTask[]>(`/admin/agents/${agentId}/commands`);
};

export const getCommandTask = (agentId: string, commandId: string) => {
    return get<CommandTask>(`/admin/agents/${agentId}/commands/${commandId}`);
};

export const cancelCommandTask = (agentId: string, commandId: string) => {
    return post<CommandTask>(`/admin/agents/${agentId}/commands/${commandId}/cancel`, {});
};

// 发送审计指令
export const sendAuditCommand = (agentId: string) => {
    return post<SendCommandResponse>(`/admin/agents/${agentId}/command?type=vps_audit`, {});
};

// 获取最新的审计结果（管理员接口）
export const getAuditResult = (agentId: string) => {
    return get<VPSAuditResult>(`/admin/agents/${agentId}/audit/result`);
};

// 获取审计结果列表（管理员接口）
export const listAuditResults = (agentId: string) => {
    return get<{ items: AuditResultSummary[]; total: number }>(`/admin/agents/${agentId}/audit/results`);
};

// 更新探针名称
export const updateAgentName = (agentId: string, name: string) => {
    return put(`/admin/agents/${agentId}/name`, {name});
};

// 更新探针信息（名称、标签、到期时间、可见性）
export interface UpdateAgentInfoRequest {
    name?: string;
    tags?: string[];
    expireTime?: number;
    visibility?: string;
}

export const updateAgentInfo = (agentId: string, data: UpdateAgentInfoRequest) => {
    return put(`/admin/agents/${agentId}`, data);
};

// 获取探针统计数据
export interface AgentStatistics {
    total: number;
    online: number;
    offline: number;
    onlineRate: number;
}

// 删除探针
export const deleteAgent = (agentId: string) => {
    return del(`/admin/agents/${agentId}`);
};

// 获取所有探针的标签
export interface GetTagsResponse {
    tags: string[];
}

export const getTags = () => {
    return get<GetTagsResponse>('/admin/agents/tags');
};

// 获取公开的探针标签列表（无需认证）
export const getPublicTags = () => {
    return get<GetTagsResponse>('/agents/tags');
};

// 批量更新探针标签
export interface BatchUpdateTagsRequest {
    agentIds: string[];
    tags: string[];
    operation: 'add' | 'remove' | 'replace'; // add: 添加标签, remove: 移除标签, replace: 替换标签
}

export interface BatchUpdateTagsResponse {
    message: string;
    count: number;
}

export const batchUpdateTags = (data: BatchUpdateTagsRequest) => {
    return post<BatchUpdateTagsResponse>('/admin/agents/batch/tags', data);
};

// 批量更新探针可见性
export interface BatchUpdateVisibilityResponse {
    message: string;
    count: number;
}

export const batchUpdateAgentVisibility = (agentIds: string[], visibility: string) => {
    return post<BatchUpdateVisibilityResponse>('/admin/agents/batch/visibility', {
        agentIds,
        visibility,
    });
};

// 流量统计相关接口

// 更新流量配置（管理员接口）
export const updateTrafficConfig = (agentId: string, data: UpdateTrafficConfigRequest) => {
    return put(`/admin/agents/${agentId}/traffic-config`, data);
};

// 获取流量统计（管理员接口）
export const getTrafficStats = (agentId: string) => {
    return get<TrafficStats>(`/admin/agents/${agentId}/traffic`);
};

// 手动重置流量（管理员接口）
export const resetAgentTraffic = (agentId: string) => {
    return post(`/admin/agents/${agentId}/traffic-reset`, {});
};

// 获取服务器地址（管理员接口）
export interface GetServerUrlResponse {
    serverUrl: string;
}

export const getServerUrl = () => {
    return post<GetServerUrlResponse>('/admin/server-url', {});
};

// SSH 登录监控相关接口

// 获取 SSH 登录监控配置
export const getSSHLoginConfig = async (agentId: string) => {
    const response = await get<SSHLoginConfig>(`/admin/agents/${agentId}/ssh-login/config`);
    return response.data;
};

// 更新 SSH 登录监控配置
export const updateSSHLoginConfig = async (agentId: string, data: UpdateSSHLoginConfigRequest) => {
    await post<SSHLoginConfig>(`/admin/agents/${agentId}/ssh-login/config`, data);
};

// 获取 SSH 登录事件列表
export const getSSHLoginEvents = async (agentId: string, params?: any) => {
   const query = qs.stringify(params);
    const response = await get<{ items: SSHLoginEvent[]; total: number }>(`/admin/agents/${agentId}/ssh-login/events?${query}`);
    return response.data;
};

// 删除 SSH 登录事件
export const deleteSSHLoginEvents = async (agentId: string) => {
    await del(`/admin/agents/${agentId}/ssh-login/events`);
};

// 清理残留的探针指标数据
export interface CleanupMetricsResponse {
    message: string;
}

export const cleanupOrphanedAgentMetrics = async () => {
    return post<CleanupMetricsResponse>('/admin/agents/cleanup-metrics', {});
};
