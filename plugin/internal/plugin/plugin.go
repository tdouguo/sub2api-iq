// Package plugin 实现 Sub2API IQ 评测插件的出站传输层。
//
// 宿主把 OpenAI OAuth 账号的上游请求交给 RoundTrip，本插件在转发的同时按策略
// 异步评测对话质量。宿主负责账号选择、Token 生命周期与计费；插件只负责转发与评测。
package plugin

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/feeeei/sub2api-plugin-framework/sdk"
	"github.com/feeeei/sub2api-plugin-framework/sdk/hostsvc"
	"github.com/feeeei/sub2api-plugin-framework/sdk/transport"
)

// kvNamespace 是宿主 KV 中属于本插件的命名空间。
const kvNamespace = "iq"

// Plugin 实现 sdk.Plugin 及全部可选接口。
type Plugin struct {
	info      sdk.Info
	startedAt time.Time

	mu    sync.RWMutex
	cfg   *Config
	pool  *transport.Pool
	host  *hostsvc.Client
	queue *evalQueue

	randMu sync.Mutex
	rand   *rand.Rand

	requests  atomic.Int64
	failures  atomic.Int64
	evaluated atomic.Int64
	dropped   atomic.Int64
	loopSkips atomic.Int64
}

// New 构造插件；info 通常来自嵌入的 manifest.source.json。
func New(info sdk.Info) *Plugin {
	return &Plugin{
		info:      info,
		startedAt: time.Now(),
		rand:      rand.New(rand.NewSource(time.Now().UnixNano())), //nolint:gosec // 仅用于采样，不涉及安全
	}
}

// Info 返回身份，必须与已安装清单完全一致，否则宿主会立即终止进程。
func (p *Plugin) Info() sdk.Info { return p.info }

// ValidateConfig 严格解析并返回补齐默认值的规范化配置。
func (p *Plugin) ValidateConfig(_ context.Context, raw []byte) ([]byte, error) {
	return Codec.Normalize(raw)
}

// ApplyConfig 原子切换配置、连接池与评测队列；解析失败时保留旧配置。
//
// 宿主会在写库失败回滚时重复调用本方法，因此它必须幂等。
func (p *Plugin) ApplyConfig(_ context.Context, raw []byte) error {
	cfg, err := Codec.Decode(raw)
	if err != nil {
		return err
	}
	pool := newPool()
	queue := newEvalQueue(cfg)

	p.mu.Lock()
	oldPool, oldQueue := p.pool, p.queue
	p.cfg = &cfg
	p.pool = pool
	p.queue = queue
	p.mu.Unlock()

	oldQueue.close()
	if oldPool != nil {
		oldPool.Close()
	}
	queue.start(p.evaluateTask)
	return nil
}

// newPool 构造按代理地址复用连接池的传输池。
// 依赖框架的 Pool 而不是自建 http.Client，才能保留宿主为账号解析出的代理配置。
func newPool() *transport.Pool {
	return transport.NewPool(transport.PoolOptions{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	})
}

func (p *Plugin) current() (*Config, *transport.Pool, *hostsvc.Client) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg, p.pool, p.host
}

// RoundTrip 向上游发出请求，并在策略允许时于响应流读完后异步提交一次评测。
func (p *Plugin) RoundTrip(req *http.Request, meta *sdk.RequestMeta) (*http.Response, error) {
	cfg, pool, _ := p.current()
	if cfg == nil || pool == nil {
		return nil, transport.NotSent(transport.CodeNotConfigured, "插件配置尚未应用")
	}

	// 1. 防循环：绝不评测插件自己发出的评测请求，否则会无限递归。
	if p.isLoopRequest(cfg, req) {
		p.loopSkips.Add(1)
		return p.forward(pool, req, meta, nil)
	}

	// 2. 按账号策略决定是否评测；不评测时完全不采集请求体，零额外开销。
	var reqCapture *captureReader
	evaluate := p.shouldEvaluate(cfg, meta)
	if evaluate {
		reqCapture = newCaptureReader(req.Body, cfg.Capture.MaxRequestBodyBytes)
		req.Body = reqCapture
	}

	p.requests.Add(1)
	resp, err := p.forward(pool, req, meta, reqCapture)
	if err != nil {
		p.failures.Add(1)
		return nil, err
	}
	if !evaluate {
		return resp, nil
	}

	// 3. 响应体以旁路采集的方式交还宿主：宿主读多少就收集多少，绝不预先读完。
	//    评测任务在流结束时投递，确保拿到完整内容且不拖慢首字节。
	respCapture := newCaptureReader(resp.Body, cfg.Capture.MaxResponseBodyBytes)
	respCapture.onDone = func() { p.submit(cfg, reqCapture, respCapture, meta) }
	resp.Body = respCapture
	return resp, nil
}

