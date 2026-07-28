package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dushixiang/pika/internal/models"
	"github.com/dushixiang/pika/internal/utils"
	"github.com/go-orz/cache"
	"github.com/valyala/fasttemplate"
	"go.uber.org/zap"
	"gopkg.in/gomail.v2"
)

const (
	maxRetries     = 3
	retryBaseDelay = 1 * time.Second
)

var sharedHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	},
}

// AlertTypeMetadata 告警类型元数据
type AlertTypeMetadata struct {
	Name          string // 中文名称
	ThresholdUnit string // 阈值单位
	ValueUnit     string // 当前值单位
	ShowThreshold bool   // 是否显示阈值
	ShowActual    bool   // 是否显示当前值
}

// 告警类型元数据映射
var alertTypeMetadataMap = map[string]AlertTypeMetadata{
	"cpu": {
		Name:          "CPU告警",
		ThresholdUnit: "%",
		ValueUnit:     "%",
		ShowThreshold: true,
		ShowActual:    true,
	},
	"memory": {
		Name:          "内存告警",
		ThresholdUnit: "%",
		ValueUnit:     "%",
		ShowThreshold: true,
		ShowActual:    true,
	},
	"disk": {
		Name:          "磁盘告警",
		ThresholdUnit: "%",
		ValueUnit:     "%",
		ShowThreshold: true,
		ShowActual:    true,
	},
	"network": {
		Name:          "网络告警",
		ThresholdUnit: "MB/s",
		ValueUnit:     "MB/s",
		ShowThreshold: true,
		ShowActual:    true,
	},
	"traffic": {
		Name:          "流量告警",
		ThresholdUnit: "%",
		ValueUnit:     "%",
		ShowThreshold: true,
		ShowActual:    true,
	},
	"cert": {
		Name:          "证书告警",
		ThresholdUnit: "天",
		ValueUnit:     "天",
		ShowThreshold: true,
		ShowActual:    true,
	},
	"service": {
		Name:          "服务告警",
		ThresholdUnit: "秒",
		ValueUnit:     "秒离线",
		ShowThreshold: false,
		ShowActual:    true,
	},
	"agent_offline": {
		Name:          "探针离线告警",
		ThresholdUnit: "秒",
		ValueUnit:     "秒",
		ShowThreshold: true,
		ShowActual:    true,
	},
	"agent_expire": {
		Name:          "机器过期提醒",
		ThresholdUnit: "天",
		ValueUnit:     "天",
		ShowThreshold: true,
		ShowActual:    true,
	},
	"ssh_login": {
		Name:          "SSH登录成功",
		ThresholdUnit: "",
		ValueUnit:     "",
		ShowThreshold: false,
		ShowActual:    false,
	},
	"tamper": {
		Name:          "防篡改事件",
		ThresholdUnit: "",
		ValueUnit:     "",
		ShowThreshold: false,
		ShowActual:    false,
	},
}

// 告警级别图标映射
var levelIconMap = map[string]string{
	"info":     "ℹ️",
	"warning":  "⚠️",
	"critical": "🚨",
}

// Notifier 告警通知服务
type Notifier struct {
	logger *zap.Logger
}

func NewNotifier(logger *zap.Logger) *Notifier {
	return &Notifier{
		logger: logger,
	}
}

// maskIPAddress 打码 IP 地址 (例如: 192.168.1.100 -> 192.168.*.*）
func maskIPAddress(ip string) string {
	parts := strings.Split(ip, ".")
	if len(parts) == 4 {
		// IPv4: 保留前两段，后两段打码
		return parts[0] + "." + parts[1] + ".*.*"
	}
	// IPv6 或其他格式: 保留前半部分，后半部分打码
	if len(ip) > 8 {
		return ip[:len(ip)/2] + "****"
	}
	return "****"
}

func joinAgentIPs(ip string, ipv4 string, ipv6 string) string {
	parts := make([]string, 0, 3)
	seen := make(map[string]bool)

	// 按优先级添加，避免重复
	if ip != "" && !seen[ip] {
		parts = append(parts, ip)
		seen[ip] = true
	}
	if ipv4 != "" && !seen[ipv4] {
		parts = append(parts, ipv4)
		seen[ipv4] = true
	}
	if ipv6 != "" && !seen[ipv6] {
		parts = append(parts, ipv6)
		seen[ipv6] = true
	}
	return strings.Join(parts, " / ")
}

