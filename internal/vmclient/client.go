package vmclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

const maxConnectionsPerHost = 128

// VMClient VictoriaMetrics 客户端
type VMClient struct {
	baseURL      string
	httpClient   *http.Client
	writeTimeout time.Duration
	queryTimeout time.Duration
}

// QueryResult 查询结果
type QueryResult struct {
	Status string     `json:"status"`
	Data   ResultData `json:"data"`
}

// ResultData 查询结果数据
type ResultData struct {
	ResultType string   `json:"resultType"`
	Result     []Result `json:"result"`
}

// Result 单个时间序列结果
type Result struct {
	Metric map[string]string `json:"metric"`
	Values [][]interface{}   `json:"values"` // [[timestamp, value], ...]
}

// DataPoint 数据点
type DataPoint struct {
	Timestamp int64
	Value     float64
	Labels    map[string]string
}

// Metric VictoriaMetrics JSON Line Format 指标
type Metric struct {
	Metric     map[string]string `json:"metric"`
	Values     []float64         `json:"values"`
	Timestamps []int64           `json:"timestamps"`
}

// NewVMClient 创建 VictoriaMetrics 客户端
func NewVMClient(baseURL string, writeTimeout, queryTimeout time.Duration) *VMClient {
	if writeTimeout == 0 {
		writeTimeout = 30 * time.Second
	}
	if queryTimeout == 0 {
		queryTimeout = 60 * time.Second
	}

	return &VMClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			// 默认 Transport 每个目标仅保留 2 个空闲连接，大量探针并发
			// 写入时会频繁建连，耗尽临时端口。专用连接池同时限制活跃连接，
			// 达到上限的请求等待可复用连接，并受各操作的 context 超时约束。
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					Timeout:   30 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          maxConnectionsPerHost,
				MaxIdleConnsPerHost:   maxConnectionsPerHost,
				MaxConnsPerHost:       maxConnectionsPerHost,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: time.Second,
			},
		},
		writeTimeout: writeTimeout,
		queryTimeout: queryTimeout,
	}
}

// closeResponseBody 读取到 EOF 后关闭，确保 HTTP/1.1 连接可以放回连接池。
// 读取受请求 context 的超时约束，即使响应无效也不会无限等待。
func closeResponseBody(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
}

// Write 写入指标（VictoriaMetrics JSON Line Format）
func (c *VMClient) Write(ctx context.Context, metrics []Metric) error {
	if len(metrics) == 0 {
		return nil
	}

	payload, err := encodeMetrics(metrics)
	if err != nil {
		return err
	}
	return c.writePayload(ctx, payload)
}

func encodeMetrics(metrics []Metric) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	for _, metric := range metrics {
		if err := encoder.Encode(metric); err != nil {
			return nil, fmt.Errorf("encode metric failed: %w", err)
		}
	}
	return buf.Bytes(), nil
}

func (c *VMClient) writePayload(ctx context.Context, payload []byte) error {
	reqCtx, cancel := context.WithTimeout(ctx, c.writeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "POST", c.baseURL+"/api/v1/import", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create request failed: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-ndjson")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("write metrics failed: %w", err)
	}
	defer closeResponseBody(resp.Body)

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("write metrics failed with status %d: %s", resp.StatusCode, string(body))
	}

	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return fmt.Errorf("read write response failed: %w", err)
	}
	return nil
}

// AutoStep 根据查询范围自动生成 step（适用于 VictoriaMetrics）
func AutoStep(start, end time.Time) time.Duration {
	r := end.Sub(start)

	switch {
	case r <= 5*time.Minute:
		// 实时/极短窗口：与探针 1s 采集对齐，避免聚合丢点
		return 1 * time.Second
	case r <= time.Hour:
		return 10 * time.Second
	case r <= 3*time.Hour:
		return 15 * time.Second
	case r <= 6*time.Hour:
		return 30 * time.Second
	case r <= 12*time.Hour:
		return time.Minute
	case r <= 24*time.Hour:
		return 2 * time.Minute
	case r <= 3*24*time.Hour:
		return 5 * time.Minute
	case r <= 7*24*time.Hour:
		return 10 * time.Minute
	case r <= 30*24*time.Hour:
		return 30 * time.Minute
	default:
		return time.Hour
	}
}

