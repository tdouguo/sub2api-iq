package plugin

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/feeeei/sub2api-plugin-framework/plugintest"
	"github.com/feeeei/sub2api-plugin-framework/sdk/hostsvc"
)

// hostClient 返回已与内存宿主服务完成握手的客户端。
// 它复用插件自身的 SetHostServices 回调，因此测的是真实握手链路而非伪造对象。
func hostClient(t *testing.T, h *plugintest.Harness) *hostsvc.Client {
	t.Helper()
	p, ok := h.Plugin.(*Plugin)
	if !ok {
		t.Fatalf("插件类型不符: %T", h.Plugin)
	}
	_, _, host := p.current()
	if host == nil {
		t.Fatal("宿主服务未握手，插件拿不到 hostsvc.Client")
	}
	return host
}

func testConfig(t *testing.T, raw string) *Config {
	t.Helper()
	cfg, err := Codec.Decode([]byte(raw))
	if err != nil {
		t.Fatalf("解析测试配置失败: %v", err)
	}
	return &cfg
}

func sampleConversation(accountID int64, requestID string, at time.Time) *conversation {
	return &conversation{
		RequestID:      requestID,
		AccountID:      accountID,
		Model:          "gpt-5",
		UserPrompt:     "解释一下快速排序",
		AssistantReply: "快速排序采用分治…",
		PromptTokens:   100,
		OutputTokens:   200,
		ReasoningToks:  50,
		TotalTokens:    300,
		At:             at,
	}
}

func sampleScore() *score {
	return &score{
		ReasoningDepth:  80,
		LogicCoherence:  90,
		TaskCompletion:  85,
		JuiceEfficiency: 0.5,
		Overall:         82.5,
		Analysis:        "结构清晰",
	}
}

// TestStorageRoundTrip 覆盖「写入 → 按键前缀读回」的完整链路。
func TestStorageRoundTrip(t *testing.T) {
	h := plugintest.New(t, New(info(t)))
	ctx := context.Background()
	host := hostClient(t, h)
	kv := host.KV()

	at := time.Now()
	if err := saveRecord(ctx, host, 42, sampleConversation(42, "req-1", at), sampleScore(), testConfig(t, `{}`)); err != nil {
		t.Fatalf("写入记录失败: %v", err)
	}

	records, err := loadRecords(ctx, kv, 42, 10)
	if err != nil {
		t.Fatalf("读取记录失败: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("期望 1 条记录，得到 %d", len(records))
	}
	entry := records[0]
	if entry.AccountID != 42 || entry.RequestID != "req-1" {
		t.Errorf("记录字段不符: %+v", entry)
	}
	if entry.Overall != 82.5 || entry.JuiceEfficiency != 0.5 {
		t.Errorf("评分不符: %+v", entry)
	}
	if entry.TotalTokens != 300 || entry.ReasoningTokens != 50 {
		t.Errorf("Token 统计不符: %+v", entry)
	}

	// 不同账号的键互不干扰。
	if other, err := loadRecords(ctx, kv, 43, 10); err != nil || len(other) != 0 {
		t.Errorf("账号 43 不应有记录: %v %d", err, len(other))
	}
}

// TestStorageConcurrentWritesLoseNothing 是本文件最关键的用例。
//
// 早期实现在一个共享的「账号 → ID 列表」键上做 KVGet → append → KVSet，
// 并发评测时彼此覆盖，记录会静默丢失。现在每条记录独立成键，因此并发写不丢数据。
func TestStorageConcurrentWritesLoseNothing(t *testing.T) {
	h := plugintest.New(t, New(info(t)))
	ctx := context.Background()
	host := hostClient(t, h)
	kv := host.KV()
	cfg := testConfig(t, `{"capture":{"max_records_per_account":0}}`)

	const writers = 24
	base := time.Now()
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			at := base.Add(time.Duration(index) * time.Millisecond)
			conv := sampleConversation(7, "req-"+string(rune('a'+index)), at)
			errs[index] = saveRecord(ctx, host, 7, conv, sampleScore(), cfg)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 个写入失败: %v", i, err)
		}
	}

	records, err := loadRecords(ctx, kv, 7, 100)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(records) != writers {
		t.Fatalf("并发写入后应保留 %d 条记录，实际 %d 条（说明存在读-改-写竞态）", writers, len(records))
	}
	// 应返回最新的在前。
	if !records[0].EvaluatedAt.After(records[len(records)-1].EvaluatedAt) {
		t.Error("记录应按时间倒序返回")
	}
}

// TestPruneRecords 覆盖每账号记录上限：裁剪最旧的，保留最新的。
func TestPruneRecords(t *testing.T) {
	h := plugintest.New(t, New(info(t)))
	ctx := context.Background()
	host := hostClient(t, h)
	kv := host.KV()
	cfg := testConfig(t, `{"capture":{"max_records_per_account":5}}`)

	base := time.Now()
	for i := 0; i < 12; i++ {
		at := base.Add(time.Duration(i) * time.Millisecond)
		if err := saveRecord(ctx, host, 9, sampleConversation(9, "r"+string(rune('a'+i)), at), sampleScore(), cfg); err != nil {
			t.Fatal(err)
		}
	}
	records, err := loadRecords(ctx, kv, 9, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 5 {
		t.Fatalf("每账号应只保留 5 条，实际 %d 条", len(records))
	}
	// 保留的必须是最新的 5 条。
	newest := records[0].EvaluatedAt
	if base.Add(11*time.Millisecond).Sub(newest) > time.Millisecond {
		t.Errorf("未保留最新记录: 最新=%s", newest)
	}
}

// TestRecordKeySortableAndSafe 保证键名在宿主 KV 的字符集与长度限制内，
// 且按字典序排序等价于按时间排序（裁剪逻辑依赖这一点）。
func TestRecordKeySortableAndSafe(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	older := recordKey(1, base, "req/a b:c")
	newer := recordKey(1, base.Add(time.Second), "req-2")

	if older >= newer {
		t.Errorf("键名未按时间递增: %q >= %q", older, newer)
	}
	if len(older) > hostsvc.MaxKeyLen {
		t.Errorf("键名超过宿主上限 %d: %d", hostsvc.MaxKeyLen, len(older))
	}
	// 宿主只接受 [A-Za-z0-9._-] 与 '/' 作为命名空间分隔。
	if strings.ContainsAny(older, " /:@") {
		t.Errorf("键名包含非法分隔符或字符: %q", older)
	}
	for _, part := range []string{accountKey(1), kvNamespace} {
		if len(part) > hostsvc.MaxNamespaceLen {
			t.Errorf("命名空间片段过长: %q", part)
		}
	}
}

func TestSanitizeKeyPart(t *testing.T) {
	cases := map[string]string{
		"req-123":  "req-123",
		"a/b c:d":  "abcd",
		"":         "",
		"!!!":      "",
		"safe._-1": "safe._-1",
	}
	for input, want := range cases {
		if got := sanitizeKeyPart(input); got != want {
			t.Errorf("sanitizeKeyPart(%q)=%q want %q", input, got, want)
		}
	}
	// 超长输入必须被截断到 64 个字符以内。
	if got := sanitizeKeyPart(strings.Repeat("x", 500)); len(got) > 64 {
		t.Errorf("键片段未被截断: %d", len(got))
	}
}
