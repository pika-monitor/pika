package service

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/pika-monitor/pika/internal/protocol"
	"github.com/pika-monitor/pika/pkg/agent/audit"
	"github.com/pika-monitor/pika/pkg/agent/collector"
	"github.com/pika-monitor/pika/pkg/agent/config"
	"github.com/pika-monitor/pika/pkg/agent/id"
	"github.com/pika-monitor/pika/pkg/agent/sshmonitor"
	"github.com/pika-monitor/pika/pkg/agent/tamper"
	"github.com/pika-monitor/pika/pkg/version"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jpillora/backoff"
	"github.com/sourcegraph/conc"
)

const (
	agentPingInterval   = 10 * time.Second
	agentPongWait       = 30 * time.Second
	agentWriteWait      = 5 * time.Second
	agentCollectTimeout = 30 * time.Second
	// agentDataWriteWait 数据帧写入超时。没有超时的写在对端停止读取
	// （网络劣化、服务端处理阻塞）时会无限阻塞并持有连接写锁，
	// 期间采集快照被覆盖造成静默丢数据。
	agentDataWriteWait = 10 * time.Second
)

// safeConn 线程安全的 WebSocket 连接包装器
type safeConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

// WriteJSON 线程安全地写入 JSON 消息（带写超时）
func (sc *safeConn) WriteJSON(v interface{}) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if err := sc.conn.SetWriteDeadline(time.Now().Add(agentDataWriteWait)); err != nil {
		return err
	}
	return sc.conn.WriteJSON(v)
}

// WriteMessage 线程安全地写入消息（带写超时）
func (sc *safeConn) WriteMessage(messageType int, data []byte) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if err := sc.conn.SetWriteDeadline(time.Now().Add(agentDataWriteWait)); err != nil {
		return err
	}
	return sc.conn.WriteMessage(messageType, data)
}

// ReadJSON 读取 JSON 消息（读操作本身是安全的）
func (sc *safeConn) ReadJSON(v interface{}) error {
	return sc.conn.ReadJSON(v)
}

// Close 关闭连接
func (sc *safeConn) Close() error {
	return sc.conn.Close()
}

// WriteControl 线程安全地写入控制消息
func (sc *safeConn) WriteControl(messageType int, data []byte, deadline time.Time) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.conn.WriteControl(messageType, data, deadline)
}

// Agent 探针服务
type Agent struct {
	cfg              *config.Config
	idMgr            *id.Manager
	bootID           string
	cancel           context.CancelFunc
	connMu           sync.RWMutex
	activeConn       *safeConn
	collectorMu      sync.RWMutex
	collectorManager *collector.Manager
	metricsStore     *metricsStore
	outbox           *outbox
	tamperProtector  *tamper.Protector
	sshMonitor       *sshmonitor.Monitor
}

// New 创建 Agent 实例
func New(cfg *config.Config) *Agent {
	return &Agent{
		cfg:              cfg,
		idMgr:            id.NewManager(),
		bootID:           uuid.NewString(),
		collectorManager: collector.NewManager(cfg),
		metricsStore:     newMetricsStore(),
		outbox:           newOutbox(),
		tamperProtector:  tamper.NewProtector(),
		sshMonitor:       sshmonitor.NewMonitor(),
	}
}

// Start 启动探针服务
func (a *Agent) Start(ctx context.Context) error {
	// 创建可取消的 context
	ctx, cancel := context.WithCancel(ctx)
	a.cancel = cancel

	// 采集与发送解耦：采集永远运行（断线也写快照），发送独立读快照上报
	go a.collectLoop(ctx)
	go a.sendLoop(ctx)
	// 安全事件消费属于 Agent 生命周期，而不是单次 WebSocket 连接。
	// 断线和退避期间仍持续写入 outbox，避免上游小缓冲区溢出丢事件。
	go a.tamperEventLoop(ctx)
	go a.sshLoginEventLoop(ctx)

	// 启动探针主循环
	b := &backoff.Backoff{
		Min:    1 * time.Second,
		Max:    1 * time.Minute,
		Factor: 2,
		Jitter: true,
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		err := a.runOnce(ctx, b.Reset)

		// 检查是否是上下文取消
		if ctx.Err() != nil {
			slog.Info("收到停止信号，探针服务退出")
			return nil
		}

		// 连接建立失败或注册失败（使用 backoff）
		if err != nil {
			retryAfter := b.Duration()
			slog.Warn("探针运行出错，将在后重试", "error", err, "retryAfter", retryAfter)

			select {
			case <-time.After(retryAfter):
				continue
			case <-ctx.Done():
				return nil
			}
		}

		// 理论上不会到这里
		slog.Info("连接意外结束")
		return nil
	}
}