func formatAgentIP(agent *models.Agent, mask bool) string {
	ip := strings.TrimSpace(agent.IP)
	ipv4 := strings.TrimSpace(agent.IPv4)
	ipv6 := strings.TrimSpace(agent.IPv6)
	if mask {
		if ip != "" {
			ip = maskIPAddress(ip)
		}
		if ipv4 != "" {
			ipv4 = maskIPAddress(ipv4)
		}
		if ipv6 != "" {
			ipv6 = maskIPAddress(ipv6)
		}
	}
	combined := joinAgentIPs(ip, ipv4, ipv6)
	if combined == "" {
		return "-"
	}
	return combined
}

// getAlertTypeMetadata 获取告警类型元数据，如果不存在则返回默认值
func getAlertTypeMetadata(alertType string) AlertTypeMetadata {
	if metadata, ok := alertTypeMetadataMap[alertType]; ok {
		return metadata
	}
	// 返回默认值
	return AlertTypeMetadata{
		Name:          "未知告警",
		ThresholdUnit: "",
		ValueUnit:     "",
		ShowThreshold: true,
		ShowActual:    true,
	}
}

// getLevelIcon 获取告警级别图标，如果不存在则返回默认值
func getLevelIcon(level string) string {
	if icon, ok := levelIconMap[level]; ok {
		return icon
	}
	return "❓" // 未知级别的默认图标
}

// buildMessage 构建告警消息文本
func (n *Notifier) buildMessage(agent *models.Agent, record *models.AlertRecord, maskIP bool) string {
	// 获取告警级别图标
	levelIcon := getLevelIcon(record.Level)

	// 获取告警类型元数据
	metadata := getAlertTypeMetadata(record.AlertType)

	// 处理 IP 地址显示
	displayIP := formatAgentIP(agent, maskIP)

	// 根据状态构建消息
	switch record.Status {
	case "firing":
		return n.buildFiringMessage(agent, record, displayIP, levelIcon, metadata)
	case "resolved":
		return n.buildResolvedMessage(agent, record, displayIP, metadata)
	case "notice":
		return n.buildNoticeMessage(agent, record, displayIP, levelIcon, metadata)
	default:
		// 未知状态，返回基本信息
		return fmt.Sprintf("⚠️ 未知告警状态: %s\n探针: %s (%s)", record.Status, agent.Name, agent.ID)
	}
}

// buildFiringMessage 构建告警触发消息
func (n *Notifier) buildFiringMessage(
	agent *models.Agent,
	record *models.AlertRecord,
	displayIP string,
	levelIcon string,
	metadata AlertTypeMetadata,
) string {
	title := fmt.Sprintf("%s %s", levelIcon, metadata.Name)
	if record.Level != "" && record.Level != "info" {
		title = fmt.Sprintf("%s %s【%s】", levelIcon, metadata.Name, strings.ToUpper(record.Level))
	}

	lines := []string{
		title,
		"",
		fmt.Sprintf("📍 探针: %s", agent.Name),
		fmt.Sprintf("🖥️  主机: %s", agent.Hostname),
		fmt.Sprintf("🌐 IP: %s", displayIP),
		"",
		"⚠️ " + record.Message,
	}

	if metadata.ShowThreshold {
		lines = append(lines, fmt.Sprintf("📊 阈值: %.2f%s", record.Threshold, metadata.ThresholdUnit))
	}

	if metadata.ShowActual {
		lines = append(lines, fmt.Sprintf("📈 当前值: %.2f%s", record.ActualValue, metadata.ValueUnit))
	}

	lines = append(lines, "", fmt.Sprintf("🕐 时间: %s", utils.FormatTimestamp(record.FiredAt)))

	return strings.Join(lines, "\n")
}

// buildResolvedMessage 构建告警恢复消息
func (n *Notifier) buildResolvedMessage(
	agent *models.Agent,
	record *models.AlertRecord,
	displayIP string,
	metadata AlertTypeMetadata,
) string {
	var durationStr string
	if record.FiredAt > 0 && record.ResolvedAt > record.FiredAt {
		durationMs := record.ResolvedAt - record.FiredAt
		durationStr = utils.FormatDuration(durationMs)
	}

	lines := []string{
		fmt.Sprintf("✅ %s已恢复", metadata.Name),
		"",
		fmt.Sprintf("📍 探针: %s", agent.Name),
		fmt.Sprintf("🖥️  主机: %s", agent.Hostname),
		fmt.Sprintf("🌐 IP: %s", displayIP),
		"",
		"✓ " + record.Message,
	}

	if metadata.ShowActual {
		if (record.AlertType == "service" || record.AlertType == "agent_offline") && record.ResolvedValue == 0 {
			lines = append(lines,
				fmt.Sprintf("⏱️  离线时长: %.0f秒", record.ActualValue),
				"✅ 恢复状态: 已在线",
			)
		} else {
			lines = append(lines,
				fmt.Sprintf("📈 告警值: %.2f%s", record.ActualValue, metadata.ValueUnit),
				fmt.Sprintf("📉 恢复值: %.2f%s", record.ResolvedValue, metadata.ValueUnit),
			)
		}
	}

	if durationStr != "" {
		lines = append(lines, fmt.Sprintf("⏱️  持续时间: %s", durationStr))
	}

	lines = append(lines, fmt.Sprintf("🕐 恢复时间: %s", utils.FormatTimestamp(record.ResolvedAt)))

	return strings.Join(lines, "\n")
}

