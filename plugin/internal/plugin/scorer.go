package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/feeeei/sub2api-plugin-framework/sdk/transport"
)

// 评分公式权重，与项目定义一致。
const (
	weightReasoning = 0.3
	weightJuice     = 0.2
	weightLogic     = 0.3
	weightTask      = 0.2

	// judgeTemperature 固定为 0，保证同一对话的评分可复现。
	judgeTemperature = 0
	// maxJudgeOutputTokens 限制评委模型的输出长度。
	maxJudgeOutputTokens = 512
)

// score 是四个维度组成的评测结果。
type score struct {
	ReasoningDepth  float64
	LogicCoherence  float64
	TaskCompletion  float64
	JuiceEfficiency float64
	Overall         float64
	Analysis        string
}

// breakdown 结构与评委模型被要求输出的 JSON 一致。
type breakdown struct {
	ReasoningDepth float64 `json:"reasoning_depth"`
	LogicCoherence float64 `json:"logic_coherence"`
	TaskCompletion float64 `json:"task_completion"`
	Analysis       string  `json:"analysis"`
}

type scorer struct {
	cfg  *Config
	pool *transport.Pool
}

func newScorer(cfg *Config, pool *transport.Pool) *scorer {
	return &scorer{cfg: cfg, pool: pool}
}

// score 调用评委模型，计算四维评分与总分。
//
// JuiceEfficiency 不由评委模型给出：它是 thinking_tokens / total_tokens，
// 属于可观测的客观量，让模型猜反而更不准确。
func (s *scorer) score(ctx context.Context, conv *conversation) (*score, error) {
	prompt := buildJudgePrompt(conv)
	raw, err := s.call(ctx, prompt)
	if err != nil {
		return nil, err
	}
	parsed, err := parseBreakdown(raw)
	if err != nil {
		return nil, err
	}

	juice := conv.JuiceEfficiency()
	clamp := func(value float64) float64 {
		return math.Max(0, math.Min(100, value))
	}
	parsed.ReasoningDepth = clamp(parsed.ReasoningDepth)
	parsed.LogicCoherence = clamp(parsed.LogicCoherence)
	parsed.TaskCompletion = clamp(parsed.TaskCompletion)

	overall := parsed.ReasoningDepth*weightReasoning +
		juice*100*weightJuice +
		parsed.LogicCoherence*weightLogic +
		parsed.TaskCompletion*weightTask

	return &score{
		ReasoningDepth:  parsed.ReasoningDepth,
		LogicCoherence:  parsed.LogicCoherence,
		TaskCompletion:  parsed.TaskCompletion,
		JuiceEfficiency: juice,
		Overall:         math.Round(overall*100) / 100,
		Analysis:        parsed.Analysis,
	}, nil
}

// probe 发一次最小请求验证评测接口可用，供配置页的“测试”按钮使用。
// 任何能解析出 JSON 的回复都算连通。
func (s *scorer) probe(ctx context.Context) (*breakdown, error) {
	raw, err := s.call(ctx, "回复一个 JSON 对象：{\"reasoning_depth\":0,\"logic_coherence\":0,\"task_completion\":0,\"analysis\":\"probe\"}")
	if err != nil {
		return nil, err
	}
	return parseBreakdown(raw)
}

// call 带指数退避重试地调用 chat/completions。
func (s *scorer) call(ctx context.Context, prompt string) (string, error) {
	if s.cfg == nil || s.cfg.IQAPI.BaseURL == "" {
		return "", transport.NotSent(transport.CodeNotConfigured, "iq_api.base_url 为空")
	}
	if s.pool == nil {
		return "", transport.NotSent(transport.CodeNotConfigured, "传输池尚未初始化")
	}

	body, err := json.Marshal(map[string]any{
		"model":       s.cfg.IQAPI.Model,
		"messages":    []map[string]string{{"role": "user", "content": prompt}},
		"temperature": judgeTemperature,
		"max_tokens":  maxJudgeOutputTokens,
		"stream":      false,
	})
	if err != nil {
		return "", err
	}
	endpoint := strings.TrimSuffix(s.cfg.IQAPI.BaseURL, "/") + "/chat/completions"

	attempts := s.cfg.IQAPI.MaxRetries + 1
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			// 指数退避：200ms、400ms、800ms…，并尊重 ctx 取消。
			delay := time.Duration(200*(1<<uint(attempt-1))) * time.Millisecond
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			case <-timer.C:
			}
		}
		content, retryable, err := s.attempt(ctx, endpoint, body)
		if err == nil {
			return content, nil
		}
		lastErr = err
		if !retryable {
			return "", err
		}
	}
	return "", fmt.Errorf("评测接口连续 %d 次失败: %w", attempts, lastErr)
}

