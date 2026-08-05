package updater

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/dushixiang/pika/pkg/agent/config"
	"github.com/minio/selfupdate"
	"path/filepath"
	"strings"
	"sync"
)

// VersionInfo 版本信息
type VersionInfo struct {
	Version string `json:"version"`
}

// Updater 自动更新器
type Updater struct {
	cfg            *config.Config
	currentVer     string
	httpClient     *http.Client
	executablePath string
	mu             sync.Mutex
	updating       bool
}

// New 创建更新器
func New(cfg *config.Config, currentVer string) (*Updater, error) {
	execPath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("获取可执行文件路径失败: %w", err)
	}

	// 创建 HTTP 客户端，根据配置决定是否跳过证书验证
	httpClient := &http.Client{
		Timeout: 60 * time.Second,
	}
	if cfg.Server.InsecureSkipVerify {
		httpClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		}
	}

	return &Updater{
		cfg:            cfg,
		currentVer:     currentVer,
		httpClient:     httpClient,
		executablePath: execPath,
	}, nil
}

// Start 启动自动更新检查
func (u *Updater) Start(ctx context.Context) {
	if !u.cfg.AutoUpdate.Enabled {
		slog.Info("自动更新已禁用")
		return
	}

	slog.Info("自动更新已启用", "check_interval", u.cfg.GetUpdateCheckInterval())

	// 立即检查一次
	u.CheckAndUpdate()

	// 定时检查
	ticker := time.NewTicker(u.cfg.GetUpdateCheckInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			u.CheckAndUpdate()
		case <-ctx.Done():
			slog.Info("停止自动更新检查")
			return
		}
	}
}

// CheckAndUpdate 检查并更新
func (u *Updater) CheckAndUpdate() {
	slog.Debug("检查更新...")

	// 获取最新版本信息
	versionInfo, err := u.fetchLatestVersion()
	if err != nil {
		slog.Warn("获取版本信息失败", "error", err)
		return
	}

	// 比较版本
	if versionInfo.Version == u.currentVer {
		slog.Debug("当前已是最新版本", "version", u.currentVer)
		return
	}

	slog.Info("发现新版本", "new_version", versionInfo.Version, "current_version", u.currentVer)

	// 加锁防止并发更新
	u.mu.Lock()
	if u.updating {
		u.mu.Unlock()
		slog.Warn("更新已在进行中，跳过本次触发")
		return
	}
	u.updating = true
	u.mu.Unlock()

	defer func() {
		u.mu.Lock()
		u.updating = false
		u.mu.Unlock()
	}()

	// 下载新版本
	if err := u.downloadAndUpdate(versionInfo); err != nil {
		slog.Error("更新失败", "error", err)
		return
	}

	slog.Info("更新成功，将在下次重启时生效")
}

// fetchLatestVersion 获取最新版本信息
func (u *Updater) fetchLatestVersion() (*VersionInfo, error) {
	latestVersionURL := u.cfg.GetLatestVersionURL()
	return u.checkUpdateWithClient(latestVersionURL)
}

// checkUpdateWithClient 使用实例的 httpClient 检查更新
func (u *Updater) checkUpdateWithClient(latestVersionURL string) (*VersionInfo, error) {
	resp, err := u.httpClient.Get(latestVersionURL)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP 状态码: %d", resp.StatusCode)
	}

	var versionInfo VersionInfo
	if err := json.NewDecoder(resp.Body).Decode(&versionInfo); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}

	return &versionInfo, nil
}

// downloadAndUpdate 下载并更新
func (u *Updater) downloadAndUpdate(versionInfo *VersionInfo) error {
	slog.Info("下载新版本", "version", versionInfo.Version)

	downloadURL := u.cfg.GetDownloadURL()

	// 下载文件
	resp, err := u.httpClient.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP 状态码: %d", resp.StatusCode)
	}

	// 检查当前运行的文件名，避免在 .old 文件中运行并尝试更新
	if strings.HasSuffix(u.executablePath, ".old") {
		return fmt.Errorf("当前程序正在以备份模式 (.old) 运行，跳过自动更新以避免递归错误: %s", u.executablePath)
	}

	// 检查并清理旧的备份文件，避免 selfupdate 因旧文件存在而产生的冲突（如构建 .old.old 路径失败）
	targetPath := u.executablePath
	oldPath := filepath.Join(filepath.Dir(targetPath), "."+filepath.Base(targetPath)+".old")
	if _, err := os.Stat(oldPath); err == nil {
		slog.Info("发现旧的备份文件，正在清理以确保更新顺利", "path", oldPath)
		if removeErr := os.Remove(oldPath); removeErr != nil {
			slog.Warn("清理旧备份文件失败 (非致命)", "error", removeErr)
		}
	}

	// 使用 selfupdate 应用更新
	if err := selfupdate.Apply(resp.Body, selfupdate.Options{
		TargetPath: targetPath,
	}); err != nil {
		return fmt.Errorf("应用更新失败: %w", err)
	}

	slog.Info("更新成功，进程即将退出，等待系统服务重启...")

	// 退出当前进程，让系统服务管理器（systemd/supervisor等）自动重启
	// 注意：这要求服务配置了自动重启（如 systemd 的 Restart=always）
	os.Exit(1)

	return nil
}
