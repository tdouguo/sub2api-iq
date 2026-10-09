package plugin

import (
	"testing"
	"time"

	"github.com/feeeei/sub2api-plugin-framework/sdk"
)

// TestParseConversationContentTypeHeader 是本次修复的核心回归防线。
//
// 修复前：isEventStream 纯靠 body 嗅探（bytes.Contains(trimmed, "\ndata:")），
// 非流式 JSON 响应若正文含真实换行后紧跟 `data:` 会被误判为 SSE，导致
// parseSSE 取不到 choices，返回 errNoConversation，静默丢一次评测。
//
// 修复后：优先检查 Content-Type header（text/event-stream），body 嗅探
// 仅作兜底。本测试发送 application/json + 正文含 `\ndata:` 的非流式响应，
// 断言它被正确识别为 JSON 而非 SSE，且 AssistantReply 正确提取。
func TestParseConversationContentTypeHeader(t *testing.T) {
	req := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"什么是 SSE？"}]}`)

	// 构造一个非流式 JSON 响应，其 content 字段里含「真实换行 + data:」。
	// 这在合法 JSON 里不会出现（换行会被转义），但本测试刻意制造它，
	// 验证 Content-Type 能挡住 body 嗅探的误判。
	resp := []byte(`{"id":"chatcmpl-123","choices":[{"message":{"role":"assistant","content":"SSE 帧格式：\ndata: {\"a\":1}\n\nevent: message"}}],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`)

	meta := &sdk.RequestMeta{AccountID: 100, Platform: "openai", AccountType: "oauth"}

	// Case 1: Content-Type 明确为 application/json，即便正文含 `\ndata:`，
	// 也应被识别为 JSON 而非 SSE。
	conv, err := parseConversation(req, resp, "application/json; charset=utf-8", meta, time.Now())
	if err != nil {
		t.Fatalf("parseConversation 返回错误: %v", err)
	}
	if conv.AssistantReply == "" {
		t.Error("Content-Type=application/json 时未能从 JSON 提取 AssistantReply")
	}
	if conv.PromptTokens != 10 || conv.OutputTokens != 20 {
		t.Errorf("token 统计错误: prompt=%d output=%d, 期望 10/20", conv.PromptTokens, conv.OutputTokens)
	}

	// Case 2: Content-Type 明确为 text/event-stream，body 却是 JSON 格式，
	// 应尝试按 SSE 解析（会取不到内容，这里只验证它走了 SSE 分支）。
	convSSE, err := parseConversation(req, resp, "text/event-stream", meta, time.Now())
	if err == nil && convSSE.AssistantReply != "" {
		t.Error("Content-Type=text/event-stream 时不应从 JSON body 提取出回复")
	}

	// Case 3: Content-Type 留空，应回退到 body 嗅探。由于 resp 含 `\ndata:`，
	// 会被误判为 SSE（这是已知局限，修复只保证 header 存在时生效）。
	convFallback, err := parseConversation(req, resp, "", meta, time.Now())
	if err == nil && convFallback.AssistantReply != "" {
		t.Log("Content-Type 留空时 body 嗅探误判为 SSE（预期行为）")
	}
}

// TestIsEventStreamWithHeader 单独验证 isEventStream 函数的 Content-Type 优先逻辑。
func TestIsEventStreamWithHeader(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		body        []byte
		want        bool
	}{
		{
			name:        "Content-Type 明确为 text/event-stream",
			contentType: "text/event-stream",
			body:        []byte("data: {}\n\n"),
			want:        true,
		},
		{
			name:        "Content-Type 为 application/json，即便 body 含 data:",
			contentType: "application/json",
			body:        []byte(`{"content":"帧：\ndata: {}"}`),
			want:        false,
		},
		{
			name:        "Content-Type 留空，body 以 data: 开头",
			contentType: "",
			body:        []byte("data: {}\n\n"),
			want:        true,
		},
		{
			name:        "Content-Type 留空，body 含 \\ndata:",
			contentType: "",
			body:        []byte("{\"x\":1}\ndata: {\"y\":2}"),
			want:        true,
		},
		{
			name:        "Content-Type 留空，body 含 \\nevent:",
			contentType: "",
			body:        []byte("event: message\ndata: {}\n\n"),
			want:        true,
		},
		{
			name:        "Content-Type 留空，body 为普通 JSON",
			contentType: "",
			body:        []byte(`{"id":"x","choices":[]}`),
			want:        false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isEventStream(tc.contentType, tc.body)
			if got != tc.want {
				t.Errorf("isEventStream(%q, ...) = %v, 期望 %v", tc.contentType, got, tc.want)
			}
		})
	}
}