// forward 通过按代理复用的连接池发出请求，保留宿主为该账号解析出的代理配置。
func (p *Plugin) forward(pool *transport.Pool, req *http.Request, meta *sdk.RequestMeta, body *captureReader) (*http.Response, error) {
	resp, err := pool.RoundTrip(req, meta.ProxyURL)
	if err != nil {
		// 请求体已被读取即意味着请求已开始写向上游；否则交给 transport 判定
		// （DNS、拨号、TLS 失败发生在写请求之前，可安全换号重试）。
		touched := body != nil && body.BodyTouched()
		return nil, transport.Wrap(err, transport.Code(err), transport.RequestSent(err, touched))
	}
	return resp, nil
}

// isLoopRequest 判定该请求是否指向评测 API 自身。
// 评测 API 被配置成同一个 Sub2API 实例时，缺少该检查会导致自我调用无限递归。
//
// 只比对 host，不比对请求体中的 model：读取请求体会让 SDK 认为请求已开始写向上游，
// 从而把「拨号失败」误报成 request_sent=true，进而让宿主放弃换号重试。
func (p *Plugin) isLoopRequest(cfg *Config, req *http.Request) bool {
	if !cfg.LoopDetection.Enabled || !cfg.LoopDetection.CheckHost {
		return false
	}
	if req == nil || req.URL == nil || cfg.IQAPI.BaseURL == "" {
		return false
	}
	return hostOf(cfg.IQAPI.BaseURL) == strings.ToLower(req.URL.Host)
}

// shouldEvaluate 按「账号规则 → 默认策略」决定是否评测，并按采样率随机跳过。
func (p *Plugin) shouldEvaluate(cfg *Config, meta *sdk.RequestMeta) bool {
	if !cfg.Enabled || meta == nil {
		return false
	}
	rule, enabled := cfg.ruleFor(meta.AccountID)
	if !enabled {
		return false
	}
	switch {
	case rule.SampleRatio <= 0:
		return false
	case rule.SampleRatio >= 1:
		return true
	default:
		return p.sample() < rule.SampleRatio
	}
}

func (p *Plugin) sample() float64 {
	p.randMu.Lock()
	defer p.randMu.Unlock()
	return p.rand.Float64()
}

// submit 把评测任务送入有界队列；队列满时按配置丢弃而非阻塞宿主转发。
func (p *Plugin) submit(cfg *Config, req, resp *captureReader, meta *sdk.RequestMeta) {
	p.mu.RLock()
	queue := p.queue
	p.mu.RUnlock()
	if queue == nil {
		return
	}
	if !queue.enqueue(&evalTask{req: req, resp: resp, meta: meta, cfg: cfg}) {
		p.dropped.Add(1)
	}
}

// evaluateTask 是队列的消费函数：确认采集完整后解析、评分并落库。
func (p *Plugin) evaluateTask(ctx context.Context, task *evalTask) {
	// 宿主提前关闭响应体（客户端断开、上游读失败）时不评测，否则会把半截内容当完整对话打分。
	if !task.resp.Complete() {
		p.dropped.Add(1)
		return
	}
	reqRaw, _ := task.req.Snapshot()
	respRaw, truncated := task.resp.Snapshot()
	if truncated || len(respRaw) == 0 {
		p.dropped.Add(1)
		return
	}

	conv, err := parseConversation(reqRaw, respRaw, task.meta, time.Now())
	if err != nil {
		return
	}
	_, pool, host := p.current()
	if pool == nil {
		return
	}
	scorer := newScorer(task.cfg, pool)
	result, err := scorer.score(ctx, conv)
	if err != nil {
		// 上下文被取消说明这是关闭流程主动中断的结果（配置被替换），不是评分失败。
		// 计成 failures 会让每次保存配置都凭空抬高失败率，掩盖真实故障。
		if ctx.Err() != nil {
			p.dropped.Add(1)
			return
		}
		p.failures.Add(1)
		return
	}
	if err := saveRecord(ctx, host, conv.AccountID, conv, result, task.cfg); err != nil {
		p.failures.Add(1)
		return
	}
	p.evaluated.Add(1)
}

