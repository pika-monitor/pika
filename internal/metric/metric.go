package metric

// DataPoint 统一的指标数据点结构
type DataPoint struct {
	Timestamp int64   `json:"timestamp"` // 毫秒时间戳
	Value     float64 `json:"value"`
}

// Series 指标系列（支持多系列，如多网卡、多传感器）
type Series struct {
	Name   string            `json:"name"`             // 系列名称
	Labels map[string]string `json:"labels,omitempty"` // 额外标签
	Data   []DataPoint       `json:"data"`             // 数据点列表
}

// GetMetricsResponse 统一的查询响应格式
type GetMetricsResponse struct {
	AgentID      string   `json:"agentId"`
	Type         string   `json:"type"`
	Range        string   `json:"range"`
	Series       []Series `json:"series"`
	Start        int64    `json:"start"`
	End          int64    `json:"end"`
	FailedSeries []string `json:"failedSeries,omitempty"`
}

// QueryDefinition 查询定义（用于构建多个查询）
type QueryDefinition struct {
	Name   string            // 系列名称
	Query  string            // PromQL 查询语句
	Labels map[string]string // 额外标签
}

// LiveMetricsResponse uses original collection timestamps and a shared window.
type LiveMetricsResponse struct {
	AgentID        string              `json:"agentId"`
	GeneratedAt    int64               `json:"generatedAt"`
	Start          int64               `json:"start"`
	End            int64               `json:"end"`
	MonitorStart   int64               `json:"monitorStart"`
	Series         map[string][]Series `json:"series"`
	LatestSampleAt map[string]int64    `json:"latestSampleAt"`
	HistoryError   string              `json:"historyError,omitempty"`
}
