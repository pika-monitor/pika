import {useEffect, useState} from 'react';
import {useNavigate, useParams, useSearchParams} from 'react-router-dom';
import type {TabsProps} from 'antd';
import {Alert, Button, Card, Space, Spin, Tabs, Tag} from 'antd';
import {Activity, ArrowLeft, FileWarning, Lock, Shield, Terminal, TrendingUp} from 'lucide-react';
import {useQuery} from '@tanstack/react-query';
import {getAgentForAdmin} from '@/api/agent.ts';
import AgentBasicInfo from './AgentBasicInfo';
import AgentAudit from './AgentAudit';
import TamperProtection from './TamperProtection';
import SSHLoginMonitor from './SSHLoginMonitor';
import TrafficStats from './TrafficStats';
import AgentCommands from './AgentCommands';

const AgentDetail = () => {
    const {id} = useParams<{ id: string }>();
    const navigate = useNavigate();
    const [searchParams, setSearchParams] = useSearchParams();
    const [activeTab, setActiveTab] = useState<string>(searchParams.get('tab') || 'info');

    // 获取探针基本信息（用于显示头部卡片）
    const {data: agent, isLoading} = useQuery({
        queryKey: ['admin', 'agent', id],
        queryFn: async () => {
            if (!id) throw new Error('Agent ID is required');
            const response = await getAgentForAdmin(id);
            return response.data;
        },
        enabled: !!id,
    });

    useEffect(() => {
        // 同步 activeTab 到 URL
        const nextParams = new URLSearchParams(searchParams);
        if (nextParams.get('tab') === activeTab) {
            return;
        }
        nextParams.set('tab', activeTab);
        setSearchParams(nextParams);
    }, [activeTab, searchParams, setSearchParams]);

    if (isLoading) {
        return (
            <div className="text-center py-24">
                <Spin/>
            </div>
        );
    }

    if (!agent || !id) {
        return (
            <div className="text-center py-24">
                <Alert
                    title="未找到探针"
                    description="该探针不存在或已被删除"
                    type="error"
                    showIcon
                />
            </div>
        );
    }

    // Tab 项配置
    const tabItems: TabsProps['items'] = [
        {
            key: 'info',
            label: (
                <div className="flex items-center gap-2 text-sm">
                    <Activity size={16}/>
                    <div>基本信息</div>
                </div>
            ),
            children: <AgentBasicInfo agentId={id}/>,
        },
        {
            key: 'traffic',
            label: (
                <div className="flex items-center gap-2 text-sm">
                    <TrendingUp size={16}/>
                    <div>流量统计</div>
                </div>
            ),
            children: <TrafficStats agentId={id}/>,
        },
        {
            key: 'commands',
            label: (
                <div className="flex items-center gap-2 text-sm">
                    <Terminal size={16}/>
                    <div>远程命令</div>
                </div>
            ),
            children: <AgentCommands agentId={id} online={agent.status === 1}/>,
        },
        {
            key: 'audit',
            label: (
                <div className="flex items-center gap-2 text-sm">
                    <Shield size={16}/>
                    <div>安全审计</div>
                </div>
            ),
            children: <AgentAudit agentId={id}/>,
        },
        {
            key: 'tamper',
            label: (
                <div className="flex items-center gap-2 text-sm">
                    <FileWarning size={16}/>
                    <div>防篡改保护</div>
                </div>
            ),
            children: agent.os.toLowerCase().includes('linux') ? (
                <TamperProtection agentId={id}/>
            ) : (
                <Alert
                    title="功能限制"
                    description="防篡改保护功能仅支持 Linux 系统。当前系统为 Windows 或其他系统，无法使用此功能。"
                    type="warning"
                    showIcon
                />
            ),
        },
        {
            key: 'ssh-login',
            label: (
                <div className="flex items-center gap-2 text-sm">
                    <Lock size={16}/>
                    <div>SSH 登录监控</div>
                </div>
            ),
            children: agent.os.toLowerCase().includes('linux') ? (
                <SSHLoginMonitor agentId={id}/>
            ) : (
                <Alert
                    title="功能限制"
                    description="SSH 登录监控功能仅支持 Linux 系统。当前系统为 Windows 或其他系统，无法使用此功能。"
                    type="warning"
                    showIcon
                />
            ),
        },
    ];

    return (
        <div className="space-y-4 lg:space-y-6">
            {/* 探针状态卡片 */}
            <Card variant="outlined">
                <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
                    <div className="flex items-center gap-4">
                        <Activity size={32} className="text-blue-500"/>
                        <div>
                            <h2 className="text-xl font-semibold m-0">{agent.name || agent.hostname}</h2>
                            <Space className="mt-1">
                                <span className="text-gray-500">{agent.hostname}</span>
                                {agent.status === 1 ? (
                                    <Tag color="success">在线</Tag>
                                ) : (
                                    <Tag color="error">离线</Tag>
                                )}
                            </Space>
                        </div>
                    </div>
                    <Button
                        icon={<ArrowLeft size={16}/>}
                        onClick={() => navigate('/admin/agents')}
                    >
                        返回列表
                    </Button>
                </div>
            </Card>

            {/* Tabs 内容 */}
            <Tabs
                activeKey={activeTab}
                onChange={setActiveTab}
                items={tabItems}
            />
        </div>
    );
};

export default AgentDetail;
