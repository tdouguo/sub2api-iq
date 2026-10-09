package plugin

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/feeeei/sub2api-plugin-framework/sdk"
)

// evalTask 是一次待评测的转发。采集器在任务被消费时才取值 ——
// 此时宿主已读完响应流，采集器里才是完整内容。
type evalTask struct {
	req  *captureReader
	resp *captureReader
	meta *sdk.RequestMeta
	cfg  *Config
}

// evalQueue 是有界的评测工作队列。
//
// 之所以必须有界：转发路径不能因为评测慢而堆积协程。早期实现直接 go f(...)，
// 并发一高就是无界协程 + 无界内存，同时把评测 API 打满。
type evalQueue struct {
	tasks   chan *evalTask
	workers int

	startOnce sync.Once
	stopOnce  sync.Once
	stop      chan struct{}
	wg        sync.WaitGroup

	mu   sync.Mutex
	host any // 保持对宿主服务的引用，避免配置切换后队列拿不到它

	dropped atomic.Int64
	running atomic.Int64

	// dropWhenFull 为 true 时队列满即丢弃；否则短暂等待（可能轻微拖慢宿主转发）。
	dropWhenFull bool
}

func newEvalQueue(cfg Config) *evalQueue {
	return &evalQueue{
		tasks:        make(chan *evalTask, cfg.Queue.MaxSize),
		workers:      cfg.Queue.WorkerCount,
		stop:         make(chan struct{}),
		dropWhenFull: cfg.Queue.DropWhenFull,
	}
}

// start 拉起消费协程；重复调用只有第一次生效。
func (q *evalQueue) start(ctx context.Context, handle func(context.Context, *evalTask)) {
	if q == nil {
		return
	}
	q.startOnce.Do(func() {
		for i := 0; i < q.workers; i++ {
			q.wg.Add(1)
			go q.worker(ctx, handle)
		}
	})
}

func (q *evalQueue) worker(ctx context.Context, handle func(context.Context, *evalTask)) {
	defer q.wg.Done()
	for {
		select {
		case <-q.stop:
			return
		case task, ok := <-q.tasks:
			if !ok {
				return
			}
			q.running.Add(1)
			handle(ctx, task)
			q.running.Add(-1)
		}
	}
}

// enqueue 投递任务；返回 false 表示因队列满而丢弃。永不长时间阻塞。
func (q *evalQueue) enqueue(task *evalTask) bool {
	if q == nil {
		return false
	}
	select {
	case <-q.stop:
		return false
	default:
	}
	select {
	case q.tasks <- task:
		return true
	default:
	}
	if q.dropWhenFull {
		q.dropped.Add(1)
		return false
	}
	// 不丢弃时最多等 200ms，仍然满则放弃 —— 宁可少评一条，也不能拖住转发。
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()
	select {
	case q.tasks <- task:
		return true
	case <-timer.C:
		q.dropped.Add(1)
		return false
	case <-q.stop:
		return false
	}
}

// close 停止消费；在途任务会被放弃，因为配置已被替换。
func (q *evalQueue) close() {
	if q == nil {
		return
	}
	q.stopOnce.Do(func() { close(q.stop) })
	q.wg.Wait()
}

// stats 返回当前队列深度与累计丢弃数。
func (q *evalQueue) stats() (int, int64) {
	if q == nil {
		return 0, 0
	}
	return len(q.tasks), q.dropped.Load()
}