// Stop 停止探针服务
func (a *Agent) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
}

// runOnce 运行一次探针连接
// 返回 error 表示需要重连，返回 nil 表示上下文取消
func (a *Agent) runOnce(ctx context.Context, onRegistered func()) error {
	wsURL := a.cfg.GetWebSocketURL()
	slog.Info("正在连接到服务器", "url", wsURL)

	// 创建自定义的 Dialer
	dialer := &websocket.Dialer{
		Proxy:             http.ProxyFromEnvironment,
		HandshakeTimeout:  45 * time.Second,
		EnableCompression: true,
	}
	if a.cfg.Server.InsecureSkipVerify {
		dialer.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
		slog.Warn("警告: 已禁用 TLS 证书验证")
	}

	// 连接到服务器
	rawConn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("connect failed: %w", err)
	}
	defer rawConn.Close()

	// 创建线程安全的连接包装器
	conn := &safeConn{conn: rawConn}

	if err := rawConn.SetReadDeadline(time.Now().Add(agentPongWait)); err != nil {
		return fmt.Errorf("set read deadline failed: %w", err)
	}
	rawConn.SetPongHandler(func(string) error {
		return rawConn.SetReadDeadline(time.Now().Add(agentPongWait))
	})

	// 设置 Ping 处理器，自动响应服务端的 Ping
	rawConn.SetPingHandler(func(appData string) error {
		if err := rawConn.SetReadDeadline(time.Now().Add(agentPongWait)); err != nil {
			return err
		}
		return rawConn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(agentWriteWait))
	})

	// 发送注册消息
	registerResp, err := a.registerAgent(conn)
	if err != nil {
		return fmt.Errorf("register failed: %w", err)
	}
	onRegistered()

	slog.Info("探针注册成功，开始监控...")

	a.setActiveConn(conn)
	// 重置发送游标，使下一个 sendLoop tick 立即发送当前全量快照，
	// 保证重连瞬间服务端就能拿到最新数据
	a.metricsStore.reset()
	// 可靠服务端返回累计确认位点；旧服务端没有 ACK，发送器会在每次
	// WebSocket 写成功后本地确认，从而完整支持升级后的降级/回滚。
	if registerResp.Reliable && registerResp.AckSeq > 0 {
		a.outbox.ack(registerResp.AckSeq)
	}
	defer func() {
		a.setActiveConn(nil)
	}()

	// 创建完成通道和错误通道
	done := make(chan struct{})
	errChan := make(chan error, 1) // 只需要接收第一个错误

	// 创建 WaitGroup 用于等待所有 goroutine 退出
	var wg conc.WaitGroup

	// 启动读取循环（处理服务端的 Ping/Pong 等控制消息）
	wg.Go(func() {
		if err := a.readLoop(rawConn, done); err != nil {
			select {
			case errChan <- fmt.Errorf("read failed: %w", err):
			default:
			}
		}
	})

	// 主动发送 Ping，快速检测断线
	wg.Go(func() {
		if err := a.pingLoop(ctx, conn, done); err != nil {
			select {
			case errChan <- fmt.Errorf("ping failed: %w", err):
			default:
			}
		}
	})

	// 每个连接只有一个 outbox 发送器。可靠服务端等待累计 ACK；旧服务端
	// 按写成功确认，队列不会因版本回退而被永久搁置。
	wg.Go(func() {
		a.eventSendLoop(done, conn, registerResp.Reliable)
	})

	// 等待第一个错误或上下文取消
	var returnErr error
	select {
	case err := <-errChan:
		slog.Info("连接断开", "error", err)
		returnErr = err
	case <-ctx.Done():
		// 收到取消信号
		slog.Info("收到停止信号，准备关闭连接")
		returnErr = ctx.Err()
	}

	// 关闭 done channel，通知所有 goroutine 退出
	close(done)
	a.setActiveConn(nil)

	// 发送 WebSocket 关闭消息
	closeMsg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
	if err := conn.WriteMessage(websocket.CloseMessage, closeMsg); err != nil {
		slog.Warn("发送关闭消息失败", "error", err)
	}
	if err := rawConn.Close(); err != nil && !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
		slog.Warn("关闭连接失败", "error", err)
	}

	// 等待所有 goroutine 优雅退出
	wg.Wait()

	return returnErr
}

