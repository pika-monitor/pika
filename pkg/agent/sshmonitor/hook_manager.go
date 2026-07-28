package sshmonitor

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	PAMConfigFile   = "/etc/pam.d/sshd"
	HookBinaryPath  = "/usr/local/bin/pika-agent"
	HookCommand     = "ssh-login-hook"
	SSHDConfigFile  = "/etc/ssh/sshd_config"
	UsePAMDirective = "UsePAM yes"
)

// HookManager PAM Hook 管理器
type HookManager struct{}

// NewHookManager 创建管理器
func NewHookManager() *HookManager {
	return &HookManager{}
}

func pamConfigLine() string {
	return fmt.Sprintf("session optional pam_exec.so %s %s", HookBinaryPath, HookCommand)
}

func legacyPamConfigLine() string {
	return "session optional pam_exec.so /usr/local/bin/pika_ssh_hook.sh"
}

// Install 安装 PAM Hook
func (h *HookManager) Install() error {
	// 检查是否为 root 用户
	if os.Geteuid() != 0 {
		return fmt.Errorf("%w: 安装 PAM Hook 需要 root 权限", os.ErrPermission)
	}

	// 检查是否已安装（幂等性）
	if h.isInstalled() {
		slog.Info("PAM Hook 已安装，跳过")
		return nil
	}

	// 确保 SSH 配置启用 PAM
	if err := h.ensureUsePAM(); err != nil {
		return fmt.Errorf("配置 SSH UsePAM 失败: %w", err)
	}

	// 安装 Hook 可执行文件（软链接）
	if err := h.ensureHookBinary(); err != nil {
		return fmt.Errorf("安装 Hook 可执行文件失败: %w", err)
	}

	// 修改 PAM 配置
	if err := h.modifyPAMConfig(true); err != nil {
		// 回滚 Hook 可执行文件
		_ = h.removeHookBinary()
		return fmt.Errorf("修改 PAM 配置失败: %w", err)
	}

	slog.Info("PAM Hook 安装成功")
	return nil
}

// Uninstall 卸载 PAM Hook
func (h *HookManager) Uninstall() error {
	// 仅在配置确实存在时改写 PAM 文件，避免未启用过 SSH 监控时产生备份等副作用。
	installed, legacyInstalled, err := h.installedConfigState()
	if err != nil {
		return fmt.Errorf("检查 PAM 配置失败: %w", err)
	}
	if installed || legacyInstalled {
		if err := h.modifyPAMConfig(false); err != nil {
			return fmt.Errorf("移除 PAM 配置失败: %w", err)
		}
	}

	if err := h.removeHookBinary(); err != nil {
		return fmt.Errorf("移除 PAM Hook 可执行文件失败: %w", err)
	}

	slog.Info("PAM Hook 卸载成功")
	return nil
}

func (h *HookManager) installedConfigState() (installed, legacyInstalled bool, err error) {
	f, err := os.Open(PAMConfigFile)
	if err != nil {
		if os.IsNotExist(err) {
			return false, false, nil
		}
		return false, false, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch line {
		case pamConfigLine():
			installed = true
		case legacyPamConfigLine():
			legacyInstalled = true
		}
	}

	if err := scanner.Err(); err != nil {
		return false, false, err
	}
	return installed, legacyInstalled, nil
}

// isInstalled 检查是否已安装
func (h *HookManager) isInstalled() bool {
	installed, _, _ := h.installedConfigState()
	return installed
}

// ensureHookBinary 确保 Hook 可执行文件存在
func (h *HookManager) ensureHookBinary() error {
	execPath, err := os.Executable()
	if err != nil {
		return err
	}

	if execPath == HookBinaryPath {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(HookBinaryPath), 0755); err != nil {
		return err
	}

	if info, err := os.Lstat(HookBinaryPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			if target, err := os.Readlink(HookBinaryPath); err == nil && target == execPath {
				return nil
			}
			if err := os.Remove(HookBinaryPath); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("Hook 路径已存在普通文件，拒绝覆盖: %s", HookBinaryPath)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	if err := os.Symlink(execPath, HookBinaryPath); err != nil {
		return fmt.Errorf("创建 Hook 软链接失败: %w", err)
	}
	return nil
}

func (h *HookManager) removeHookBinary() error {
	return removeHookSymlink(HookBinaryPath)
}

func removeHookSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return os.Remove(path)
	}

	// 兼容旧版本：普通文件可能是主程序、旧版复制品或用户文件，无法确认所有权时保留。
	return nil
}

