package plugin

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/feeeei/sub2api-plugin-framework/sdk/config"
)

// Config 是插件配置。字段统一 snake_case，未知字段会被拒绝（见 Codec）。
//
// 宿主会用 DisallowUnknownFields 的严格模式解析，并在保存失败回滚时重复调用
// ApplyConfig，因此这里的解析必须无副作用且幂等。
type Config struct {
	// Enabled 是总开关。默认关闭，避免安装后立即产生评测流量与费用。
	Enabled bool `json:"enabled"`
	// DefaultEnabled 决定未被 any 账号规则命中的账号是否评测。
	DefaultEnabled bool `json:"default_enabled"`
	// Accounts 按宿主账号 ID 覆盖评测策略；键为十进制 account_id 字符串。
	Accounts map[string]AccountRule `json:"accounts"`

	IQAPI         IQAPIConfig         `json:"iq_api"`
	LoopDetection LoopDetectionConfig `json:"loop_detection"`
	Queue         QueueConfig         `json:"queue"`
	Capture       CaptureConfig       `json:"capture"`
}

// IQAPIConfig 描述 IQ 评测接口。接口须兼容 OpenAI /chat/completions。
type IQAPIConfig struct {
	BaseURL    string `json:"base_url"`
	APIKey     string `json:"api_key"`
	Model      string `json:"model"`
	TimeoutSec int    `json:"timeout_seconds"`
	MaxRetries int    `json:"max_retries"`
}

// AccountRule 是单个账号的评测覆盖项。
type AccountRule struct {
	Enabled bool `json:"enabled"`
	// SampleRatio 是评测采样率（0–1）：1 表示全部评测，0 表示全部跳过。
	SampleRatio float64 `json:"sample_ratio"`
}

// LoopDetectionConfig 控制防自评循环。缺失该项会导致插件评测自己的评测请求，无限递归。
type LoopDetectionConfig struct {
	Enabled bool `json:"enabled"`
	// CheckHost 比对请求 URL 的 host 与 iq_api.base_url 的 host。
	CheckHost bool `json:"check_host"`
	// CheckModel 比对请求体中的 model 与 iq_api.model。
	CheckModel bool `json:"check_model"`
}

// QueueConfig 配置异步评测的有界队列。评测请求不得阻塞宿主转发。
type QueueConfig struct {
	// WorkerCount 是并发评测协程数。
	WorkerCount int `json:"worker_count"`
	// MaxSize 是待评测任务队列容量；队列满时按 DropWhenFull 处理。
	MaxSize int `json:"max_size"`
	// DropWhenFull 为 true 时丢弃新任务，否则等待（会阻塞调用方，通常不建议）。
	DropWhenFull bool `json:"drop_when_full"`
}

// CaptureConfig 限制留存与送评的报文长度，避免大请求撑爆内存与 KV。
type CaptureConfig struct {
	MaxRequestBodyBytes  int `json:"max_request_body_bytes"`
	MaxResponseBodyBytes int `json:"max_response_body_bytes"`
	// MaxRecordsPerAccount 是每个账号保留的评测记录条数上限。
	MaxRecordsPerAccount int `json:"max_records_per_account"`
	// RecordTTLHours 是评测记录的存活小时数；0 表示不过期（宿主 KV 上限 90 天）。
	RecordTTLHours int `json:"record_ttl_hours"`
}

// DefaultConfig 返回完整默认配置；每次调用都返回独立副本，避免 Codec 复用共享状态。
func DefaultConfig() Config {
	return Config{
		Enabled:        false,
		DefaultEnabled: false,
		Accounts:       map[string]AccountRule{},
		IQAPI: IQAPIConfig{
			BaseURL:    "",
			APIKey:     "",
			Model:      "swift-1.5-iq3_xxs",
			TimeoutSec: 30,
			MaxRetries: 2,
		},
		LoopDetection: LoopDetectionConfig{
			Enabled:    true,
			CheckHost:  true,
			CheckModel: true,
		},
		Queue: QueueConfig{
			WorkerCount:  4,
			MaxSize:      256,
			DropWhenFull: true,
		},
		Capture: CaptureConfig{
			MaxRequestBodyBytes:  256 * 1024,
			MaxResponseBodyBytes: 256 * 1024,
			MaxRecordsPerAccount: 200,
			RecordTTLHours:       0,
		},
	}
}

