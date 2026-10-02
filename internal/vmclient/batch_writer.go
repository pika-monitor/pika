package vmclient

import (
	"context"
	"sync"
	"time"
)

const (
	batchWorkers     = 4
	batchQueueSize   = 1024
	batchMaxRequests = 256
	// 合并目标为 1 MiB；单次上报超过此大小时独立导入，避免拆分其结果。
	batchMaxBytes      = 1 << 20
	batchFlushInterval = 10 * time.Millisecond
)

type writeRequest struct {
	ctx     context.Context
	payload []byte
	done    chan error
}

// BatchWriter 合并多个探针的 NDJSON 导入，限制并发和排队数量。
// Write 同步等待导入结果；错误交给上层已有的可靠消息重试机制处理。
// worker 按需启动，队列清空后退出，不需要常驻 goroutine 或额外的关闭流程。
type BatchWriter struct {
	client  *VMClient
	queue   chan *writeRequest
	slots   chan struct{}
	mu      sync.Mutex // 保护 worker 数量，并将入队与最后一个 worker 退出串行化。
	workers int
}

func NewBatchWriter(client *VMClient) *BatchWriter {
	return &BatchWriter{
		client: client,
		queue:  make(chan *writeRequest, batchQueueSize),
		slots:  make(chan struct{}, batchQueueSize),
	}
}

func (w *BatchWriter) Write(ctx context.Context, metrics []Metric) error {
	if len(metrics) == 0 {
		return nil
	}
	// 排队、合并和 HTTP 写入共用调用者的等待预算。
	ctx, cancel := context.WithTimeout(ctx, w.client.writeTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case w.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	// 在入队前完成编码，避免一条无效指标使同批其他探针全部写入失败。
	payload, err := encodeMetrics(metrics)
	if err != nil {
		<-w.slots
		return err
	}
	r := &writeRequest{ctx: ctx, payload: payload, done: make(chan error, 1)}
	w.mu.Lock()
	w.queue <- r // slots 已保留容量，此处不会阻塞。
	if w.workers < batchWorkers {
		w.workers++
		go w.run()
	}
	w.mu.Unlock()
	select {
	case err := <-r.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *BatchWriter) finish(r *writeRequest, err error) {
	r.done <- err
	<-w.slots
}

func (w *BatchWriter) run() {
	var next *writeRequest
	for {
		if next == nil {
			w.mu.Lock()
			select {
			case next = <-w.queue:
			default:
				w.workers--
				w.mu.Unlock()
				return
			}
			w.mu.Unlock()
		}
		if err := next.ctx.Err(); err != nil {
			w.finish(next, err)
			next = nil
			continue
		}
		requests := []*writeRequest{next}
		size := len(next.payload)
		next = nil
		timer := time.NewTimer(batchFlushInterval)
	collect:
		for len(requests) < batchMaxRequests && size < batchMaxBytes {
			select {
			case r := <-w.queue:
				if err := r.ctx.Err(); err != nil {
					w.finish(r, err)
					continue
				}
				if size+len(r.payload) > batchMaxBytes {
					next = r // 单次上报不拆分；超出当前批次的请求留给下一批。
					break collect
				}
				requests = append(requests, r)
				size += len(r.payload)
			case <-timer.C:
				break collect
			}
		}
		timer.Stop()
		payload := make([]byte, 0, size)
		active := requests[:0]
		for _, r := range requests {
			if err := r.ctx.Err(); err != nil {
				w.finish(r, err)
				continue
			}
			payload = append(payload, r.payload...)
			active = append(active, r)
		}
		if len(active) == 0 {
			continue
		}
		// 某个探针断线不能取消同一批其他探针的导入；HTTP 请求自身有写入超时。
		err := w.client.writePayload(context.Background(), payload)
		for _, r := range active {
			w.finish(r, err)
		}
	}
}