func (n *Notifier) buildNoticeMessage(
	agent *models.Agent,
	record *models.AlertRecord,
	displayIP string,
	levelIcon string,
	metadata AlertTypeMetadata,
) string {
	title := fmt.Sprintf("%s %s", levelIcon, metadata.Name)
	if record.Level != "" && record.Level != "info" {
		title = fmt.Sprintf("%s %s【%s】", levelIcon, metadata.Name, strings.ToUpper(record.Level))
	}

	lines := []string{
		title,
		"",
		fmt.Sprintf("📍 探针: %s", agent.Name),
		fmt.Sprintf("🖥️  主机: %s", agent.Hostname),
		fmt.Sprintf("🌐 IP: %s", displayIP),
		"",
		"ℹ️ " + record.Message,
	}

	if metadata.ShowThreshold {
		lines = append(lines, fmt.Sprintf("📊 阈值: %.2f%s", record.Threshold, metadata.ThresholdUnit))
	}

	if metadata.ShowActual {
		lines = append(lines, fmt.Sprintf("📈 当前值: %.2f%s", record.ActualValue, metadata.ValueUnit))
	}

	lines = append(lines, "", fmt.Sprintf("🕐 时间: %s", utils.FormatTimestamp(record.FiredAt)))

	return strings.Join(lines, "\n")
}

// sendDingTalk 发送钉钉通知
func (n *Notifier) sendDingTalk(ctx context.Context, webhook, secret, message string) error {
	// 构造钉钉消息体
	body := map[string]interface{}{
		"msgtype": "text",
		"text": map[string]string{
			"content": message,
		},
	}

	// 如果有加签密钥，计算签名
	timestamp := time.Now().UnixMilli()
	if secret != "" {
		sign := n.calculateDingTalkSign(timestamp, secret)
		webhook = fmt.Sprintf("%s&timestamp=%d&sign=%s", webhook, timestamp, sign)
	}
	_, err := n.sendJSONRequest(ctx, webhook, body)
	if err != nil {
		return err
	}
	return nil
}

