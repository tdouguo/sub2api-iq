package plugin

import (
	"context"
	"testing"
	"time"
)

// TestQueueCloseCancelsInFlight 是本次修复的核心回归防线。
//
// 修复前：close() 先 close(q.stop) 再 wg.Wait()，而 worker 一旦进入 handle 就
// 不可打断，close 必须等它自然跑完。这里用「等 ctx 取消才返回」的 handle 模拟
// 一次在途评分 —— 修复前它会一直等下去，close 只能撞上超时。
//
// 修复后：close() 先 cancel，handle 里的 ctx.Done() 立即就绪，close 随即返回。
func TestQueueCloseCancelsInFlight(t *testing.T) {
	cfg := testConfig(t, `{"queue":{"worker_count":1,"max_size":4}}`)

	// 把超时上限设得远大于本测试的容忍时间：若 close 依赖超时才返回，说明
	// 取消通路失效，测试必须失败而不是「靠超时侥幸通过」。
	restore := queueCloseTimeout
	queueCloseTimeout = time.Minute
	t.Cleanup(func() { queueCloseTimeout = restore })

	q := newEvalQueue(*cfg)

	entered := make(chan struct{})
	handle := func(ctx context.Context, _ *evalTask) {
		close(entered)
		// 模拟评分请求：只有 ctx 被取消才会返回。
		<-ctx.Done()
	}
	q.start(handle)

	if !q.enqueue(&evalTask{}) {
		t.Fatal("任务未能入队")
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("worker 未开始处理任务")
	}

	started := time.Now()
	q.close()
	elapsed := time.Since(started)

	if elapsed > 5*time.Second {
		t.Fatalf("close 耗时 %s，说明它没有取消在途评分，而是在等它跑完", elapsed)
	}
}

// TestQueueCloseIsBounded 覆盖兜底路径：worker 卡在非 HTTP 逻辑上、
// 完全不理会 ctx 时，close 仍须在 queueCloseTimeout 内返回，绝不无界等待。
func TestQueueCloseIsBounded(t *testing.T) {
	cfg := testConfig(t, `{"queue":{"worker_count":1,"max_size":4}}`)

	restore := queueCloseTimeout
	queueCloseTimeout = 200 * time.Millisecond
	t.Cleanup(func() { queueCloseTimeout = restore })

	q := newEvalQueue(*cfg)

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	entered := make(chan struct{})
	handle := func(context.Context, *evalTask) {
		close(entered)
		<-release // 故意忽略 ctx
	}
	q.start(handle)

	if !q.enqueue(&evalTask{}) {
		t.Fatal("任务未能入队")
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("worker 未开始处理任务")
	}

	started := time.Now()
	q.close()
	elapsed := time.Since(started)

	if elapsed > 2*time.Second {
		t.Fatalf("close 耗时 %s，未受 queueCloseTimeout 约束", elapsed)
	}
}

// TestQueueCloseIsIdempotent 保证重复 close 不会 panic（close(stop) 与 cancel 都只执行一次）。
func TestQueueCloseIsIdempotent(t *testing.T) {
	cfg := testConfig(t, `{"queue":{"worker_count":2,"max_size":4}}`)
	q := newEvalQueue(*cfg)
	q.start(func(context.Context, *evalTask) {})

	for i := 0; i < 3; i++ {
		q.close()
	}
}

// TestQueueCloseNilSafe 覆盖未配置场景：nil 队列的 close 不应 panic。
func TestQueueCloseNilSafe(t *testing.T) {
	var q *evalQueue
	q.close()
	q.start(func(context.Context, *evalTask) {})
	if q.enqueue(&evalTask{}) {
		t.Error("nil 队列不应接受任务")
	}
	if _, dropped := q.stats(); dropped != 0 {
		t.Error("nil 队列不应报告丢弃")
	}
}
