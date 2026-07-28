package handler

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/dushixiang/pika/internal/assets"
	"github.com/dushixiang/pika/pkg/version"
	"github.com/go-orz/orz"
	"github.com/labstack/echo/v5"
	"go.uber.org/zap"
)

// GetAgentVersion 获取 Agent 版本信息
func (h *AgentHandler) GetAgentVersion(c *echo.Context) error {
	return orz.Ok(c, orz.Map{
		"version": version.GetAgentVersion(),
	})
}

// DownloadAgent 下载 Agent 二进制文件
func (h *AgentHandler) DownloadAgent(c *echo.Context) error {
	filename := c.Param("filename")
	if filename == "" || strings.ContainsAny(filename, `/\`) {
		return orz.NewError(400, "无效的 Agent 文件名")
	}

	// 校验 API Key
	apiKey := c.QueryParam("key")
	if _, err := h.apiKeyService.ValidateApiKey(c.Request().Context(), apiKey, "agent"); err != nil {
		// API Key 校验失败，尝试 IP 白名单兜底（兼容旧版 Agent 自动更新）
		clientIP := c.RealIP()
		isOnline, checkErr := h.agentService.IsAgentByIP(c.Request().Context(), clientIP)
		if checkErr != nil {
			h.logger.Error("check agent online by ip failed", zap.String("ip", clientIP), zap.Error(checkErr))
			return orz.NewError(500, "系统内部错误")
		}

		if !isOnline {
			h.logger.Warn("download agent failed: invalid api key and ip not whitelisted",
				zap.String("key", apiKey),
				zap.String("ip", clientIP))
			return orz.NewError(401, "无效的 API 密钥，且来源 IP 未在白名单中")
		}
		h.logger.Info("download agent allowed by ip whitelist", zap.String("ip", clientIP), zap.String("filename", filename))
	}

	agentFilename := fmt.Sprintf("pika-%s", filename)
	agentFile, err := os.Open(assets.AgentPath(agentFilename))
	if err != nil {
		h.logger.Error("agent binary not found", zap.String("filename", filename), zap.Error(err))
		return orz.NewError(404, "未找到对应平台的 Agent 二进制文件")
	}
	defer agentFile.Close()

	// 设置响应头
	c.Response().Header().Set("Content-Type", "application/octet-stream")
	c.Response().Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))

	return c.Stream(http.StatusOK, "application/octet-stream", agentFile)
}

// getServerURL 获取服务器地址（仅从配置读取）
func (h *AgentHandler) getServerURL(c *echo.Context) string {
	// 优先读取配置的服务端地址
	config, err := h.propertyService.GetAgentInstallConfig(c.Request().Context())
	if err == nil && config.ServerURL != "" {
		return config.ServerURL
	}

	return ""
}

func bashSingleQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func (h *AgentHandler) GetServerUrl(c *echo.Context) error {
	serverUrl := h.getServerURL(c)
	return orz.Ok(c, orz.Map{
		"serverUrl": serverUrl,
	})
}

// GetInstallScript 生成自动安装脚本
func (h *AgentHandler) GetInstallScript(c *echo.Context) error {
	token := c.QueryParam("token")
	if token == "" {
		return orz.NewError(400, "token不能为空")
	}

	// 使用统一的 getServerURL 函数获取服务器地址（仅从配置读取）
	serverUrl := strings.TrimSpace(h.getServerURL(c))
	if serverUrl == "" {
		return orz.NewError(400, "请先配置服务端地址")
	}
	customName := strings.TrimSpace(c.QueryParam("name"))
	customNameLiteral := bashSingleQuote(customName)

	script := `#!/bin/bash
set -e

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

echo_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

echo_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

# 解析参数
AGENT_NAME_CUSTOM=` + customNameLiteral + `
while [[ $# -gt 0 ]]; do
    case $1 in
        --name)
            AGENT_NAME_CUSTOM="$2"
            shift # past argument
            shift # past value
            ;;
        *)
            shift # past argument
            ;;
    esac
done

if [ -n "$AGENT_NAME_CUSTOM" ]; then
    echo_info "使用自定义探针名称: $AGENT_NAME_CUSTOM"
fi

# 检测操作系统和架构
detect_platform() {
    OS=$(uname -s | awk '{print tolower($0)}')
    ARCH=$(uname -m)

    case "$ARCH" in
        x86_64)
            ARCH="amd64"
            ;;
        aarch64|arm64|armv8*)
            ARCH="arm64"
            ;;
        armv7*|armv7l)
            ARCH="armv7"
            ;;
        loongarch64)
            ARCH="loong64"
            ;;
        *)
            echo_error "不支持的架构: $ARCH"
            exit 1
            ;;
    esac

    case "$OS" in
        linux)
            PLATFORM="linux-$ARCH"
            AGENT_NAME="pika-agent"
            ;;
        darwin)
            PLATFORM="darwin-$ARCH"
            AGENT_NAME="pika-agent"
            ;;
        *)
            echo_error "不支持的操作系统: $OS"
            exit 1
            ;;
    esac

    echo_info "检测到平台: $PLATFORM"
}

# 下载探针
download_agent() {
    local download_url="` + serverUrl + `/api/agent/downloads/agent-$PLATFORM?key=` + token + `"
    local temp_file="/tmp/pika-agent-download"

    echo_info "正在下载探针..."

    if command -v curl &> /dev/null; then
        curl -# -L "$download_url" -o "$temp_file"
    elif command -v wget &> /dev/null; then
        wget -q "$download_url" -O "$temp_file"
    else
        echo_error "未找到 wget 或 curl 命令，请先安装其中之一"
        exit 1
    fi

    if [ ! -f "$temp_file" ]; then
        echo_error "下载失败"
        exit 1
    fi

    # 移动到目标位置
	mkdir -p /usr/local/bin
    mv "$temp_file" "/usr/local/bin/$AGENT_NAME"
    chmod +x "/usr/local/bin/$AGENT_NAME"

    echo_info "探针下载完成: /usr/local/bin/$AGENT_NAME"
}

# 注册并启动服务
register_agent() {
    local endpoint="` + serverUrl + `"
    local token="` + token + `"

    echo_info "正在注册探针..."
    if [ -n "$AGENT_NAME_CUSTOM" ]; then
        /usr/local/bin/$AGENT_NAME register --endpoint "$endpoint" --token "$token" --name "$AGENT_NAME_CUSTOM" --yes
    else
        /usr/local/bin/$AGENT_NAME register --endpoint "$endpoint" --token "$token" --yes
    fi
}

# 主流程
main() {
    echo_info "开始安装 Pika Agent..."
    echo ""

    detect_platform
    download_agent
    register_agent

    echo ""
    echo_info "=========================================="
    echo_info "安装完成！"
    echo_info "=========================================="
    echo ""
    echo_info "常用命令："
    echo "  查看状态: pika-agent status"
    echo "  启动服务: pika-agent start"
    echo "  停止服务: pika-agent stop"
    echo "  重启服务: pika-agent restart"
    echo "  卸载服务: pika-agent uninstall"
    echo ""
}

main`

	c.Response().Header().Set("Content-Type", "text/plain; charset=utf-8")
	return c.String(http.StatusOK, script)
}
