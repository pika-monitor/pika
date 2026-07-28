import {useEffect, useState} from 'react';
import {Alert, App, Button, Card, Empty, Input, InputNumber, List, Space, Tag, Tooltip, Typography} from 'antd';
import {Ban, Clock3, Play, RefreshCw, TerminalSquare} from 'lucide-react';
import {useQuery} from '@tanstack/react-query';
import dayjs from 'dayjs';
import {
    cancelCommandTask,
    createCommandTask,
    getCommandTask,
    listCommandTasks,
    type CommandTask,
} from '@/api/agent.ts';
import {getErrorMessage} from '@/lib/utils';

const {Text, Paragraph} = Typography;

interface AgentCommandsProps {
    agentId: string;
    online: boolean;
}

const activeStatuses = new Set(['pending', 'running', 'cancelling']);

const statusMeta: Record<CommandTask['status'], {label: string; color: string}> = {
    pending: {label: '等待执行', color: 'default'},
    running: {label: '执行中', color: 'processing'},
    cancelling: {label: '取消中', color: 'warning'},
    success: {label: '成功', color: 'success'},
    error: {label: '失败', color: 'error'},
    cancelled: {label: '已取消', color: 'default'},
};

const formatTaskDuration = (task: CommandTask) => {
    if (!task.startedAt) return null;
    const end = task.finishedAt || Date.now();
    const seconds = Math.max(0, end - task.startedAt) / 1000;
    return seconds < 10 ? `${seconds.toFixed(1)} 秒` : `${Math.round(seconds)} 秒`;
};