// attempt 发一次请求。retryable 表示该错误值得重试（网络抖动、5xx、429）。
func (s *scorer) attempt(ctx context.Context, endpoint string, body []byte) (string, bool, error) {
	timeout := time.Duration(s.cfg.IQAPI.TimeoutSec) * time.Second
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "")
	if s.cfg.IQAPI.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.IQAPI.APIKey)
	}

	resp, err := s.pool.RoundTrip(req, "")
	if err != nil {
		return "", true, err
	}
	defer func() { _ = resp.Body.Close() }()

	// 只读固定上限，避免异常端点上不封顶的响应把内存吃掉。
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", true, err
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return "", true, fmt.Errorf("评测接口返回 %d: %s", resp.StatusCode, snippet(payload))
	}
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("评测接口返回 %d: %s", resp.StatusCode, snippet(payload))
	}

	var completion struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(payload, &completion); err != nil {
		return "", false, err
	}
	if len(completion.Choices) == 0 || strings.TrimSpace(completion.Choices[0].Message.Content) == "" {
		return "", false, errors.New("评测接口没有返回内容")
	}
	return completion.Choices[0].Message.Content, false, nil
}

// parseBreakdown 从评委模型输出中提取 JSON。
// 模型常把 JSON 包在 ```json 代码块或解释性文字里，因此取首个 `{` 到末个 `}`。
func parseBreakdown(content string) (*breakdown, error) {
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return nil, errors.New("评测结果中找不到 JSON 对象")
	}
	var parsed breakdown
	if err := json.Unmarshal([]byte(content[start:end+1]), &parsed); err != nil {
		return nil, fmt.Errorf("解析评测结果失败: %w", err)
	}
	return &parsed, nil
}

func snippet(payload []byte) string {
	text := strings.TrimSpace(string(payload))
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	return transport.SanitizeMessage(text)
}

// buildJudgePrompt 组装评委提示词。两个方向的内容都会被截断，
// 避免把整个上下文（可能上百 KB）都塞给评委模型。
func buildJudgePrompt(conv *conversation) string {
	const perField = 4000
	var builder strings.Builder
	builder.WriteString("你是一个严谨的 AI 对话质量评委。请按三个维度给下面这段对话打分，每项 0–100 的浮点数。\n\n")
	builder.WriteString("维度定义：\n")
	builder.WriteString("1. reasoning_depth：推理深度。是否拆解了问题、考虑了边界情况与替代方案。\n")
	builder.WriteString("2. logic_coherence：逻辑连贯性。结论与前提是否自洽，有无自相矛盾或跳跃。\n")
	builder.WriteString("3. task_completion：任务完成度。是否真正解决了用户提出的问题。\n\n")
	builder.WriteString("只输出一个 JSON 对象，不要输出任何其他文字：\n")
	builder.WriteString("{\"reasoning_depth\": 0.0, \"logic_coherence\": 0.0, \"task_completion\": 0.0, \"analysis\": \"50 字以内的中文简评\"}\n\n")
	builder.WriteString("=== 对话开始 ===\n")
	builder.WriteString("[用户]\n")
	builder.WriteString(truncateRunes(conv.UserPrompt, perField))
	builder.WriteString("\n\n[助手]\n")
	builder.WriteString(truncateRunes(conv.AssistantReply, perField))
	builder.WriteString("\n=== 对话结束 ===\n\n")
	builder.WriteString("观察到的事实（仅供参考，不用打分）：\n")
	builder.WriteString("模型: " + conv.Model + "\n")
	builder.WriteString("输入 Token: " + strconv.Itoa(conv.PromptTokens) + "\n")
	builder.WriteString("输出 Token: " + strconv.Itoa(conv.OutputTokens) + "\n")
	builder.WriteString("其中推理 Token: " + strconv.Itoa(conv.ReasoningToks) + "\n")
	return builder.String()
}

// truncateRunes 按字符（而非字节）截断，避免把多字节字符切坏。
func truncateRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
