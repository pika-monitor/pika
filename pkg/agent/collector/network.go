package collector

import (
	"net"
	"sync"
	"time"

	"github.com/pika-monitor/pika/internal/protocol"
	"github.com/pika-monitor/pika/pkg/agent/config"
	gopsutilNet "github.com/shirou/gopsutil/v4/net"
)

const networkInterfaceRefreshInterval = 30 * time.Second

// NetworkCollector 网络监控采集器
type NetworkCollector struct {
	mu                  sync.Mutex
	config              *config.Config // 配置信息
	previous            map[string]gopsutilNet.IOCountersStat
	previousTime        time.Time
	interfaces          gopsutilNet.InterfaceStatList
	interfacesUpdatedAt time.Time
}

// safeDelta 计算网络计数器的增量,当出现重置或回绕时返回当前值避免溢出
func safeDelta(current, previous uint64) uint64 {
	if current >= previous {
		return current - previous
	}
	// 计数器被重置(例如接口重启)或发生回绕,只能依赖当前值
	return 0
}

func counterRate(delta uint64, elapsed float64) uint64 {
	if elapsed <= 0 {
		return 0
	}
	rate := float64(delta) / elapsed
	if rate >= float64(^uint64(0)) {
		return ^uint64(0)
	}
	return uint64(rate)
}

// NewNetworkCollector 创建网络采集器
func NewNetworkCollector(cfg *config.Config) *NetworkCollector {
	return &NetworkCollector{
		config: cfg,
	}
}

// Collect 采集网络数据并根据相邻采样的时间差计算速率
func (n *NetworkCollector) Collect() ([]protocol.NetworkData, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	secondCounters, secondInterfaces, err := n.collectOnce()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	elapsed := now.Sub(n.previousTime).Seconds()
	firstStatsMap := n.previous
	n.previous = make(map[string]gopsutilNet.IOCountersStat, len(secondCounters))
	for _, counter := range secondCounters {
		n.previous[counter.Name] = counter
	}
	n.previousTime = now

	// 创建接口信息映射(使用第二次采集的接口信息)
	interfaceMap := make(map[string]protocol.NetworkData)
	for _, iface := range secondInterfaces {
		// 使用配置中的排除规则过滤网卡
		if n.config.ShouldExcludeNetworkInterface(iface.Name) {
			continue
		}

		// 获取 IP 地址列表
		var addrs []string
		for _, addr := range iface.Addrs {
			// 过滤 fe80::/10 (Link-Local) 地址
			ip := net.ParseIP(addr.Addr)
			if ip == nil {
				// 尝试处理带有 CIDR 的字符串
				if parsedIP, _, err := net.ParseCIDR(addr.Addr); err == nil {
					ip = parsedIP
				}
			}
			if ip != nil && ip.IsLinkLocalUnicast() {
				continue
			}
			addrs = append(addrs, addr.Addr)
		}

		interfaceMap[iface.Name] = protocol.NetworkData{
			Interface:  iface.Name,
			MacAddress: iface.HardwareAddr,
			Addrs:      addrs,
		}
	}

	// 计算速率(基于两次采集的差值)
	var networkDataList []protocol.NetworkData
	for _, counter := range secondCounters {
		// 使用配置中的排除规则过滤网卡
		if n.config.ShouldExcludeNetworkInterface(counter.Name) {
			continue
		}

		// 如果已有接口信息,则更新;否则创建新的
		netData, ok := interfaceMap[counter.Name]
		if !ok {
			netData = protocol.NetworkData{
				Interface: counter.Name,
			}
		}

		// 使用第二次采集的总量
		netData.BytesSentTotal = counter.BytesSent
		netData.BytesRecvTotal = counter.BytesRecv

		// 计算速率(如果第一次采集有数据)
		if firstStat, exists := firstStatsMap[counter.Name]; exists && elapsed > 0 {
			bytesSentDelta := safeDelta(counter.BytesSent, firstStat.BytesSent)
			bytesRecvDelta := safeDelta(counter.BytesRecv, firstStat.BytesRecv)
			netData.BytesSentRate = counterRate(bytesSentDelta, elapsed)
			netData.BytesRecvRate = counterRate(bytesRecvDelta, elapsed)
		} else {
			// 如果第一次采集没有该网卡数据,速率为0
			netData.BytesSentRate = 0
			netData.BytesRecvRate = 0
		}

		networkDataList = append(networkDataList, netData)
	}

	return networkDataList, nil
}

// collectOnce 执行一次网络数据采集
func (n *NetworkCollector) collectOnce() ([]gopsutilNet.IOCountersStat, []gopsutilNet.InterfaceStat, error) {
	// 获取网络接口信息
	interfaces, err := n.collectInterfaces(time.Now(), gopsutilNet.Interfaces)
	if err != nil {
		return nil, nil, err
	}

	// 获取网络 IO 统计
	ioCounters, err := gopsutilNet.IOCounters(true)
	if err != nil {
		return nil, nil, err
	}

	return ioCounters, interfaces, nil
}

func (n *NetworkCollector) collectInterfaces(now time.Time, load func() (gopsutilNet.InterfaceStatList, error)) (gopsutilNet.InterfaceStatList, error) {
	if !n.interfacesUpdatedAt.IsZero() && now.Sub(n.interfacesUpdatedAt) < networkInterfaceRefreshInterval {
		return n.interfaces, nil
	}
	interfaces, err := load()
	if err != nil {
		return nil, err
	}
	n.interfaces = interfaces
	n.interfacesUpdatedAt = now
	return interfaces, nil
}
