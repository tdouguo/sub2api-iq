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
	req         *captureReader
	resp        *captureReader
	meta        *sdk.RequestMeta
	cfg         *Config
	contentType string // 响应的 Content-Type，用于判定是否 SSE
}

// queueCloseTimeout 是 close 等待在途 worker 退出的上限。
//
// 取值权衡：正常情况下 close 先 cancel、worker 随之立刻返回，这个上限根本不会
// 触发；它只兜住「worker 卡在非 HTTP 路径上」的异常情形。设置过大等于没设，
// 过小则可能让仍在收尾的 worker 与已替换的旧配置继续运行 —— 5 秒兼顾两者。
var queueCloseTimeout = 5 * time.Second

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

	// ctx 与 cancel 让 close 能主动中断在途评分。
	//
	// 没有它时，正在 handle 里的 worker 无法被打断，close 只能等它把这次评分
	// 请求自然跑完（最长 timeout_seconds × (max_retries+1)，可达数十分钟），
	// 而 ApplyConfig 是同步调用 close 的，于是宿主的配置保存被一起拖住。
	ctx    context.Context
	cancel context.CancelFunc

	dropped atomic.Int64

	// dropWhenFull 为 true 时队列满即丢弃；否则短暂等待（可能轻微拖慢宿主转发）。
	dropWhenFull bool
}

func newEvalQueue(cfg Config) *evalQueue {
	ctx, cancel := context.WithCancel(context.Background())
	return &evalQueue{
		tasks:        make(chan *evalTask, cfg.Queue.MaxSize),
		workers:      cfg.Queue.WorkerCount,
		stop:         make(chan struct{}),
		ctx:          ctx,
		cancel:       cancel,
		dropWhenFull: cfg.Queue.DropWhenFull,
	}
}

// start 拉起消费协程；重复调用只有第一次生效。
//
// ctx 不再由调用方传入：队列自己持有可取消的 ctx，close 才能中断在途评分。
func (q *evalQueue) start(handle func(context.Context, *evalTask)) {
	if q == nil {
		return
	}
	q.startOnce.Do(func() {
		for i := 0; i < q.workers; i++ {
			q.wg.Add(1)
			go q.worker(handle)
		}
	})
}

func (q *evalQueue) worker(handle func(context.Context, *evalTask)) {
	defer q.wg.Done()
	ctx := q.ctx
	for {
		select {
		case <-q.stop:
			return
		case task, ok := <-q.tasks:
			if !ok {
				return
			}
			handle(ctx, task)
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

// close 停止消费并中断在途评分。
//
// 顺序至关重要：先 cancel 再等待。
//
//   - cancel 让在途的评分 HTTP 请求立刻返回（req 由 http.NewRequestWithContext
//     构造，且 Pool.RoundTrip 直接透传给 http.Transport，取消能真正生效）；
//   - wg.Wait 之后通常瞬间返回；
//   - 若仍有 worker 卡在别处，最多等 queueCloseTimeout，之后放弃等待。
//
// 之所以必须有上限：close 由 ApplyConfig 同步调用，而它跑在宿主的配置保存路径上。
// 无界等待会把「保存配置」变成「等所有评分跑完」。
func (q *evalQueue) close() {
	if q == nil {
		return
	}
	q.stopOnce.Do(func() {
		close(q.stop)
		q.cancel()
	})

	done := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(done)
	}()

	timer := time.NewTimer(queueCloseTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		// 超时：放弃等待。这些 worker 用的是已被替换的旧配置，
		// 让它们自生自灭好过阻塞宿主保存配置。
	}
}

// stats 返回当前队列深度与累计丢弃数。
func (q *evalQueue) stats() (int, int64) {
	if q == nil {
		return 0, 0
	}
	return len(q.tasks), q.dropped.Load()
}