// readLoop 读取服务端消息（主要用于处理 Ping/Pong 和指令）
func (a *Agent) readLoop(conn *websocket.Conn, done chan struct{}) error {
	for {
		select {
		case <-done:
			return nil
		default:
		}

		// 读取消息（这会触发 PingHandler）
		_, message, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		// 解析消息
		var msg protocol.InputMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			slog.Warn("解析消息失败", "error", err)
			continue
		}

		switch msg.Type {
		case protocol.MessageTypeCommand:
			go a.handleCommand(msg.Data)
		case protocol.MessageTypeMonitorConfig:
			go a.handleMonitorConfig(msg.Data)
		case protocol.MessageTypeTamperProtect:
			go a.handleTamperProtect(msg.Data)
		case protocol.MessageTypeDDNSConfig:
			go a.handleDDNSConfig(msg.Data)
		case protocol.MessageTypePublicIPConfig:
			go a.handlePublicIPConfig(msg.Data)
		case protocol.MessageTypeSSHLoginConfig:
			go a.handleSSHLoginConfig(msg.Data)
		case protocol.MessageTypeUninstall:
			go a.handleUninstall()
		case protocol.MessageTypeAck:
			a.handleAck(msg.Data)
		default:
			// 忽略其他类型
		}
	}
}

func (a *Agent) pingLoop(ctx context.Context, conn *safeConn, done chan struct{}) error {
	ticker := time.NewTicker(agentPingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(agentWriteWait)); err != nil {
				return err
			}
		case <-done:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

// registerAgent 注册探针，返回服务端的注册响应（含可靠投递协商结果）
func (a *Agent) registerAgent(conn *safeConn) (protocol.RegisterResponse, error) {
	var registerResp protocol.RegisterResponse

	// 加载或生成探针 ID
	agentID, err := a.idMgr.Load()
	if err != nil {
		return registerResp, fmt.Errorf("load agent ID failed: %w", err)
	}
	slog.Info("Agent ID", "id", agentID, "path", a.idMgr.GetPath())

	// 获取主机信息
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}

	// 使用配置或默认值
	agentName := a.cfg.Agent.Name
	if agentName == "" {
		agentName = hostname
	}

	// 构建注册请求（BootID 标识本次探针进程，服务端据此识别重启并
	// 重置事件流确认位点——纯内存 outbox 重启后 seq 从 1 重新分配）
	registerReq := protocol.RegisterRequest{
		AgentInfo: protocol.AgentInfo{
			ID:       agentID,
			Name:     agentName,
			Hostname: hostname,
			OS:       runtime.GOOS,
			Arch:     runtime.GOARCH,
			Version:  GetVersion(),
		},
		ApiKey: a.cfg.Server.APIKey,
		BootID: a.bootID,
	}

	if err := conn.WriteJSON(protocol.OutboundMessage{
		Type: protocol.MessageTypeRegister,
		Data: registerReq,
	}); err != nil {
		return registerResp, fmt.Errorf("send register message failed: %w", err)
	}

	// 读取注册响应
	var response protocol.InputMessage
	if err := conn.ReadJSON(&response); err != nil {
		return registerResp, fmt.Errorf("read register response failed: %w", err)
	}

	// 检查响应类型
	if response.Type == protocol.MessageTypeRegisterErr {
		var errResp protocol.RegisterResponse
		if err := json.Unmarshal(response.Data, &errResp); err == nil {
			return registerResp, fmt.Errorf("register failed: %s", errResp.Message)
		}
		return registerResp, fmt.Errorf("register failed: unknown error")
	}

	if response.Type != protocol.MessageTypeRegisterAck {
		return registerResp, fmt.Errorf("register failed: unexpected response type %s", response.Type)
	}

	if err := json.Unmarshal(response.Data, &registerResp); err != nil {
		return registerResp, fmt.Errorf("parse register response failed: %w", err)
	}

	slog.Info("注册成功", "agentId", registerResp.AgentID, "status", registerResp.Status,
		"reliable", registerResp.Reliable, "serverAckSeq", registerResp.AckSeq)
	return registerResp, nil
}

