package plugin

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/feeeei/sub2api-plugin-framework/sdk"
)

// errNoConversation 表示该请求/响应不构成可评测的对话（例如非对话接口、空回复）。
var errNoConversation = errors.New("not an evaluable conversation")

// conversation 是从一次转发中还原出的可评测对话。
type conversation struct {
	RequestID      string
	AccountID      int64
	Model          string
	UserPrompt     string
	AssistantReply string
	PromptTokens   int
	OutputTokens   int
	ReasoningToks  int
	TotalTokens    int
	CompletionDone bool
	At             time.Time
}

// JuiceEfficiency 是 CoT 长度占比：思考 Token / 总 Token。
// 它衡量「模型把多少预算花在了推理上」，是本插件评分公式的一项输入。
func (c *conversation) JuiceEfficiency() float64 {
	if c.TotalTokens <= 0 {
		return 0
	}
	return float64(c.ReasoningToks) / float64(c.TotalTokens)
}

// parseConversation 从原始请求体与响应体还原对话。
// 响应体可能是普通 JSON，也可能是 SSE 流（text/event-stream）。
//
// contentType 是响应的 Content-Type header，用于判定流式 vs 非流式。
// 优先用它而非 body 嗅探：嗅探在罕见情况下会误判（见 isEventStream 注释）。
func parseConversation(reqRaw, respRaw []byte, contentType string, meta *sdk.RequestMeta, at time.Time) (*conversation, error) {
	conv := &conversation{At: at}
	if meta != nil {
		conv.RequestID = meta.RequestID
		conv.AccountID = meta.AccountID
	}

	var req struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(reqRaw, &req); err == nil {
		conv.Model = req.Model
		var prompts []string
		for _, message := range req.Messages {
			if message.Role == "user" || message.Role == "system" {
				if text := extractContent(message.Content); text != "" {
					prompts = append(prompts, text)
				}
			}
		}
		conv.UserPrompt = strings.Join(prompts, "\n")
	}

	if len(respRaw) == 0 {
		return nil, errNoConversation
	}
	if isEventStream(contentType, respRaw) {
		parseSSE(respRaw, conv)
	} else {
		parseJSONResponse(respRaw, conv)
	}
	if strings.TrimSpace(conv.AssistantReply) == "" {
		return nil, errNoConversation
	}
	return conv, nil
}

// extractContent 兼容 content 为字符串或分块数组两种形态。
func extractContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var builder strings.Builder
		for _, part := range parts {
			if part.Text != "" {
				if builder.Len() > 0 {
					builder.WriteByte('\n')
				}
				builder.WriteString(part.Text)
			}
		}
		return builder.String()
	}
	return ""
}

// isEventStream 判定响应是否为 SSE 流。
//
// 优先用 Content-Type header（text/event-stream），body 嗅探仅作兜底：
// 嗅探 `\ndata:` 在极罕见情况下会误判 —— 非流式 JSON 响应的正文里若含
// 真实换行（非转义的 \n）后紧跟 `data:`，就会被误判为 SSE。
//
// 这要求响应体本身就是非法 JSON（合法 JSON 里换行会被转义成 `\` + `n` 两字符），
// 或上游未按标准转义，实际触发概率极低，但用 header 可彻底避免。
func isEventStream(contentType string, raw []byte) bool {
	// 优先判据：Content-Type 包含 text/event-stream。
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		return true
	}
	// 兜底：header 缺失或不可信时，才走 body 嗅探。
	// 只接受以 `data:` 或 `event:` 开头，或含 `\ndata:` / `\nevent:` 的响应。
	trimmed := bytes.TrimSpace(raw)
	if bytes.HasPrefix(trimmed, []byte("data:")) || bytes.HasPrefix(trimmed, []byte("event:")) {
		return true
	}
	return bytes.Contains(trimmed, []byte("\ndata:")) || bytes.Contains(trimmed, []byte("\nevent:"))
}

// parseJSONResponse 解析非流式 chat.completions 响应。
func parseJSONResponse(raw []byte, conv *conversation) {
	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
		Usage usageBlock `json:"usage"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return
	}
	if conv.RequestID == "" {
		conv.RequestID = resp.ID
	}
	if conv.Model == "" {
		conv.Model = resp.Model
	}
	if len(resp.Choices) > 0 {
		content := resp.Choices[0].Message.Content
		if content == "" {
			content = resp.Choices[0].Delta.Content
		}
		conv.AssistantReply = content
	}
	applyUsage(&resp.Usage, conv)
	conv.CompletionDone = true
}

// parseSSE 从 SSE 分片中累加增量文本与 usage。
// Codex / Claude Code 等客户端走的就是这条流式路径。
func parseSSE(raw []byte, conv *conversation) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var builder strings.Builder
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			if payload == "[DONE]" {
				conv.CompletionDone = true
			}
			continue
		}
		var chunk struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
					// 部分实现对推理增量使用独立的字段名。
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
				Text string `json:"text"`
			} `json:"choices"`
			Usage usageBlock `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if conv.RequestID == "" {
			conv.RequestID = chunk.ID
		}
		if conv.Model == "" {
			conv.Model = chunk.Model
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				builder.WriteString(choice.Delta.Content)
			} else if choice.Text != "" {
				builder.WriteString(choice.Text)
			}
		}
		applyUsage(&chunk.Usage, conv)
	}
	conv.AssistantReply = builder.String()
}

// usageBlock 覆盖各实现对 usage 的常见字段命名。
type usageBlock struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	OutputTokens     int `json:"output_tokens"`
	InputTokens      int `json:"input_tokens"`
	// CompletionTokensDetails 是 OpenAI 风格：推理 Token 放在这里。
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
	// ReasoningTokens 是部分实现直接给出的顶层字段。
	ReasoningTokens int `json:"reasoning_tokens"`
}

func applyUsage(usage *usageBlock, conv *conversation) {
	if usage == nil {
		return
	}
	if usage.PromptTokens > 0 {
		conv.PromptTokens = usage.PromptTokens
	}
	if usage.InputTokens > 0 {
		conv.PromptTokens = usage.InputTokens
	}
	switch {
	case usage.CompletionTokens > 0:
		conv.OutputTokens = usage.CompletionTokens
	case usage.OutputTokens > 0:
		conv.OutputTokens = usage.OutputTokens
	}
	if usage.TotalTokens > 0 {
		conv.TotalTokens = usage.TotalTokens
	} else if conv.PromptTokens > 0 || conv.OutputTokens > 0 {
		conv.TotalTokens = conv.PromptTokens + conv.OutputTokens
	}
	switch {
	case usage.CompletionTokensDetails.ReasoningTokens > 0:
		conv.ReasoningToks = usage.CompletionTokensDetails.ReasoningTokens
	case usage.ReasoningTokens > 0:
		conv.ReasoningToks = usage.ReasoningTokens
	}
}
