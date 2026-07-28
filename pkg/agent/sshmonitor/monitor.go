package sshmonitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/dushixiang/pika/internal/protocol"
)

const (
	DefaultSocketDir  = "/run/pika"
	DefaultSocketPath = "/run/pika/ssh_login.sock"
)

// Monitor SSH登录监控器
type Monitor struct {
	mu          sync.RWMutex
	enabled     bool
	socketPath  string
	listener    *net.UnixConn
	ctx         context.Context
	cancel      context.CancelFunc
	eventCh     chan protocol.SSHLoginEvent
	hookManager *HookManager
}

// NewMonitor 创建监控器
func NewMonitor() *Monitor {
	return &Monitor{
		enabled:     false,
		socketPath:  DefaultSocketPath,
		eventCh:     make(chan protocol.SSHLoginEvent, 100),
		hookManager: NewHookManager(),
	}
}

// Start 启动监控（根据配置启用/禁用）
func (m *Monitor) Start(ctx context.Context, config protocol.SSHLoginConfig) error {
	// 检查操作系统
	if runtime.GOOS != "linux" {
		return fmt.Errorf("SSH登录监控仅支持 Linux 系统")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// 如果已启动，先停止
	if m.enabled {
		_ = m.stopInternal()
	}

	if !config.Enabled {
		slog.Info("SSH登录监控已禁用")
		return nil
	}

	if err := m.initSocket(ctx); err != nil {
		return err
	}
	m.enabled = true

	// 安装 PAM Hook
	if err := m.hookManager.Install(); err != nil {
		if os.IsPermission(err) {
			slog.Warn("权限不足，无法自动安装 PAM Hook", "error", err)
			slog.Info("如需启用 SSH 登录监控，请手动执行安装")
			// 不返回错误，继续监控（假设已手动安装）
		} else {
			slog.Warn("安装 PAM Hook 失败", "error", err)
			_ = m.stopInternal()
			return err
		}
	}

	slog.Info("SSH登录监控已启动", "socket", m.socketPath)
	return nil
}

// Stop 停止监控
func (m *Monitor) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopInternal()
}

// Uninstall 清理 SSH 登录监控安装的持久配置和运行时文件。
func (m *Monitor) Uninstall() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.enabled {
		if err := m.stopInternal(); err != nil {
			return err
		}
	} else {
		if err := os.Remove(m.socketPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("移除 socket 失败: %w", err)
		}
		if err := m.hookManager.Uninstall(); err != nil {
			return err
		}
	}

	if err := os.Remove(DefaultSocketDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("移除 socket 目录失败: %w", err)
	}

	return nil
}

// stopInternal 内部停止方法（不加锁）
func (m *Monitor) stopInternal() error {
	if !m.enabled {
		return nil
	}

	// 取消 context
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}

	// 关闭 socket
	if m.listener != nil {
		_ = m.listener.Close()
		m.listener = nil
	}

	var cleanupErr error
	if err := os.Remove(m.socketPath); err != nil && !os.IsNotExist(err) {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("移除 socket 失败: %w", err))
	}

	// 卸载 PAM Hook
	if err := m.hookManager.Uninstall(); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("卸载 PAM Hook 失败: %w", err))
	}

	m.enabled = false
	slog.Info("SSH登录监控已停止")
	return cleanupErr
}

// GetEvents 获取事件通道
func (m *Monitor) GetEvents() <-chan protocol.SSHLoginEvent {
	return m.eventCh
}

// initSocket 初始化 socket 监听
func (m *Monitor) initSocket(ctx context.Context) error {
	if err := os.MkdirAll(DefaultSocketDir, 0755); err != nil {
		return fmt.Errorf("创建 socket 目录失败: %w", err)
	}

	if err := os.Remove(m.socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("清理旧 socket 失败: %w", err)
	}

	addr := &net.UnixAddr{Name: m.socketPath, Net: "unixgram"}
	conn, err := net.ListenUnixgram("unixgram", addr)
	if err != nil {
		return fmt.Errorf("监听 socket 失败: %w", err)
	}

	if err := os.Chmod(m.socketPath, 0600); err != nil {
		slog.Warn("设置 socket 权限失败", "error", err, "path", m.socketPath)
	}

	m.ctx, m.cancel = context.WithCancel(ctx)
	m.listener = conn

	go m.readLoop()

	slog.Info("SSH登录 socket 已启动", "path", m.socketPath)
	return nil
}

func (m *Monitor) readLoop() {
	buf := make([]byte, 4096)
	for {
		n, _, err := m.listener.ReadFromUnix(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed) {
				return
			}
			select {
			case <-m.ctx.Done():
				return
			default:
			}
			slog.Warn("读取SSH登录事件失败", "error", err)
			continue
		}

		if n == 0 {
			continue
		}

		var event protocol.SSHLoginEvent
		if err := json.Unmarshal(buf[:n], &event); err != nil {
			slog.Warn("解析SSH登录事件失败", "error", err)
			continue
		}

		if event.Timestamp == 0 {
			event.Timestamp = time.Now().UnixMilli()
		}
		if event.Status == "" {
			event.Status = "success"
		}

		select {
		case m.eventCh <- event:
			slog.Info("检测到SSH登录", "user", event.Username, "ip", event.IP, "status", event.Status)
		default:
			slog.Warn("事件队列已满，丢弃事件")
		}
	}
}