// handleAck 处理服务端的累计确认，修剪已投递的事件消息
func (a *Agent) handleAck(data json.RawMessage) {
	var ack protocol.AckData
	if err := json.Unmarshal(data, &ack); err != nil {
		slog.Warn("解析投递确认失败", "error", err)
		return
	}
	a.outbox.ack(ack.Seq)
}

// eventSendLoop 是单连接 outbox 发送器。可靠模式下由服务端 ACK 修剪；
// 兼容旧服务端时以 WebSocket 写成功作为本地确认，至少保证断线期间已经
// 入队的消息会在降级连接上继续发送。
func (a *Agent) eventSendLoop(done chan struct{}, conn *safeConn, reliable bool) {
	if err := sendOutboxLoop(a.outbox, done, conn, reliable); err != nil {
		slog.Warn("事件消息发送失败，等待重连后重放", "error", err)
		a.killConn(conn)
	}
}

type jsonWriter interface {
	WriteJSON(v interface{}) error
}

// sendOutboxLoop 是与连接生命周期无关的发送算法，便于独立验证可靠和
// 旧版降级语义。写失败不修改待确认消息，由调用方关闭连接并重连。
func sendOutboxLoop(outbox *outbox, done <-chan struct{}, writer jsonWriter, reliable bool) error {
	var lastSent uint64
	for {
		for _, msg := range outbox.after(lastSent) {
			if err := writer.WriteJSON(msg); err != nil {
				return fmt.Errorf("send outbox seq %d: %w", msg.Seq, err)
			}
			lastSent = msg.Seq
			if !reliable {
				outbox.ack(msg.Seq)
			}
		}
		if !outbox.wait(done) {
			return nil
		}
	}
}

// killConn 写失败时立即关闭连接，让读循环马上感知并触发重连，而不是
// 等 30 秒 pong 超时。Close 可安全并发调用，会以错误唤醒阻塞中的读写。
func (a *Agent) killConn(conn *safeConn) {
	if conn == nil {
		return
	}
	_ = conn.Close()
}

func (a *Agent) handleMonitorConfig(data json.RawMessage) {
	var payload protocol.MonitorConfigPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		slog.Warn("解析监控配置失败", "error", err)
		return
	}

	if len(payload.Items) == 0 {
		slog.Info("收到空的服务监控配置，跳过")
		return
	}

	manager := a.getCollectorManager()
	if manager == nil {
		slog.Warn("采集器未就绪，无法执行服务监控任务")
		return
	}

	slog.Info("收到服务监控配置，立即执行检测", "count", len(payload.Items))

	// 立即执行一次监控检测，结果以单元素 batch 上报。
	// 走常驻 outbox，断线和版本降级期间也不会绕过队列
	sample := manager.CollectMonitor(payload.Items)
	if err := a.sendOutboundMessage(protocol.OutboundMessage{
		Type: protocol.MessageTypeMetrics,
		Data: protocol.MetricsBatch{Samples: []protocol.MetricSample{sample}},
	}); err != nil {
		slog.Warn("发送监控结果失败", "error", err)
	} else {
		slog.Info("服务监控检测完成，已提交监控项结果", "count", len(payload.Items))
	}
}

func (a *Agent) setActiveConn(conn *safeConn) {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	a.activeConn = conn
}

func (a *Agent) getActiveConn() *safeConn {
	a.connMu.RLock()
	defer a.connMu.RUnlock()
	return a.activeConn
}

// sendOutboundMessage 把事件类消息无条件写入常驻 outbox。网络版本协商
// 只影响连接级发送器如何确认，不影响事件是否在断线期间被接收。
func (a *Agent) sendOutboundMessage(msg protocol.OutboundMessage) error {
	a.outbox.enqueue(msg)
	return nil
}

func (a *Agent) setCollectorManager(manager *collector.Manager) {
	a.collectorMu.Lock()
	defer a.collectorMu.Unlock()
	a.collectorManager = manager
}

func (a *Agent) getCollectorManager() *collector.Manager {
	a.collectorMu.RLock()
	defer a.collectorMu.RUnlock()
	return a.collectorManager
}

// metricsSendInterval 指标发送循环的节奏，独立于采集节奏
const metricsSendInterval = 1 * time.Second

// collectLoop 指标采集循环：只采集写快照，不关心连接状态。
// 断线期间采集持续运行，避免重连后数据出现空洞。
func (a *Agent) collectLoop(ctx context.Context) {
	manager := a.getCollectorManager()
	if manager == nil {
		manager = collector.NewManager(a.cfg)
		a.setCollectorManager(manager)
	}
	// 采集器表只构造一次，后续每个 tick 复用
	scheduler := newMetricsScheduler(manager)

	ticker := time.NewTicker(collectorBaseInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			a.collectOnce(scheduler, time.Now())
		case <-ctx.Done():
			return
		}
	}
}

