import React, {useState} from 'react';
import {useSearchParams} from 'react-router-dom';
import {App, Divider, Select, Space, Table, Tag} from 'antd';
import type {ColumnsType, TablePaginationConfig} from 'antd/es/table';
import {Trash2} from 'lucide-react';
import {clearAlertRecords, getAlertRecords} from '@/api/alert.ts';
import type {AlertRecord} from '@/types';
import dayjs from 'dayjs';
import {getErrorMessage} from '@/lib/utils';
import {PageHeader} from '@admin/components';
import {useQuery, useQueryClient} from '@tanstack/react-query';
import {listAgentsByAdmin} from "@/api/agent.ts";

const AlertRecordList = () => {
    const {message: messageApi, modal} = App.useApp();
    const queryClient = useQueryClient();
    const [searchParams, setSearchParams] = useSearchParams();
    const [selectedAgentId, setSelectedAgentId] = useState<string>('');

    const pageIndex = Number(searchParams.get('pageIndex')) || 1;
    const pageSize = Number(searchParams.get('pageSize')) || 20;

    // 使用 react-query 获取探针列表
    const {data: agentsData} = useQuery({
        queryKey: ['agents-for-alert-filter'],
        queryFn: async () => {
            const response = await listAgentsByAdmin();
            return response.data;
        },
    });

    // 告警类型中文映射
    const alertTypeMap: Record<string, string> = {
        cpu: 'CPU使用率',
        memory: '内存使用率',
        disk: '磁盘使用率',
        network: '网速',
        traffic: '流量',
        cert: 'HTTPS证书',
        service: '服务下线',
        agent_offline: '探针离线',
        agent_expire: '机器到期',
    };

    // 告警级别映射
    const getLevelTag = (level: string) => {
        const config = {
            info: {color: 'blue', text: '信息'},
            warning: {color: 'orange', text: '警告'},
            critical: {color: 'red', text: '严重'},
        };
        const levelConfig = config[level as keyof typeof config] || {color: 'default', text: level};
        return <Tag color={levelConfig.color}>{levelConfig.text}</Tag>;
    };

    // 状态映射
    const getStatusTag = (status: string) => {
        const config = {
            firing: {color: 'red', text: '告警中'},
            resolved: {color: 'green', text: '已恢复'},
            notice: {color: 'blue', text: '通知'},
        };
        const statusConfig = config[status as keyof typeof config] || {color: 'default', text: status};
        return <Tag color={statusConfig.color}>{statusConfig.text}</Tag>;
    };

    // 格式化持续时间
    const formatDuration = (firedAt: number, resolvedAt: number | null, status: string) => {
        // 如果告警还在进行中，返回 "-"
        if (status === 'firing' || !resolvedAt || resolvedAt <= firedAt) {
            return '-';
        }

        const durationMs = resolvedAt - firedAt;
        const durationSec = Math.floor(durationMs / 1000);

        if (durationSec < 60) {
            return `${durationSec}秒`;
        }

        if (durationSec < 3600) {
            const minutes = Math.floor(durationSec / 60);
            const seconds = durationSec % 60;
            return `${minutes}分${seconds}秒`;
        }

        const hours = Math.floor(durationSec / 3600);
        const minutes = Math.floor((durationSec % 3600) / 60);
        const seconds = durationSec % 60;
        return `${hours}时${minutes}分${seconds}秒`;
    };

    // 计算探针选项
    const agentOptions = agentsData?.map((agent) => ({
        label: agent.name || agent.id,
        value: agent.id,
    })) || [];

    const {
        data: alertPaging,
        isLoading,
        isFetching,
    } = useQuery({
        queryKey: ['admin', 'alert-records', pageIndex, pageSize, selectedAgentId],
        queryFn: () => getAlertRecords(pageIndex, pageSize, selectedAgentId || undefined),
    });

    // 处理表格变化
    const handleTableChange = (newPagination: TablePaginationConfig) => {
        const nextParams = new URLSearchParams(searchParams);
        nextParams.set('pageIndex', String(newPagination.current || 1));
        nextParams.set('pageSize', String(newPagination.pageSize || pageSize));
        setSearchParams(nextParams);
    };

    // 处理探针筛选变化
    const handleAgentChange = (value: string) => {
        setSelectedAgentId(value || '');
        const nextParams = new URLSearchParams(searchParams);
        nextParams.set('pageIndex', '1');
        nextParams.set('pageSize', String(pageSize));
        setSearchParams(nextParams);
    };

    // 清空记录
    const handleClear = () => {
        modal.confirm({
            title: '确认清空',
            content: selectedAgentId
                ? '确定要清空该探针的所有告警记录吗？'
                : '确定要清空所有告警记录吗？此操作不可恢复！',
            okText: '确定',
            okType: 'danger',
            cancelText: '取消',
            onOk: async () => {
                try {
                    await clearAlertRecords(selectedAgentId || undefined);
                    messageApi.success('清空成功');
                    queryClient.invalidateQueries({queryKey: ['admin', 'alert-records']});
                } catch (error: unknown) {
                    messageApi.error(getErrorMessage(error, '清空失败'));
                }
            },
        });
    };

    const columns: ColumnsType<AlertRecord> = [
        {
            title: 'ID',
            dataIndex: 'id',
            width: 80,
        },
        {
            title: '探针',
            dataIndex: 'agentName',
            width: 200,
            ellipsis: true,
        },
        {
            title: '告警类型',
            dataIndex: 'alertType',
            width: 120,
            render: (_, record) => alertTypeMap[record.alertType] || record.alertType,
        },
        {
            title: '告警消息',
            dataIndex: 'message',
            width: 320,
            ellipsis: true,
        },
        {
            title: '阈值',
            dataIndex: 'threshold',
            width: 100,
            render: (_, record) => {
                if (record.alertType === 'network') {
                    return `${record.threshold.toFixed(2)} MB/s`;
                }
                if (record.alertType === 'cert' || record.alertType === 'agent_expire') {
                    return `${record.threshold.toFixed(0)} 天`;
                }
                if (record.alertType === 'service' || record.alertType === 'agent_offline') {
                    return `${record.threshold.toFixed(0)} 秒`;
                }
                return `${record.threshold.toFixed(2)}%`;
            },
        },
        {
            title: '告警值',
            dataIndex: 'actualValue',
            width: 100,
            render: (_, record) => {
                if (record.alertType === 'network') {
                    return `${record.actualValue.toFixed(2)} MB/s`;
                }
                if (record.alertType === 'cert' || record.alertType === 'agent_expire') {
                    return `${record.actualValue.toFixed(0)} 天`;
                }
                if (record.alertType === 'service' || record.alertType === 'agent_offline') {
                    return `${record.actualValue.toFixed(0)} 秒`;
                }
                return `${record.actualValue.toFixed(2)}%`;
            },
        },
        {
            title: '恢复值',
            dataIndex: 'resolvedValue',
            width: 100,
            render: (_, record) => {
                // 只有已恢复的告警才显示恢复值
                if (record.status !== 'resolved' || record.resolvedValue === undefined) {
                    return '-';
                }
                if (record.alertType === 'network') {
                    return `${record.resolvedValue.toFixed(2)} MB/s`;
                }
                if (record.alertType === 'cert' || record.alertType === 'agent_expire') {
                    return `${record.resolvedValue.toFixed(0)} 天`;
                }
                if (record.alertType === 'service' || record.alertType === 'agent_offline') {
                    return record.resolvedValue === 0 ? '已在线' : `${record.resolvedValue.toFixed(0)} 秒`;
                }
                return `${record.resolvedValue.toFixed(2)}%`;
            },
        },
        {
            title: '告警级别',
            dataIndex: 'level',
            width: 100,
            render: (_, record) => getLevelTag(record.level),
        },
        {
            title: '状态',
            dataIndex: 'status',
            width: 100,
            render: (_, record) => getStatusTag(record.status),
        },
        {
            title: '触发时间',
            dataIndex: 'firedAt',
            width: 180,
            render: (_, record) => dayjs(record.firedAt).format('YYYY-MM-DD HH:mm:ss'),
        },
        {
            title: '恢复时间',
            dataIndex: 'resolvedAt',
            width: 180,
            render: (_, record) =>
                record.resolvedAt ? dayjs(record.resolvedAt).format('YYYY-MM-DD HH:mm:ss') : '-',
        },
        {
            title: '持续时间',
            dataIndex: 'duration',
            width: 130,
            render: (_, record) => formatDuration(record.firedAt, record.resolvedAt, record.status),
        },
    ];

    return (
        <div className={'space-y-6'}>
            <PageHeader
                title="告警记录"
                description="查看和管理系统的告警记录"
                actions={[
                    {
                        key: 'clear',
                        label: '清空记录',
                        icon: <Trash2 className="h-4 w-4"/>,
                        type: 'primary',
                        danger: true,
                        onClick: handleClear,
                    },
                ]}
            />

            <div className="bg-white dark:bg-[#1c1c21] rounded-2xl border border-gray-100 dark:border-white/5 shadow-sm p-4 sm:p-6 space-y-4">
                <div className="flex flex-col md:flex-row justify-between items-start md:items-center gap-4">
                    <Space>
                        <Select
                            placeholder="选择探针"
                            allowClear
                            showSearch={{
                                filterOption: (inputValue, option) =>
                                    (option?.label?.toString() ?? '')
                                        .toLowerCase()
                                        .includes(inputValue.toLowerCase()),
                            }}
                            style={{width: 200}}
                            value={selectedAgentId || undefined}
                            onChange={handleAgentChange}
                            options={agentOptions}
                        />
                    </Space>
                </div>

                <Table<AlertRecord>
                    columns={columns}
                    dataSource={alertPaging?.items || []}
                    loading={isLoading || isFetching}
                    rowKey="id"
                    size={'small'}
                    scroll={{x: 1700}}
                    tableLayout="fixed"
                    pagination={{
                        current: pageIndex,
                        pageSize,
                        total: alertPaging?.total || 0,
                        showSizeChanger: true,
                        showQuickJumper: true,
                        pageSizeOptions: ['10', '20', '50', '100'],
                        showTotal: (total) => `共 ${total} 条`,
                    }}
                    onChange={handleTableChange}
                />
            </div>
        </div>
    );
};

export default AlertRecordList;
