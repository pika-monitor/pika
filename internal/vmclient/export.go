package vmclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Export returns stored samples with their original millisecond timestamps,
// without query_range's lookback filling or latency offset.
func (c *VMClient) Export(ctx context.Context, matchers []string, start, end time.Time) ([]Metric, error) {
	ctx, cancel := context.WithTimeout(ctx, c.queryTimeout)
	defer cancel()
	release, err := c.acquireQuery(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	params := url.Values{"start": {fmt.Sprintf("%.3f", float64(start.UnixMilli())/1000)}, "end": {fmt.Sprintf("%.3f", float64(end.UnixMilli())/1000)}, "max_rows_per_line": {"1000"}}
	for _, matcher := range matchers {
		params.Add("match[]", matcher)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/export?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("export samples: %w", err)
	}
	defer closeResponseBody(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("export samples: HTTP %d", resp.StatusCode)
	}
	// Bound both decoding and retained samples, including chunked responses.
	reader := &io.LimitedReader{R: resp.Body, N: 32 << 20}
	decoder := json.NewDecoder(reader)
	rows := make([]Metric, 0)
	count := 0
	for {
		var row Metric
		if err := decoder.Decode(&row); err == io.EOF {
			if reader.N == 0 {
				return nil, fmt.Errorf("export exceeds response limit")
			}
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode exported samples: %w", err)
		}
		if len(row.Values) != len(row.Timestamps) {
			return nil, fmt.Errorf("exported values and timestamps differ in length")
		}
		if len(row.Values) == 0 {
			continue
		}
		count += len(row.Values)
		if count > 100000 {
			return nil, fmt.Errorf("export exceeds sample limit")
		}
		rows = append(rows, row)
	}
	return rows, nil
}