// collectOnce 在超时保护下采集本 tick 到期的指标并写入快照存储。
func (a *Agent) collectOnce(scheduler *metricsScheduler, now time.Time) {
	type collectionResult struct {
		samples  []protocol.MetricSample
		hasError bool
	}
	done := make(chan collectionResult, 1)
	go func() {
		samples, hasError := scheduler.collect(now)
		done <- collectionResult{samples: samples, hasError: hasError}
	}()

	select {
	case result := <-done:
		if len(result.samples) > 0 {
			a.metricsStore.put(result.samples)
		}
		if result.hasError {
			slog.Warn("部分指标采集失败")
		}
	case <-time.After(agentCollectTimeout):
		slog.Warn("数据采集超时", "timeout", agentCollectTimeout)
	}
}

// sendLoop 指标发送循环：独立于采集，定时读取快照发送"新采集"的样本。
func (a *Agent) sendLoop(ctx context.Context) {
	ticker := time.NewTicker(metricsSendInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			a.sendMetricsOnce()
		case <-ctx.Done():
			return
		}
	}
}

// sendMetricsOnce 发送快照中比上次更新的样本；无连接或无新数据时跳过。
func (a *Agent) sendMetricsOnce() {
	conn := a.getActiveConn()
	if conn == nil {
		return
	}

	samples, cursor := a.metricsStore.pending()
	if len(samples) == 0 {
		return
	}

	if err := conn.WriteJSON(protocol.OutboundMessage{
		Type: protocol.MessageTypeMetrics,
		Data: protocol.MetricsBatch{Samples: samples},
	}); err != nil {
		slog.Warn("发送指标 batch 失败", "error", err)
		// 写失败说明连接已不可用，主动断开让读循环立刻触发重连；
		// 未确认样本由 metricsStore 在重连后重发（latest-wins）
		a.killConn(conn)
		return
	}

	a.metricsStore.ack(cursor)
}

// handleCommand 处理服务端下发的指令
func (a *Agent) handleCommand(data json.RawMessage) {
	var cmdReq protocol.CommandRequest
	if err := json.Unmarshal(data, &cmdReq); err != nil {
		slog.Warn("解析指令失败", "error", err)
		return
	}

	slog.Info("收到指令", "type", cmdReq.Type, "id", cmdReq.ID)

	// 发送运行中状态
	a.sendCommandResponse(cmdReq.ID, cmdReq.Type, "running", "", "")

	switch cmdReq.Type {
	case "vps_audit":
		a.handleVPSAudit(cmdReq.ID)
	default:
		slog.Warn("未知指令类型", "type", cmdReq.Type)
		a.sendCommandResponse(cmdReq.ID, cmdReq.Type, "error", "未知指令类型", "")
	}
}

// handleVPSAudit 处理VPS安全审计指令
func (a *Agent) handleVPSAudit(cmdID string) {
	// 导入 audit 包
	result, err := a.runVPSAudit()
	if err != nil {
		slog.Error("VPS安全审计失败", "error", err)
		a.sendCommandResponse(cmdID, "vps_audit", "error", err.Error(), "")
		return
	}

	// 将结果序列化为JSON
	resultJSON, err := json.Marshal(result)
	if err != nil {
		slog.Error("序列化审计结果失败", "error", err)
		a.sendCommandResponse(cmdID, "vps_audit", "error", "序列化结果失败", "")
		return
	}

	slog.Info("VPS安全审计完成")
	a.sendCommandResponse(cmdID, "vps_audit", "success", "", string(resultJSON))
}

// runVPSAudit 运行VPS安全审计
func (a *Agent) runVPSAudit() (*protocol.VPSAuditResult, error) {
	return audit.RunAudit()
}

// sendCommandResponse 发送指令响应
func (a *Agent) sendCommandResponse(cmdID, cmdType, status, errMsg, result string) {
	resp := protocol.CommandResponse{
		ID:     cmdID,
		Type:   cmdType,
		Status: status,
		Error:  errMsg,
		Result: result,
	}

	if err := a.sendOutboundMessage(protocol.OutboundMessage{
		Type: protocol.MessageTypeCommandResp,
		Data: resp,
	}); err != nil {
		slog.Warn("发送指令响应失败", "error", err)
	}
}

