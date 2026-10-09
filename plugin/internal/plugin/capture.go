package plugin

import (
	"bytes"
	"io"
	"net/http"
	"sync"
)

// captureReader 边转发边收集字节：宿主读取多少就复制多少，绝不预先缓冲整条响应流。
//
// 之所以不能用 io.ReadAll：宿主把插件返回的 resp.Body 当流式管道消费（SSE 场景首字节
// 必须尽快到达），一旦插件先读完再交还，流式语义就失效了，框架的一致性测试也会失败。
//
// 超过 limit 后停止收集（但继续正常转发），并标记 Truncated，避免大响应占用内存。
type captureReader struct {
	source io.ReadCloser
	limit  int

	// onDone 在响应流读完（EOF）或宿主关闭响应体时恰好触发一次。
	// 评测任务在这里投递，确保拿到的是完整内容。
	onDone   func()
	doneOnce sync.Once

	mu        sync.Mutex
	buffer    bytes.Buffer
	truncated bool
	complete  bool
	touched   bool
}

func newCaptureReader(source io.ReadCloser, limit int) *captureReader {
	if source == nil {
		source = http.NoBody
	}
	return &captureReader{source: source, limit: limit}
}

// newCaptureReaderWithDone 与 newCaptureReader 相同，但在流结束时回调 onDone。
func newCaptureReaderWithDone(source io.ReadCloser, limit int, onDone func()) *captureReader {
	reader := newCaptureReader(source, limit)
	reader.onDone = onDone
	return reader
}

func (c *captureReader) Read(p []byte) (int, error) {
	n, err := c.source.Read(p)
	if n > 0 {
		c.collect(p[:n])
	}
	c.mu.Lock()
	c.touched = true
	if err == io.EOF {
		c.complete = true
	}
	c.mu.Unlock()
	if err == io.EOF {
		c.fireDone()
	}
	return n, err //nolint:wrapcheck // 必须是原样的读取结果，SDK 依赖 io.EOF 判断流结束
}

// Close 结束采集。宿主可能提前关闭（客户端断开、上游读失败），
// 此时 Complete() 为 false，调用方应据此跳过评测。
func (c *captureReader) Close() error {
	err := c.source.Close()
	c.fireDone()
	return err //nolint:wrapcheck // 透传底层关闭结果
}

func (c *captureReader) fireDone() {
	if c.onDone == nil {
		return
	}
	c.doneOnce.Do(c.onDone)
}

// BodyTouched 报告底层请求体是否被读取过 —— 读取过即可断定请求已开始写向上游。
// transport.RequestSent 用它把「拨号失败」与「响应读取失败」区分开。
func (c *captureReader) BodyTouched() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.touched
}

func (c *captureReader) collect(chunk []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.truncated {
		return
	}
	remaining := c.limit - c.buffer.Len()
	if remaining <= 0 {
		c.truncated = true
		return
	}
	if len(chunk) > remaining {
		c.buffer.Write(chunk[:remaining])
		c.truncated = true
		return
	}
	c.buffer.Write(chunk)
}

// Snapshot 返回已收集的字节与是否被截断。返回的是副本，调用方可安全持有。
func (c *captureReader) Snapshot() ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.buffer.Bytes()...), c.truncated
}

// Complete 报告宿主是否完整读到了流结束（io.EOF）。
func (c *captureReader) Complete() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.complete
}