// TestConfig 向评测接口发一次最小请求，验证地址、密钥与模型是否可用。
func (p *Plugin) TestConfig(ctx context.Context, raw []byte) (*sdk.TestResult, error) {
	cfg, err := Codec.Decode(raw)
	if err != nil {
		return nil, err
	}
	status, _ := json.Marshal(map[string]any{
		"base_url": cfg.IQAPI.BaseURL,
		"model":    cfg.IQAPI.Model,
		"endpoint": strings.TrimSuffix(cfg.IQAPI.BaseURL, "/") + "/chat/completions",
	})
	if cfg.IQAPI.BaseURL == "" {
		return &sdk.TestResult{Success: false, Message: "iq_api.base_url 为空，无法测试", StatusJSON: status}, nil
	}

	pool := newPool()
	defer pool.Close()
	started := time.Now()
	_, err = newScorer(&cfg, pool).probe(ctx)
	latency := time.Since(started)
	if err != nil {
		return &sdk.TestResult{
			Success:    false,
			Message:    "评测接口不可用: " + transport.SanitizeMessage(err.Error()),
			Latency:    latency,
			StatusJSON: status,
		}, nil
	}
	return &sdk.TestResult{
		Success:    true,
		Message:    "评测接口连通：" + cfg.IQAPI.Model + "（" + latency.Round(time.Millisecond).String() + "）",
		Latency:    latency,
		StatusJSON: status,
	}, nil
}

// Health 始终健康（宿主在应用配置前就会调用），并附带只读运行状态供配置页展示。
// 生成状态没有任何副作用。
func (p *Plugin) Health(context.Context) sdk.Health {
	cfg, pool, host := p.current()
	transports := 0
	if pool != nil {
		transports = pool.Len()
	}
	queued, queueDropped := 0, int64(0)
	p.mu.RLock()
	if p.queue != nil {
		queued, queueDropped = p.queue.stats()
	}
	p.mu.RUnlock()

	status, _ := json.Marshal(map[string]any{
		"configured":      cfg != nil,
		"enabled":         cfg != nil && cfg.Enabled,
		"requests":        p.requests.Load(),
		"evaluated":       p.evaluated.Load(),
		"failures":        p.failures.Load(),
		"dropped":         p.dropped.Load() + queueDropped,
		"loop_skips":      p.loopSkips.Load(),
		"queued":          queued,
		"host_services":   host != nil,
		"transports":      transports,
		"uptime_seconds":  int64(time.Since(p.startedAt).Seconds()),
		"version":         p.info.Version,
		"iq_api_base_url": cfgBaseURL(cfg),
		"iq_api_model":    cfgModel(cfg),
	})
	return sdk.Health{Healthy: true, Message: "ok", StatusJSON: status}
}

// SetHostServices 接收宿主反向服务。旧宿主可能永远不调用，插件必须容忍 host 为 nil。
func (p *Plugin) SetHostServices(client *hostsvc.Client) {
	if client == nil {
		return
	}
	p.mu.Lock()
	p.host = client
	p.mu.Unlock()
}

func cfgBaseURL(cfg *Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.IQAPI.BaseURL
}

func cfgModel(cfg *Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.IQAPI.Model
}

func accountKey(accountID int64) string { return strconv.FormatInt(accountID, 10) }

// hostOf 从 URL 中取出小写的 host[:port]，容忍缺少 scheme 的写法。
func hostOf(rawURL string) string {
	lowered := strings.ToLower(strings.TrimSpace(rawURL))
	if index := strings.Index(lowered, "://"); index >= 0 {
		lowered = lowered[index+3:]
	}
	if index := strings.IndexAny(lowered, "/?#"); index >= 0 {
		lowered = lowered[:index]
	}
	return strings.TrimSuffix(lowered, "/")
}