// GetVersion 获取 Agent 版本号
func GetVersion() string {
	return version.GetAgentVersion()
}

// handleTamperProtect 处理防篡改保护指令（增量更新）
func (a *Agent) handleTamperProtect(data json.RawMessage) {
	var tamperProtectConfig protocol.TamperProtectConfig
	if err := json.Unmarshal(data, &tamperProtectConfig); err != nil {
		slog.Warn("解析防篡改保护配置失败", "error", err)
		a.sendTamperProtectResponse(false, fmt.Sprintf("解析配置失败: %v", err), nil, nil, nil)
		return
	}

	slog.Info("收到防篡改保护增量配置", "added", tamperProtectConfig.Added, "removed", tamperProtectConfig.Removed)

	conn := a.getActiveConn()
	if conn == nil {
		slog.Warn("当前连接未就绪，无法执行防篡改保护")
		return
	}

	// 如果没有新增也没有移除，不需要做任何操作
	if len(tamperProtectConfig.Added) == 0 && len(tamperProtectConfig.Removed) == 0 {
		slog.Info("配置无变化，跳过更新")
		a.sendTamperProtectResponse(true, "", a.tamperProtector.GetProtectedPaths(), []string{}, []string{})
		return
	}

	ctx := context.Background()

	// 应用增量更新
	result, err := a.tamperProtector.ApplyIncrementalUpdate(ctx, tamperProtectConfig.Added, tamperProtectConfig.Removed)
	if err != nil {
		slog.Warn("应用增量更新失败", "error", err)
		// 即使有错误也返回部分成功的结果
		if result != nil {
			a.sendTamperProtectResponse(false, fmt.Sprintf("应用增量更新失败: %v", err), result.Current, result.Added, result.Removed)
		} else {
			a.sendTamperProtectResponse(false, fmt.Sprintf("应用增量更新失败: %v", err), nil, nil, nil)
		}
		return
	}

	// 成功更新
	message := fmt.Sprintf("防篡改保护已更新: 新增 %d 个, 移除 %d 个, 当前保护 %d 个目录",
		len(result.Added), len(result.Removed), len(result.Current))
	slog.Info(message)
	a.sendTamperProtectResponse(true, message, result.Current, result.Added, result.Removed)
}

// sendTamperProtectResponse 发送防篡改保护响应
func (a *Agent) sendTamperProtectResponse(success bool, message string, paths []string, added []string, removed []string) {
	resp := protocol.TamperProtectResponse{
		Success: success,
		Message: message,
		Paths:   paths,
		Added:   added,
		Removed: removed,
	}

	if err := a.sendOutboundMessage(protocol.OutboundMessage{
		Type: protocol.MessageTypeTamperProtect,
		Data: resp,
	}); err != nil {
		slog.Warn("发送防篡改保护响应失败", "error", err)
	}
}

// tamperEventLoop 防篡改事件监控循环（包含事件和告警）
func (a *Agent) tamperEventLoop(ctx context.Context) {
	eventCh := a.tamperProtector.GetEvents()
	alertCh := a.tamperProtector.GetAlerts()

	for {
		select {
		case <-ctx.Done():
			return
		case event := <-eventCh:
			// 发送防篡改事件到服务端
			eventData := protocol.TamperEventData{
				Path:      event.Path,
				Operation: event.Operation,
				Timestamp: event.Timestamp.UnixMilli(),
				Details:   event.Details,
			}

			if err := a.sendOutboundMessage(protocol.OutboundMessage{
				Type: protocol.MessageTypeTamperEvent,
				Data: eventData,
			}); err != nil {
				slog.Warn("发送防篡改事件失败", "error", err)
			} else {
				slog.Info("已上报防篡改事件", "path", event.Path, "operation", event.Operation)
			}
		case alert := <-alertCh:
			// 将防篡改告警转换为事件格式发送
			eventData := protocol.TamperEventData{
				Path:      alert.Path,
				Operation: "attr_tamper", // 使用特殊的操作类型标识属性篡改
				Timestamp: alert.Timestamp.UnixMilli(),
				Details:   alert.Details,
				Restored:  alert.Restored,
			}

			if err := a.sendOutboundMessage(protocol.OutboundMessage{
				Type: protocol.MessageTypeTamperEvent,
				Data: eventData,
			}); err != nil {
				slog.Warn("发送防篡改告警失败", "error", err)
			} else {
				status := "未恢复"
				if alert.Restored {
					status = "已恢复"
				}
				slog.Info("已上报防篡改告警", "path", alert.Path, "status", status)
			}
		}
	}
}