// Codec 在 ValidateConfig 与 ApplyConfig 之间共享，保证两者行为一致。
var Codec = config.Codec[Config]{Default: DefaultConfig, Validate: validate}

func validate(c *Config) error {
	if c.Accounts == nil {
		c.Accounts = map[string]AccountRule{}
	}
	for key, rule := range c.Accounts {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			return errors.New("accounts 包含空的账号 ID")
		}
		if trimmed != key {
			delete(c.Accounts, key)
			c.Accounts[trimmed] = rule
		}
		if rule.SampleRatio < 0 || rule.SampleRatio > 1 {
			return fmt.Errorf("accounts[%s].sample_ratio 必须在 0 到 1 之间", key)
		}
	}

	c.IQAPI.BaseURL = strings.TrimSpace(c.IQAPI.BaseURL)
	c.IQAPI.APIKey = strings.TrimSpace(c.IQAPI.APIKey)
	c.IQAPI.Model = strings.TrimSpace(c.IQAPI.Model)

	// 未启用时允许留空，使宿主在插件安装后即可保存一份空配置。
	if c.Enabled {
		if c.IQAPI.BaseURL == "" {
			return errors.New("启用评测时 iq_api.base_url 不能为空")
		}
		if c.IQAPI.Model == "" {
			return errors.New("启用评测时 iq_api.model 不能为空")
		}
	}
	if c.IQAPI.BaseURL != "" {
		parsed, err := url.Parse(c.IQAPI.BaseURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return errors.New("iq_api.base_url 必须是 http(s) 绝对地址")
		}
	}
	if c.IQAPI.TimeoutSec < 1 || c.IQAPI.TimeoutSec > 600 {
		return errors.New("iq_api.timeout_seconds 必须在 1 到 600 之间")
	}
	if c.IQAPI.MaxRetries < 0 || c.IQAPI.MaxRetries > 5 {
		return errors.New("iq_api.max_retries 必须在 0 到 5 之间")
	}

	if c.Queue.WorkerCount < 1 || c.Queue.WorkerCount > 64 {
		return errors.New("queue.worker_count 必须在 1 到 64 之间")
	}
	if c.Queue.MaxSize < 1 || c.Queue.MaxSize > 10000 {
		return errors.New("queue.max_size 必须在 1 到 10000 之间")
	}

	if c.Capture.MaxRequestBodyBytes < 1024 || c.Capture.MaxRequestBodyBytes > 4*1024*1024 {
		return errors.New("capture.max_request_body_bytes 必须在 1024 到 4194304 之间")
	}
	if c.Capture.MaxResponseBodyBytes < 1024 || c.Capture.MaxResponseBodyBytes > 4*1024*1024 {
		return errors.New("capture.max_response_body_bytes 必须在 1024 到 4194304 之间")
	}
	if c.Capture.MaxRecordsPerAccount < 0 || c.Capture.MaxRecordsPerAccount > 10000 {
		return errors.New("capture.max_records_per_account 必须在 0 到 10000 之间")
	}
	// 宿主 KV 的 ttl 上限为 90 天。
	if c.Capture.RecordTTLHours < 0 || c.Capture.RecordTTLHours > 90*24 {
		return errors.New("capture.record_ttl_hours 必须在 0 到 2160 之间")
	}
	return nil
}

// ruleFor 返回账号对应的评测策略与来源标注，顺序为账号规则 → 默认策略。
func (c *Config) ruleFor(accountID int64) (AccountRule, bool) {
	if c.Accounts != nil {
		if rule, ok := c.Accounts[accountKey(accountID)]; ok {
			return rule, rule.Enabled
		}
	}
	return AccountRule{Enabled: c.DefaultEnabled, SampleRatio: 1}, c.DefaultEnabled
}