// modifyPAMConfig 修改 PAM 配置
// add: true=添加配置, false=移除配置
func (h *HookManager) modifyPAMConfig(add bool) error {
	// 读取现有配置
	f, err := os.Open(PAMConfigFile)
	if err != nil {
		return err
	}

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	f.Close()

	if err := scanner.Err(); err != nil {
		return err
	}

	// 修改配置
	var newLines []string
	found := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// 清理旧版本配置行，避免重复
		if trimmed == legacyPamConfigLine() {
			continue
		}

		// 跳过我们的配置行（移除模式）或检测是否已存在（添加模式）
		if trimmed == pamConfigLine() {
			found = true
			if add {
				newLines = append(newLines, line) // 保留
			}
			// 移除模式：不添加到 newLines
			continue
		}

		newLines = append(newLines, line)
	}

	// 如果是添加模式且未找到，则添加到文件末尾
	if add && !found {
		newLines = append(newLines, pamConfigLine())
	}

	// 备份原配置
	backupPath := PAMConfigFile + ".bak"
	if err := os.Rename(PAMConfigFile, backupPath); err != nil {
		return err
	}

	// 写入新配置
	outF, err := os.Create(PAMConfigFile)
	if err != nil {
		// 恢复备份
		os.Rename(backupPath, PAMConfigFile)
		return err
	}
	defer outF.Close()

	writer := bufio.NewWriter(outF)
	for _, line := range newLines {
		writer.WriteString(line + "\n")
	}
	if err := writer.Flush(); err != nil {
		// 恢复备份
		outF.Close()
		os.Rename(backupPath, PAMConfigFile)
		return err
	}

	// 设置正确的权限
	os.Chmod(PAMConfigFile, 0644)

	slog.Info("PAM 配置修改成功", "add", add)
	return nil
}

// ensureUsePAM 确保 SSH 配置启用 PAM
func (h *HookManager) ensureUsePAM() error {
	// 读取现有配置
	f, err := os.Open(SSHDConfigFile)
	if err != nil {
		return err
	}

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	f.Close()

	if err := scanner.Err(); err != nil {
		return err
	}

	// 检查和修改配置
	var newLines []string
	usePAMEnabled := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// 检查是否是 UsePAM 配置行
		if strings.HasPrefix(trimmed, "UsePAM") {
			// 检查是否已启用
			if strings.Contains(trimmed, "yes") && !strings.HasPrefix(trimmed, "#") {
				usePAMEnabled = true
				newLines = append(newLines, line)
			} else {
				// 注释掉旧的配置行
				if !strings.HasPrefix(trimmed, "#") {
					newLines = append(newLines, "# "+line)
				} else {
					newLines = append(newLines, line)
				}
			}
			continue
		}

		newLines = append(newLines, line)
	}

	// 如果 UsePAM 已正确启用,无需修改
	if usePAMEnabled {
		slog.Info("SSH UsePAM 已启用，跳过")
		return nil
	}

	// 如果找到了 UsePAM 但未启用,或者没找到,都需要添加配置
	newLines = append(newLines, UsePAMDirective)

	// 备份原配置
	backupPath := SSHDConfigFile + ".bak"
	if err := os.Rename(SSHDConfigFile, backupPath); err != nil {
		return err
	}

	// 写入新配置
	outF, err := os.Create(SSHDConfigFile)
	if err != nil {
		// 恢复备份
		os.Rename(backupPath, SSHDConfigFile)
		return err
	}
	defer outF.Close()

	writer := bufio.NewWriter(outF)
	for _, line := range newLines {
		writer.WriteString(line + "\n")
	}
	if err := writer.Flush(); err != nil {
		// 恢复备份
		outF.Close()
		os.Rename(backupPath, SSHDConfigFile)
		return err
	}

	// 设置正确的权限
	os.Chmod(SSHDConfigFile, 0600)

	slog.Info("SSH UsePAM 配置成功")

	// 重启 sshd 服务使配置生效
	if err := h.restartSSHD(); err != nil {
		slog.Warn("重启 sshd 服务失败，请手动重启", "error", err)
		// 不返回错误，因为配置已经成功，只是需要手动重启
	}

	return nil
}

// restartSSHD 重启 sshd 服务
func (h *HookManager) restartSSHD() error {
	// 尝试使用 systemctl (systemd)
	cmd := exec.Command("systemctl", "restart", "sshd")
	if err := cmd.Run(); err == nil {
		slog.Info("已通过 systemctl 重启 sshd 服务")
		return nil
	}

	// 尝试 service 命令 (SysV init)
	cmd = exec.Command("service", "sshd", "restart")
	if err := cmd.Run(); err == nil {
		slog.Info("已通过 service 重启 sshd 服务")
		return nil
	}

	// 尝试直接重启 (某些系统可能使用 ssh 而不是 sshd)
	cmd = exec.Command("systemctl", "restart", "ssh")
	if err := cmd.Run(); err == nil {
		slog.Info("已通过 systemctl 重启 ssh 服务")
		return nil
	}

	cmd = exec.Command("service", "ssh", "restart")
	if err := cmd.Run(); err == nil {
		slog.Info("已通过 service 重启 ssh 服务")
		return nil
	}

	return fmt.Errorf("无法找到合适的方式重启 sshd 服务")
}