const AgentCommands = ({agentId, online}: AgentCommandsProps) => {
    const {message} = App.useApp();
    const [command, setCommand] = useState('');
    const [timeoutSeconds, setTimeoutSeconds] = useState(60);
    const [selectedId, setSelectedId] = useState<string>();
    const [submitting, setSubmitting] = useState(false);
    const [cancelling, setCancelling] = useState(false);

    const {data: tasks = [], refetch: refetchTasks} = useQuery({
        queryKey: ['admin', 'agent', agentId, 'commands'],
        queryFn: async () => (await listCommandTasks(agentId)).data,
        refetchInterval: 3000,
    });

    useEffect(() => {
        if (!selectedId && tasks.length > 0) {
            setSelectedId(tasks[0].id);
        }
    }, [selectedId, tasks]);

    const {data: selectedTask, refetch: refetchTask} = useQuery({
        queryKey: ['admin', 'agent', agentId, 'command', selectedId],
        queryFn: async () => (await getCommandTask(agentId, selectedId!)).data,
        enabled: !!selectedId,
        refetchInterval: query => {
            const task = query.state.data as CommandTask | undefined;
            return task && activeStatuses.has(task.status) ? 1000 : false;
        },
    });

    const handleRun = async () => {
        const value = command.trim();
        if (!value) {
            message.warning('请输入要执行的命令');
            return;
        }
        setSubmitting(true);
        try {
            const response = await createCommandTask(agentId, value, timeoutSeconds);
            setSelectedId(response.data.id);
            setCommand('');
            await refetchTasks();
            message.success('命令任务已下发');
        } catch (error) {
            message.error(getErrorMessage(error, '命令下发失败'));
        } finally {
            setSubmitting(false);
        }
    };

    const handleCancel = async () => {
        if (!selectedTask) return;
        setCancelling(true);
        try {
            await cancelCommandTask(agentId, selectedTask.id);
            await Promise.all([refetchTask(), refetchTasks()]);
            message.success('取消请求已发送');
        } catch (error) {
            message.error(getErrorMessage(error, '取消任务失败'));
        } finally {
            setCancelling(false);
        }
    };

    return (
        <Space orientation="vertical" size="large" style={{width: '100%'}}>
            <Alert
                type="warning"
                showIcon
                title="远程命令拥有 Agent 服务进程的系统权限"
                description="请仅执行可信命令。命令和输出会保存在服务端；每台 Agent 最多并发执行 4 个任务，单个任务最长运行 1 小时，输出最多保存 1 MiB。"
            />
            <Card title="下发命令">
                <Space orientation="vertical" style={{width: '100%'}}>
                    <Input.TextArea
                        value={command}
                        onChange={event => setCommand(event.target.value)}
                        placeholder={online ? '例如：uname -a' : '探针离线，暂时无法下发命令'}
                        autoSize={{minRows: 3, maxRows: 8}}
                        disabled={!online}
                        onKeyDown={event => {
                            if ((event.ctrlKey || event.metaKey) && event.key === 'Enter') {
                                void handleRun();
                            }
                        }}
                    />
                    <Space wrap>
                        <Text>超时（秒）</Text>
                        <InputNumber
                            min={1}
                            max={3600}
                            value={timeoutSeconds}
                            onChange={value => setTimeoutSeconds(value ?? 60)}
                            disabled={!online}
                        />
                        <Button
                            type="primary"
                            icon={<Play size={16}/>} loading={submitting}
                            disabled={!online || !command.trim()}
                            onClick={handleRun}
                        >
                            后台执行
                        </Button>
                        <Text type="secondary">Ctrl/⌘ + Enter 快速下发</Text>
                    </Space>
                </Space>
            </Card>

            <div className="grid grid-cols-1 items-start gap-4 xl:grid-cols-[380px_minmax(0,1fr)]">
                <Card
                    title={(
                        <div className="flex items-center gap-2">
                            <TerminalSquare size={17}/>
                            <span>最近任务</span>
                            <span className="text-xs font-normal text-gray-400">{tasks.length}</span>
                        </div>
                    )}
                    styles={{body: {padding: 0}}}
                >
                    {tasks.length === 0 ? (
                        <div className="py-8">
                            <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无命令任务"/>
                        </div>
                    ) : (
                        <div className="max-h-[600px] overflow-y-auto overscroll-contain">
                            <List
                                dataSource={tasks}
                                renderItem={task => {
                                    const selected = selectedId === task.id;
                                    const duration = formatTaskDuration(task);
                                    return (
                                        <List.Item
                                            role="button"
                                            tabIndex={0}
                                            aria-current={selected ? 'true' : undefined}
                                            className={`cursor-pointer !border-l-2 !px-4 !py-3 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-blue-500 ${selected ? '!border-l-blue-500 bg-blue-50/80 dark:bg-cyan-950/30' : '!border-l-transparent hover:bg-gray-50 dark:hover:bg-white/5'}`}
                                            onClick={() => setSelectedId(task.id)}
                                            onKeyDown={event => {
                                                if (event.key === 'Enter' || event.key === ' ') {
                                                    event.preventDefault();
                                                    setSelectedId(task.id);
                                                }
                                            }}
                                        >
                                            <div className="w-full min-w-0">
                                                <div className="flex min-w-0 items-center gap-3">
                                                    <Tooltip
                                                        title={<span className="font-mono break-all">{task.command}</span>}
                                                        placement="topLeft"
                                                        mouseEnterDelay={0.5}
                                                    >
                                                        <div className="min-w-0 flex-1 truncate font-mono text-sm font-medium leading-6">
                                                            <span className="mr-1 text-gray-400">$</span>{task.command}
                                                        </div>
                                                    </Tooltip>
                                                    <Tag className="!m-0 shrink-0" color={statusMeta[task.status].color}>
                                                        {statusMeta[task.status].label}
                                                    </Tag>
                                                </div>
                                                <div className="mt-1.5 flex min-w-0 items-center gap-3 text-xs text-gray-400">
                                                    <span className="flex shrink-0 items-center gap-1">
                                                        <Clock3 size={12}/>
                                                        {dayjs(task.createdAt).format('MM-DD HH:mm:ss')}
                                                    </span>
                                                    {duration && <span className="truncate">耗时 {duration}</span>}
                                                    {task.exitCode !== undefined && <span className="ml-auto shrink-0">退出码 {task.exitCode}</span>}
                                                </div>
                                            </div>
                                        </List.Item>
                                    );
                                }}
                            />
                        </div>
                    )}
                </Card>

                <Card
                    title="执行输出"
                    extra={selectedTask && (
                        <Space>
                            <Button icon={<RefreshCw size={16}/>} onClick={() => void refetchTask()}>刷新</Button>
                            {(selectedTask.status === 'pending' || selectedTask.status === 'running') && (
                                <Button danger icon={<Ban size={16}/>} loading={cancelling} onClick={handleCancel}>取消</Button>
                            )}
                        </Space>
                    )}
                >
                    {!selectedTask ? (
                        <Empty description="请选择一个任务"/>
                    ) : (
                        <Space orientation="vertical" style={{width: '100%'}}>
                            <div className="flex flex-wrap items-center gap-2">
                                <Tag color={statusMeta[selectedTask.status].color}>{statusMeta[selectedTask.status].label}</Tag>
                                {selectedTask.exitCode !== undefined && <Text>退出码：{selectedTask.exitCode}</Text>}
                                <Text type="secondary">超时：{selectedTask.timeoutSeconds} 秒</Text>
                            </div>
                            <Paragraph copyable={{text: selectedTask.command}} className="font-mono m-0">
                                $ {selectedTask.command}
                            </Paragraph>
                            <pre className="m-0 min-h-64 max-h-[60vh] overflow-auto rounded-lg bg-slate-950 p-4 text-sm whitespace-pre-wrap break-words text-slate-100">
                                {selectedTask.output || (activeStatuses.has(selectedTask.status) ? '等待输出…' : '（无输出）')}
                            </pre>
                            {selectedTask.truncated && <Alert type="warning" showIcon title="输出超过 1 MiB，后续内容已截断"/>}
                            {selectedTask.error && <Alert type="error" showIcon title={selectedTask.error}/>}
                        </Space>
                    )}
                </Card>
            </div>
        </Space>
    );
};

export default AgentCommands;