// QueryRange 范围查询
// 如果 step 为 0，则让 VictoriaMetrics 自动选择合适的步长
func (c *VMClient) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) (*QueryResult, error) {
	reqCtx, cancel := context.WithTimeout(ctx, c.queryTimeout)
	defer cancel()

	params := url.Values{}
	params.Set("query", query)
	params.Set("start", fmt.Sprintf("%d", start.Unix()))
	params.Set("end", fmt.Sprintf("%d", end.Unix()))
	if step > 0 {
		params.Set("step", fmt.Sprintf("%ds", int(step.Seconds())))
	} else {
		autoStep := AutoStep(start, end)
		params.Set("step", fmt.Sprintf("%ds", int(autoStep.Seconds())))
	}

	reqURL := fmt.Sprintf("%s/api/v1/query_range?%s", c.baseURL, params.Encode())

	req, err := http.NewRequestWithContext(reqCtx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request failed: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query range failed: %w", err)
	}
	defer closeResponseBody(resp.Body)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("query range failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result QueryResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response failed: %w", err)
	}

	if result.Status != "success" {
		return nil, fmt.Errorf("query failed with status: %s", result.Status)
	}

	return &result, nil
}

// Query 即时查询
func (c *VMClient) Query(ctx context.Context, query string) (*QueryResult, error) {
	reqCtx, cancel := context.WithTimeout(ctx, c.queryTimeout)
	defer cancel()

	params := url.Values{}
	params.Set("query", query)

	reqURL := fmt.Sprintf("%s/api/v1/query?%s", c.baseURL, params.Encode())

	req, err := http.NewRequestWithContext(reqCtx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request failed: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer closeResponseBody(resp.Body)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("query failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result QueryResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response failed: %w", err)
	}

	if result.Status != "success" {
		return nil, fmt.Errorf("query failed with status: %s", result.Status)
	}

	return &result, nil
}

// DeleteSeries 删除时间序列数据
func (c *VMClient) DeleteSeries(ctx context.Context, matchers []string) error {
	if len(matchers) == 0 {
		return nil
	}

	reqCtx, cancel := context.WithTimeout(ctx, c.queryTimeout)
	defer cancel()

	params := url.Values{}
	for _, matcher := range matchers {
		params.Add("match[]", matcher)
	}

	reqURL := fmt.Sprintf("%s/api/v1/admin/tsdb/delete_series?%s", c.baseURL, params.Encode())
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, reqURL, nil)
	if err != nil {
		return fmt.Errorf("create request failed: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete series failed: %w", err)
	}
	defer closeResponseBody(resp.Body)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete series failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// ConvertToDataPoints 将查询结果转换为数据点列表
func ConvertToDataPoints(result *QueryResult) []DataPoint {
	if result == nil || len(result.Data.Result) == 0 {
		return []DataPoint{}
	}

	var points []DataPoint
	for _, r := range result.Data.Result {
		for _, v := range r.Values {
			if len(v) < 2 {
				continue
			}

			// timestamp 是 float64（Unix 秒）
			timestamp, ok := v[0].(float64)
			if !ok {
				continue
			}

			// value 是 string
			valueStr, ok := v[1].(string)
			if !ok {
				continue
			}

			var value float64
			if _, err := fmt.Sscanf(valueStr, "%f", &value); err != nil {
				continue
			}

			points = append(points, DataPoint{
				Timestamp: int64(timestamp * 1000), // 转换为毫秒
				Value:     value,
				Labels:    r.Metric,
			})
		}
	}

	return points
}

// GetLabelValues 获取指定 label 的所有值
func (c *VMClient) GetLabelValues(ctx context.Context, labelName string, match []string) ([]string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, c.queryTimeout)
	defer cancel()

	params := url.Values{}
	for _, m := range match {
		params.Add("match[]", m)
	}

	reqURL := fmt.Sprintf("%s/api/v1/label/%s/values?%s", c.baseURL, labelName, params.Encode())

	req, err := http.NewRequestWithContext(reqCtx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request failed: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get label values failed: %w", err)
	}
	defer closeResponseBody(resp.Body)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get label values failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Status string   `json:"status"`
		Data   []string `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response failed: %w", err)
	}

	if result.Status != "success" {
		return nil, fmt.Errorf("get label values failed with status: %s", result.Status)
	}

	return result.Data, nil
}