// handleDDNSConfig 处理 DDNS 配置（服务端定时下发）
func (a *Agent) handleDDNSConfig(data json.RawMessage) {
	var ddnsConfig protocol.DDNSConfigData
	if err := json.Unmarshal(data, &ddnsConfig); err != nil {
		slog.Warn("解析 DDNS 配置失败", "error", err)
		return
	}

	if !ddnsConfig.Enabled {
		slog.Info("DDNS 已禁用，跳过 IP 检查")
		return
	}

	manager := a.getCollectorManager()
	if manager == nil {
		slog.Warn("采集器未就绪，无法执行 DDNS IP 检查")
		return
	}

	slog.Info("收到 DDNS 配置检查请求，开始采集 IP 地址")

	// 采集 IP 地址并上报
	if err := a.collectAndSendDDNSIP(manager, &ddnsConfig); err != nil {
		slog.Warn("DDNS IP 采集失败", "error", err)
	} else {
		slog.Info("DDNS IP 地址已上报")
	}
}

// handlePublicIPConfig 处理公网 IP 采集配置
func (a *Agent) handlePublicIPConfig(data json.RawMessage) {
	var config protocol.PublicIPConfigData
	if err := json.Unmarshal(data, &config); err != nil {
		slog.Warn("解析公网 IP 采集配置失败", "error", err)
		return
	}

	if !config.Enabled || (!config.IPv4Enabled && !config.IPv6Enabled) {
		slog.Info("公网 IP 采集已禁用或未配置")
		return
	}

	manager := a.getCollectorManager()
	if manager == nil {
		manager = collector.NewManager(a.cfg)
		a.setCollectorManager(manager)
	}

	var report protocol.PublicIPReportData

	if config.IPv4Enabled {
		ipv4, err := a.getPublicIPFromAPIs(manager, config.IPv4APIs, false)
		if err != nil {
			slog.Warn("获取 IPv4 失败", "error", err)
		} else {
			report.IPv4 = ipv4
			slog.Info("获取 IPv4", "ip", ipv4)
		}
	}

	if config.IPv6Enabled {
		ipv6, err := a.getPublicIPFromAPIs(manager, config.IPv6APIs, true)
		if err != nil {
			slog.Warn("获取 IPv6 失败", "error", err)
		} else {
			report.IPv6 = ipv6
			slog.Info("获取 IPv6", "ip", ipv6)
		}
	}

	if report.IPv4 == "" && report.IPv6 == "" {
		return
	}

	if err := a.sendOutboundMessage(protocol.OutboundMessage{
		Type: protocol.MessageTypePublicIPReport,
		Data: report,
	}); err != nil {
		slog.Warn("发送公网 IP 报告失败", "error", err)
	}
}

func (a *Agent) getPublicIPFromAPIs(manager *collector.Manager, apis []string, isIPv6 bool) (string, error) {
	if len(apis) == 0 {
		return manager.GetPublicIP("", isIPv6)
	}

	var lastErr error
	for _, api := range apis {
		api = strings.TrimSpace(api)
		if api == "" {
			continue
		}
		if !strings.HasPrefix(api, "http://") && !strings.HasPrefix(api, "https://") {
			lastErr = fmt.Errorf("invalid API URL: %s", api)
			continue
		}
		ip, err := manager.GetPublicIP(api, isIPv6)
		if err == nil && ip != "" {
			return ip, nil
		}
		lastErr = err
	}

	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("failed to get public IP address")
}