// calculateDingTalkSign 计算钉钉加签
func (n *Notifier) calculateDingTalkSign(timestamp int64, secret string) string {
	stringToSign := fmt.Sprintf("%d\n%s", timestamp, secret)
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

type WeComResult struct {
	Errcode   int    `json:"errcode"`
	Errmsg    string `json:"errmsg"`
	Type      string `json:"type"`
	MediaId   string `json:"media_id"`
	CreatedAt string `json:"created_at"`
}

// sendWeCom 发送企业微信通知
func (n *Notifier) sendWeCom(ctx context.Context, webhook, message string) error {
	body := map[string]interface{}{
		"msgtype": "text",
		"text": map[string]string{
			"content": message,
		},
	}
	result, err := n.sendJSONRequest(ctx, webhook, body)
	if err != nil {
		return err
	}
	var weComResult WeComResult
	if err := json.Unmarshal(result, &weComResult); err != nil {
		return err
	}
	if weComResult.Errcode != 0 {
		return fmt.Errorf("%s", weComResult.Errmsg)
	}
	return nil
}

var wecomAppAccessTokenCache = cache.New[string, string](time.Minute)

func (n *Notifier) getWecomAppToken(ctx context.Context, origin, corpId, corpSecret string) (string, error) {
	key := fmt.Sprintf("%s#%s", corpId, corpSecret)
	if token, found := wecomAppAccessTokenCache.Get(key); found {
		return token, nil
	}

	accessTokenURL := fmt.Sprintf("%s/cgi-bin/gettoken?corpid=%s&corpsecret=%s", origin, corpId, corpSecret)

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, accessTokenURL, nil)
	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
		ExpiresIn   int64  `json:"expires_in"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", err
	}

	if tokenResp.ErrCode != 0 {
		return "", errors.New(tokenResp.ErrMsg)
	}

	token := tokenResp.AccessToken
	expires := time.Duration(tokenResp.ExpiresIn)*time.Second - 2*time.Minute
	wecomAppAccessTokenCache.Set(key, token, expires)
	return token, nil
}

// sendWeComApp 发送企业应用微信通知
func (n *Notifier) sendWeComApp(ctx context.Context, origin, corpId, corpSecret string, agentId int, toUser string, message string) error {
	token, err := n.getWecomAppToken(ctx, origin, corpId, corpSecret)
	if err != nil {
		return fmt.Errorf("获取企业微信应用ACCESS_TOKEN失败：%s", err)
	}

	webhook := fmt.Sprintf("%s/cgi-bin/message/send?access_token=%s", origin, token)

	body := map[string]interface{}{
		"touser":  toUser,
		"msgtype": "text",
		"agentid": agentId,
		"text": map[string]string{
			"content": message,
		},
		"safe": 0,
	}

	result, err := n.sendJSONRequest(ctx, webhook, body)
	if err != nil {
		return err
	}

	var sendRespBody struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}

	if err := json.Unmarshal(result, &sendRespBody); err != nil {
		return err
	}

	if sendRespBody.ErrCode != 0 {
		return fmt.Errorf("%s", sendRespBody.ErrMsg)
	}

	return nil
}

// sendFeishu 发送飞书通知
func (n *Notifier) sendFeishu(ctx context.Context, webhook, signSecret, message string) error {
	body := map[string]interface{}{
		"msg_type": "text",
		"content": map[string]string{
			"text": message,
		},
	}

	// 如果有加签密钥，计算签名
	if signSecret != "" {
		timestamp := time.Now().Unix()
		stringToSign := fmt.Sprintf("%v", timestamp) + "\n" + signSecret
		var data []byte
		h := hmac.New(sha256.New, []byte(stringToSign))
		_, err := h.Write(data)
		if err != nil {
			return err
		}
		signature := base64.StdEncoding.EncodeToString(h.Sum(nil))

		// 将签名和时间戳加入请求头
		body["timestamp"] = fmt.Sprintf("%v", timestamp)
		body["sign"] = signature
	}

	_, err := n.sendJSONRequest(ctx, webhook, body)
	if err != nil {
		return err
	}
	return nil
}

// sendTelegram 发送 Telegram 通知
func (n *Notifier) sendTelegram(ctx context.Context, botToken, chatID, message string) error {
	if utf8.RuneCountInString(message) > 4096 {
		n.logger.Warn("Telegram消息过长，进行截断",
			zap.Int("originalLen", utf8.RuneCountInString(message)),
			zap.Int("truncatedLen", 4096),
		)
		runes := []rune(message)
		message = string(runes[:4093]) + "..."
	}

	webhookURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)

	body := map[string]interface{}{
		"chat_id": chatID,
		"text":    message,
	}

	_, err := n.sendJSONRequest(ctx, webhookURL, body)
	if err != nil {
		return err
	}
	return nil
}

// sendEmail 发送邮件通知
func (n *Notifier) sendEmail(ctx context.Context, smtpHost string, smtpPort int, fromEmail, password, toEmail, subject, message string) error {
	m := gomail.NewMessage()
	m.SetHeader("From", fromEmail)
	m.SetHeader("To", toEmail)
	m.SetHeader("Subject", subject)
	m.SetBody("text/plain", message)

	d := gomail.NewDialer(smtpHost, smtpPort, fromEmail, password)

	var lastErr error
	for i := 0; i < maxRetries; i++ {
		if i > 0 {
			delay := retryBaseDelay * time.Duration(i)
			n.logger.Info("邮件发送重试",
				zap.Int("attempt", i+1),
				zap.Duration("delay", delay),
			)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}

		if err := d.DialAndSend(m); err != nil {
			lastErr = err
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			continue
		}

		n.logger.Info("邮件发送成功",
			zap.String("from", fromEmail),
			zap.String("to", toEmail),
			zap.String("subject", subject),
		)
		return nil
	}

	return fmt.Errorf("邮件重试%d次后仍失败: %w", maxRetries, lastErr)
}

// webhookConfig Webhook 配置
type webhookConfig struct {
	URL        string
	Method     string
	Headers    map[string]string
	CustomBody string
}

// parseWebhookConfig 解析 Webhook 配置
func parseWebhookConfig(config map[string]interface{}) (*webhookConfig, error) {
	// 解析 URL
	webhookURL, ok := config["url"].(string)
	if !ok || webhookURL == "" {
		return nil, fmt.Errorf("自定义Webhook配置缺少 url")
	}

	// 获取请求方法，默认 POST
	method := "POST"
	if m, ok := config["method"].(string); ok && m != "" {
		method = strings.ToUpper(m)
	}

	// 获取自定义请求头
	headers := make(map[string]string)
	if h, ok := config["headers"].(map[string]interface{}); ok {
		for k, v := range h {
			if strVal, ok := v.(string); ok {
				headers[k] = strVal
			}
		}
	}

	// 获取自定义请求体
	customBody, _ := config["customBody"].(string)

	return &webhookConfig{
		URL:        webhookURL,
		Method:     method,
		Headers:    headers,
		CustomBody: customBody,
	}, nil
}

// buildCustomBody 构建自定义模板格式的请求体
func (n *Notifier) buildCustomBody(agent *models.Agent, record *models.AlertRecord, message, customBody string, maskIP bool) (io.Reader, error) {
	if customBody == "" {
		return nil, fmt.Errorf("必须提供自定义请求体模板")
	}

	// 使用 fasttemplate 进行变量替换
	t := fasttemplate.New(customBody, "{{", "}}")
	escape := func(s string) string {
		b, _ := json.Marshal(s)
		// json.Marshal 会返回带双引号的字符串，例如 "hello\nworld"
		// 模板中不需要外层双引号，所以去掉
		return string(b[1 : len(b)-1])
	}

	bodyStr := t.ExecuteFuncString(func(w io.Writer, tag string) (int, error) {
		var v string

		switch tag {
		case "message":
			v = message
		case "agent.id":
			v = agent.ID
		case "agent.name":
			v = agent.Name
		case "agent.hostname":
			v = agent.Hostname
		case "agent.ip":
			v = agent.IP
		case "agent.ipv4":
			v = agent.IPv4
		case "agent.ipv6":
			v = agent.IPv6
		case "alert.type":
			v = record.AlertType
		case "alert.level":
			v = record.Level
		case "alert.status":
			v = record.Status
		case "alert.message":
			v = record.Message
		case "alert.threshold":
			v = fmt.Sprintf("%.2f", record.Threshold)
		case "alert.actualValue":
			v = fmt.Sprintf("%.2f", record.ActualValue)
		case "alert.resolvedValue":
			v = fmt.Sprintf("%.2f", record.ResolvedValue)
		case "alert.firedAt":
			// 格式化的触发时间 (使用系统时区，Docker 中设置为 Asia/Shanghai)
			v = utils.FormatTimestamp(record.FiredAt)
		case "alert.resolvedAt":
			// 格式化的恢复时间 (使用系统时区，Docker 中设置为 Asia/Shanghai)
			v = utils.FormatTimestamp(record.ResolvedAt)
		default:
			return w.Write([]byte("{{" + tag + "}}"))
		}

		// 写入 JSON 安全转义后的值
		return w.Write([]byte(escape(v)))
	})

	n.logger.Sugar().Debugf("自定义Webhook请求体: %s", bodyStr)
	return strings.NewReader(bodyStr), nil
}

// retryDoWithBackoff 带重试和退避的 HTTP 请求执行
func (n *Notifier) retryDoWithBackoff(ctx context.Context, createReq func() (*http.Request, error)) (*http.Response, error) {
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		if i > 0 {
			delay := retryBaseDelay * time.Duration(i)
			n.logger.Info("通知发送重试",
				zap.Int("attempt", i+1),
				zap.Duration("delay", delay),
			)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		req, err := createReq()
		if err != nil {
			return nil, fmt.Errorf("创建请求失败: %w", err)
		}

		resp, err := sharedHTTPClient.Do(req)
		if err == nil {
			return resp, nil
		}
		lastErr = err

		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("重试%d次后仍失败: %w", maxRetries, lastErr)
}

// sendHTTPRequest 发送 HTTP 请求
func (n *Notifier) sendHTTPRequest(ctx context.Context, method, webhookURL string, body io.Reader, headers map[string]string, contentType string) error {
	bodyData, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("读取请求体失败: %w", err)
	}

	createReq := func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, method, webhookURL, bytes.NewReader(bodyData))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", contentType)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		return req, nil
	}

	resp, err := n.retryDoWithBackoff(ctx, createReq)
	if err != nil {
		return fmt.Errorf("发送请求失败: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("请求失败，状态码: %d, 响应: %s", resp.StatusCode, string(respBody))
	}

	n.logger.Info("自定义Webhook发送成功",
		zap.String("url", webhookURL),
		zap.String("method", method),
		zap.String("response", string(respBody)),
	)

	return nil
}

// sendCustomWebhook 发送自定义Webhook
func (n *Notifier) sendCustomWebhook(ctx context.Context, config map[string]interface{}, agent *models.Agent, record *models.AlertRecord, maskIP bool) error {
	// 解析配置
	cfg, err := parseWebhookConfig(config)
	if err != nil {
		return err
	}

	// 构建消息内容
	message := n.buildMessage(agent, record, maskIP)

	// 构建自定义请求体
	reqBody, err := n.buildCustomBody(agent, record, message, cfg.CustomBody, maskIP)
	if err != nil {
		return err
	}

	// 发送 HTTP 请求，使用 application/json 作为默认 Content-Type
	contentType := "application/json"
	return n.sendHTTPRequest(ctx, cfg.Method, cfg.URL, reqBody, cfg.Headers, contentType)
}

func (n *Notifier) sendJSONRequest(ctx context.Context, url string, body interface{}) ([]byte, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("序列化请求体失败: %w", err)
	}

	createReq := func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	}

	resp, err := n.retryDoWithBackoff(ctx, createReq)
	if err != nil {
		return nil, fmt.Errorf("发送请求失败: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("请求失败，状态码: %d, 响应: %s", resp.StatusCode, string(respBody))
	}

	n.logger.Info("通知发送成功", zap.String("url", url), zap.String("response", string(respBody)))
	return respBody, nil
}

// sendDingTalkByConfig 根据配置发送钉钉通知
func (n *Notifier) sendDingTalkByConfig(ctx context.Context, config map[string]interface{}, message string) error {
	secretKey, ok := config["secretKey"].(string)
	if !ok || secretKey == "" {
		return fmt.Errorf("钉钉配置缺少 secretKey")
	}

	// 构造 Webhook URL
	webhook := fmt.Sprintf("https://oapi.dingtalk.com/robot/send?access_token=%s", secretKey)

	// 检查是否有加签密钥
	signSecret, _ := config["signSecret"].(string)

	return n.sendDingTalk(ctx, webhook, signSecret, message)
}

// sendWeComByConfig 根据配置发送企业微信通知
func (n *Notifier) sendWeComByConfig(ctx context.Context, config map[string]interface{}, message string) error {
	secretKey, ok := config["secretKey"].(string)
	if !ok || secretKey == "" {
		return fmt.Errorf("企业微信配置缺少 secretKey")
	}

	// 构造 Webhook URL
	webhook := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=%s", secretKey)

	return n.sendWeCom(ctx, webhook, message)
}

// sendWeComAppByConfig 根据配置发送企业微信应用通知
func (n *Notifier) sendWeComAppByConfig(ctx context.Context, config map[string]interface{}, message string) error {
	origin := "https://qyapi.weixin.qq.com"
	if v, ok := config["origin"].(string); ok && v != "" {
		origin = v
	}

	toUser := "@all"
	if v, ok := config["toUser"].(string); ok && v != "" {
		toUser = v
	}

	corpId, ok := config["corpId"].(string)
	if !ok || corpId == "" {
		return fmt.Errorf("企业微信应用配置缺少 corpid")
	}

	corpSecret, ok := config["corpSecret"].(string)
	if !ok || corpSecret == "" {
		return fmt.Errorf("企业微信应用配置缺少 corpsecret")
	}

	agentIdf, ok := config["agentId"].(float64)
	if !ok || agentIdf <= 0 {
		return fmt.Errorf("企业微信应用配置缺少 agentid")
	}

	return n.sendWeComApp(ctx, origin, corpId, corpSecret, int(agentIdf), toUser, message)
}

// sendFeishuByConfig 根据配置发送飞书通知
func (n *Notifier) sendFeishuByConfig(ctx context.Context, config map[string]interface{}, message string) error {
	secretKey, ok := config["secretKey"].(string)
	if !ok || secretKey == "" {
		return fmt.Errorf("飞书配置缺少 secretKey")
	}

	// 构造 Webhook URL
	webhook := fmt.Sprintf("https://open.feishu.cn/open-apis/bot/v2/hook/%s", secretKey)

	// 检查是否有加签密钥
	signSecret, _ := config["signSecret"].(string)

	return n.sendFeishu(ctx, webhook, signSecret, message)
}

// sendTelegramByConfig 根据配置发送 Telegram 通知
func (n *Notifier) sendTelegramByConfig(ctx context.Context, config map[string]interface{}, message string) error {
	botToken, ok := config["botToken"].(string)
	if !ok || botToken == "" {
		return fmt.Errorf("Telegram 配置缺少 botToken")
	}

	chatID, ok := config["chatID"].(string)
	if !ok || chatID == "" {
		return fmt.Errorf("Telegram 配置缺少 chatID")
	}

	return n.sendTelegram(ctx, botToken, chatID, message)
}

// sendEmailByConfig 根据配置发送邮件通知
func (n *Notifier) sendEmailByConfig(ctx context.Context, config map[string]interface{}, message string) error {
	smtpHost, ok := config["smtpHost"].(string)
	if !ok || smtpHost == "" {
		return fmt.Errorf("邮件配置缺少 smtpHost")
	}

	// 端口可能是 float64 或 string
	var smtpPort int
	switch v := config["smtpPort"].(type) {
	case float64:
		smtpPort = int(v)
	case string:
		port, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("邮件配置 smtpPort 格式错误: %w", err)
		}
		smtpPort = port
	default:
		return fmt.Errorf("邮件配置缺少 smtpPort")
	}

	fromEmail, ok := config["fromEmail"].(string)
	if !ok || fromEmail == "" {
		return fmt.Errorf("邮件配置缺少 fromEmail")
	}

	password, ok := config["password"].(string)
	if !ok || password == "" {
		return fmt.Errorf("邮件配置缺少 password")
	}

	toEmail, ok := config["toEmail"].(string)
	if !ok || toEmail == "" {
		return fmt.Errorf("邮件配置缺少 toEmail")
	}

	// 邮件主题，默认为"Pika 告警通知"
	subject, ok := config["subject"].(string)
	if !ok || subject == "" {
		subject = "Pika 告警通知"
	}

	return n.sendEmail(ctx, smtpHost, smtpPort, fromEmail, password, toEmail, subject, message)
}

// sendWebhookByConfig 根据配置发送自定义Webhook
func (n *Notifier) sendWebhookByConfig(ctx context.Context, config map[string]interface{}, agent *models.Agent, record *models.AlertRecord, maskIP bool) error {
	return n.sendCustomWebhook(ctx, config, agent, record, maskIP)
}

// SendNotificationByConfig 根据新的配置结构发送通知
func (n *Notifier) SendNotificationByConfig(ctx context.Context, channelConfig *models.NotificationChannelConfig, record *models.AlertRecord, agent *models.Agent, maskIP bool) error {
	if !channelConfig.Enabled {
		return fmt.Errorf("通知渠道已禁用")
	}

	n.logger.Info("发送通知",
		zap.String("channelType", channelConfig.Type),
	)

	// 构造通知消息内容
	message := n.buildMessage(agent, record, maskIP)

	switch channelConfig.Type {
	case "dingtalk":
		return n.sendDingTalkByConfig(ctx, channelConfig.Config, message)
	case "wecom":
		return n.sendWeComByConfig(ctx, channelConfig.Config, message)
	case "wecomApp":
		return n.sendWeComAppByConfig(ctx, channelConfig.Config, message)
	case "feishu":
		return n.sendFeishuByConfig(ctx, channelConfig.Config, message)
	case "telegram":
		return n.sendTelegramByConfig(ctx, channelConfig.Config, message)
	case "email":
		return n.sendEmailByConfig(ctx, channelConfig.Config, message)
	case "webhook":
		return n.sendWebhookByConfig(ctx, channelConfig.Config, agent, record, maskIP)
	default:
		return fmt.Errorf("不支持的通知渠道类型: %s", channelConfig.Type)
	}
}

// SendNotificationByConfigs 根据新的配置结构向多个渠道发送通知
func (n *Notifier) SendNotificationByConfigs(ctx context.Context, channelConfigs []models.NotificationChannelConfig, record *models.AlertRecord, agent *models.Agent, maskIP bool) error {
	message := n.buildMessage(agent, record, maskIP)

	var wg sync.WaitGroup
	var errs []error
	var mu sync.Mutex

	for _, cfg := range channelConfigs {
		if !cfg.Enabled {
			continue
		}
		wg.Add(1)
		go func(cfg models.NotificationChannelConfig) {
			defer wg.Done()
			err := n.sendToChannel(ctx, &cfg, message, agent, record, maskIP)
			if err != nil {
				n.logger.Error("发送通知失败",
					zap.String("channelType", cfg.Type),
					zap.Error(err),
				)
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(cfg)
	}

	wg.Wait()

	if len(errs) > 0 {
		return fmt.Errorf("部分通知发送失败: %v", errs)
	}
	return nil
}

func (n *Notifier) sendToChannel(ctx context.Context, channelConfig *models.NotificationChannelConfig, message string, agent *models.Agent, record *models.AlertRecord, maskIP bool) error {
	n.logger.Info("发送通知", zap.String("channelType", channelConfig.Type))

	channelCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	switch channelConfig.Type {
	case "dingtalk":
		return n.sendDingTalkByConfig(channelCtx, channelConfig.Config, message)
	case "wecom":
		return n.sendWeComByConfig(channelCtx, channelConfig.Config, message)
	case "wecomApp":
		return n.sendWeComAppByConfig(channelCtx, channelConfig.Config, message)
	case "feishu":
		return n.sendFeishuByConfig(channelCtx, channelConfig.Config, message)
	case "telegram":
		return n.sendTelegramByConfig(channelCtx, channelConfig.Config, message)
	case "email":
		return n.sendEmailByConfig(channelCtx, channelConfig.Config, message)
	case "webhook":
		return n.sendWebhookByConfig(channelCtx, channelConfig.Config, agent, record, maskIP)
	default:
		return fmt.Errorf("不支持的通知渠道类型: %s", channelConfig.Type)
	}
}

// SendDingTalkByConfig 导出方法供外部调用
func (n *Notifier) SendDingTalkByConfig(ctx context.Context, config map[string]interface{}, message string) error {
	return n.sendDingTalkByConfig(ctx, config, message)
}

// SendWeComByConfig 导出方法供外部调用
func (n *Notifier) SendWeComByConfig(ctx context.Context, config map[string]interface{}, message string) error {
	return n.sendWeComByConfig(ctx, config, message)
}

// SendWeComAppByConfig 导出方法供外部调用
func (n *Notifier) SendWeComAppByConfig(ctx context.Context, config map[string]interface{}, message string) error {
	return n.sendWeComAppByConfig(ctx, config, message)
}

// SendFeishuByConfig 导出方法供外部调用
func (n *Notifier) SendFeishuByConfig(ctx context.Context, config map[string]interface{}, message string) error {
	return n.sendFeishuByConfig(ctx, config, message)
}

// SendTelegramByConfig 导出方法供外部调用
func (n *Notifier) SendTelegramByConfig(ctx context.Context, config map[string]interface{}, message string) error {
	return n.sendTelegramByConfig(ctx, config, message)
}

// SendEmailByConfig 导出方法供外部调用
func (n *Notifier) SendEmailByConfig(ctx context.Context, config map[string]interface{}, message string) error {
	return n.sendEmailByConfig(ctx, config, message)
}

// SendWebhookByConfig 导出方法供外部调用（测试用）
func (n *Notifier) SendWebhookByConfig(ctx context.Context, config map[string]interface{}, message string) error {
	// 为了测试，创建一个临时的 agent 和 record
	agent := &models.Agent{
		ID:       "test-agent",
		Name:     "测试探针",
		Hostname: "test-host",
		IPv4:     "127.0.0.1",
	}
	record := &models.AlertRecord{
		AlertType:   "test",
		Level:       "info",
		Status:      "firing",
		Message:     message,
		Threshold:   0,
		ActualValue: 0,
		FiredAt:     time.Now().UnixMilli(),
	}
	return n.sendWebhookByConfig(ctx, config, agent, record, false)
}

// SendTestNotification 发送测试通知（动态匹配通知渠道类型）
func (n *Notifier) SendTestNotification(ctx context.Context, channelType string, config map[string]interface{}, message string) error {
	switch channelType {
	case "dingtalk":
		return n.sendDingTalkByConfig(ctx, config, message)
	case "wecom":
		return n.sendWeComByConfig(ctx, config, message)
	case "wecomApp":
		return n.sendWeComAppByConfig(ctx, config, message)
	case "feishu":
		return n.sendFeishuByConfig(ctx, config, message)
	case "telegram":
		return n.sendTelegramByConfig(ctx, config, message)
	case "email":
		return n.sendEmailByConfig(ctx, config, message)
	case "webhook":
		// Webhook 需要 agent 和 record，创建测试数据
		agent := &models.Agent{
			ID:       "test-agent",
			Name:     "测试探针",
			Hostname: "test-host",
			IPv4:     "127.0.0.1",
		}
		record := &models.AlertRecord{
			AlertType:   "test",
			Level:       "info",
			Status:      "firing",
			Message:     message,
			Threshold:   0,
			ActualValue: 0,
			FiredAt:     time.Now().UnixMilli(),
		}
		return n.sendWebhookByConfig(ctx, config, agent, record, false)
	default:
		return fmt.Errorf("不支持的通知渠道类型: %s", channelType)
	}
}