// collectAndSendDDNSIP 采集并发送 DDNS IP 地址
func (a *Agent) collectAndSendDDNSIP(manager *collector.Manager, config *protocol.DDNSConfigData) error {
	var ipReport protocol.DDNSIPReportData

	// 采集 IPv4
	if config.EnableIPv4 {
		ipv4, err := a.getIPAddress(manager, config.IPv4GetMethod, config.IPv4GetValue, false)
		if err != nil {
			slog.Warn("获取 IPv4 失败", "error", err)
		} else {
			ipReport.IPv4 = ipv4
			slog.Info("获取 IPv4", "ip", ipv4)
		}
	}

	// 采集 IPv6
	if config.EnableIPv6 {
		ipv6, err := a.getIPAddress(manager, config.IPv6GetMethod, config.IPv6GetValue, true)
		if err != nil {
			slog.Warn("获取 IPv6 失败", "error", err)
		} else {
			ipReport.IPv6 = ipv6
			slog.Info("获取 IPv6", "ip", ipv6)
		}
	}

	// 如果没有获取到任何 IP，返回错误
	if ipReport.IPv4 == "" && ipReport.IPv6 == "" {
		return fmt.Errorf("no IP address was collected")
	}

	if err := a.sendOutboundMessage(protocol.OutboundMessage{
		Type: protocol.MessageTypeDDNSIPReport,
		Data: ipReport,
	}); err != nil {
		return fmt.Errorf("send IP report failed: %w", err)
	}

	return nil
}

// getIPAddress 根据配置获取 IP 地址
func (a *Agent) getIPAddress(manager *collector.Manager, method, value string, isIPv6 bool) (string, error) {
	switch method {
	case "api":
		// 使用 API 获取公网 IP
		return manager.GetPublicIP(value, isIPv6)
	case "interface":
		// 从网络接口获取 IP
		return manager.GetInterfaceIP(value, isIPv6)
	default:
		return "", fmt.Errorf("unsupported IP get method: %s", method)
	}
}

// handleUninstall 处理服务端发送的卸载指令
func (a *Agent) handleUninstall() {
	slog.Info("收到服务端卸载指令，开始执行卸载...")

	// 获取配置文件路径
	cfgPath := a.cfg.Path
	if cfgPath == "" {
		cfgPath = config.GetDefaultConfigPath()
	}

	// 执行卸载操作
	if err := UninstallAgent(cfgPath); err != nil {
		slog.Error("卸载失败", "error", err)
		return
	}

	slog.Info("探针卸载成功，即将退出...")

	// 卸载成功后，触发停止信号
	if a.cancel != nil {
		a.cancel()
	}
}

// handleSSHLoginConfig 处理 SSH 登录监控配置
func (a *Agent) handleSSHLoginConfig(data json.RawMessage) {
	var sshLoginConfig protocol.SSHLoginConfig
	if err := json.Unmarshal(data, &sshLoginConfig); err != nil {
		slog.Warn("解析SSH登录监控配置失败", "error", err)
		// 发送失败结果
		a.sendSSHLoginConfigResult(false, false, err.Error())
		return
	}

	slog.Info("收到SSH登录监控配置", "enabled", sshLoginConfig.Enabled)

	// 应用配置
	ctx := context.Background()
	if err := a.sshMonitor.Start(ctx, sshLoginConfig); err != nil {
		slog.Warn("应用SSH登录监控配置失败", "error", err)
		// 发送失败结果，包含详细错误信息
		a.sendSSHLoginConfigResult(false, sshLoginConfig.Enabled, err.Error())
		return
	}

	// 发送成功结果
	message := "配置已成功应用"
	if sshLoginConfig.Enabled {
		message = "SSH登录监控已启用"
	} else {
		message = "SSH登录监控已禁用"
	}
	a.sendSSHLoginConfigResult(true, sshLoginConfig.Enabled, message)
}

// sendSSHLoginConfigResult 发送 SSH 登录监控配置应用结果
func (a *Agent) sendSSHLoginConfigResult(success bool, enabled bool, message string) {
	result := protocol.SSHLoginConfigResult{
		Success: success,
		Enabled: enabled,
		Message: message,
	}

	msg := protocol.OutboundMessage{
		Type: protocol.MessageTypeSSHLoginConfigResult,
		Data: result,
	}

	if err := a.sendOutboundMessage(msg); err != nil {
		slog.Warn("发送SSH登录监控配置应用结果失败", "error", err)
	}
}

// sshLoginEventLoop SSH登录事件监控循环
func (a *Agent) sshLoginEventLoop(ctx context.Context) {
	eventCh := a.sshMonitor.GetEvents()

	for {
		select {
		case <-ctx.Done():
			return
		case event := <-eventCh:
			// 上报到服务端
			if err := a.sendOutboundMessage(protocol.OutboundMessage{
				Type: protocol.MessageTypeSSHLoginEvent,
				Data: event,
			}); err != nil {
				slog.Warn("发送SSH登录事件失败", "error", err)
			} else {
				slog.Info("已上报SSH登录事件", "user", event.Username, "ip", event.IP, "status", event.Status)
			}
		}
	}
}
