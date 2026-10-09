package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/feeeei/sub2api-plugin-framework/sdk/hostsvc"
)

// record 是落库的一条评测记录。
type record struct {
	RecordID        string    `json:"record_id"`
	RequestID       string    `json:"request_id"`
	AccountID       int64     `json:"account_id"`
	Model           string    `json:"model"`
	Overall         float64   `json:"overall"`
	ReasoningDepth  float64   `json:"reasoning_depth"`
	JuiceEfficiency float64   `json:"juice_efficiency"`
	LogicCoherence  float64   `json:"logic_coherence"`
	TaskCompletion  float64   `json:"task_completion"`
	Analysis        string    `json:"analysis"`
	PromptTokens    int       `json:"prompt_tokens"`
	OutputTokens    int       `json:"output_tokens"`
	ReasoningTokens int       `json:"reasoning_tokens"`
	TotalTokens     int       `json:"total_tokens"`
	EvaluatedAt     time.Time `json:"evaluated_at"`
}

// recordKey 为每条记录生成独立键：<account>.<毫秒时间戳>-<request_id 片段>。
//
// 关键设计：不在共享的「账号 → ID 列表」键上做读-改-写。
// 并发评测时 KVGet → append → KVSet 会互相覆盖（丢记录），
// 而宿主 KV 不提供 CAS，因此改用前缀列举 + 按键名排序，天然无竞态。
//
// 分隔符必须用 "."：宿主只接受 [A-Za-z0-9._-]，用 "/" 或 ":" 会被判为
// InvalidArgument。用 "." 还能避免账号 4 与 42 的前缀互相误匹配。
func recordKey(accountID int64, at time.Time, requestID string) string {
	suffix := sanitizeKeyPart(requestID)
	if suffix == "" {
		suffix = "anon"
	}
	return fmt.Sprintf("%s.%013d-%s", accountKey(accountID), at.UnixMilli(), suffix)
}

// accountPrefix 是该账号所有记录键的公共前缀，用于 KVList 过滤。
func accountPrefix(accountID int64) string {
	return accountKey(accountID) + "."
}

// sanitizeKeyPart 把外部字符串约束成宿主 KV 允许的字符集。
// 宿主只接受 [A-Za-z0-9._-]，且键长上限 256。
func sanitizeKeyPart(raw string) string {
	var builder strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-', r == '_', r == '.':
			builder.WriteRune(r)
		}
		if builder.Len() >= 64 {
			break
		}
	}
	return builder.String()
}

// saveRecord 写入评测结果，并按配置裁剪该账号下的历史记录。
func saveRecord(ctx context.Context, host *hostsvc.Client, accountID int64, conv *conversation, result *score, cfg *Config) error {
	if host == nil {
		return nil
	}
	kv := host.KV()
	entry := record{
		RecordID:        recordKey(accountID, conv.At, conv.RequestID),
		RequestID:       conv.RequestID,
		AccountID:       accountID,
		Model:           conv.Model,
		Overall:         result.Overall,
		ReasoningDepth:  result.ReasoningDepth,
		JuiceEfficiency: result.JuiceEfficiency,
		LogicCoherence:  result.LogicCoherence,
		TaskCompletion:  result.TaskCompletion,
		Analysis:        result.Analysis,
		PromptTokens:    conv.PromptTokens,
		OutputTokens:    conv.OutputTokens,
		ReasoningTokens: conv.ReasoningToks,
		TotalTokens:     conv.TotalTokens,
		EvaluatedAt:     conv.At,
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if len(payload) > hostsvc.MaxValueBytes {
		// 分析文本可能是元凶，截断后重试一次。
		entry.Analysis = truncateRunes(entry.Analysis, 500)
		if payload, err = json.Marshal(entry); err != nil {
			return err
		}
		if len(payload) > hostsvc.MaxValueBytes {
			return fmt.Errorf("评测记录超过宿主 KV 单值上限 %d 字节", hostsvc.MaxValueBytes)
		}
	}

	ttl := time.Duration(cfg.Capture.RecordTTLHours) * time.Hour
	if err := kv.Set(ctx, kvNamespace, entry.RecordID, payload, ttl); err != nil {
		return err
	}
	pruneRecords(ctx, kv, accountID, cfg.Capture.MaxRecordsPerAccount)
	return nil
}

// pruneRecords 保留最新的 max 条记录。
//
// 时间戳是零填充的 13 位毫秒，字典序即时间序，因此按名字升序排列后
// 前面的就是最旧的记录，无需读取内容。
func pruneRecords(ctx context.Context, kv *hostsvc.KV, accountID int64, max int) {
	if kv == nil || max <= 0 {
		return
	}
	keys, err := kv.List(ctx, kvNamespace, accountPrefix(accountID), max+64)
	if err != nil || len(keys) <= max {
		return
	}
	sort.Strings(keys)
	for _, key := range keys[:len(keys)-max] {
		_ = kv.Delete(ctx, kvNamespace, key)
	}
}

// loadRecords 读取某账号最近的评测记录，最新的在前。
func loadRecords(ctx context.Context, kv *hostsvc.KV, accountID int64, limit int) ([]record, error) {
	if kv == nil {
		return nil, hostsvc.ErrUnavailable
	}
	if limit <= 0 {
		limit = 50
	}
	keys, err := kv.List(ctx, kvNamespace, accountPrefix(accountID), limit)
	if err != nil {
		return nil, err
	}
	sort.Strings(keys)
	records := make([]record, 0, len(keys))
	for i := len(keys) - 1; i >= 0; i-- {
		payload, found, err := kv.Get(ctx, kvNamespace, keys[i])
		if err != nil || !found {
			continue
		}
		var entry record
		if err := json.Unmarshal(payload, &entry); err != nil {
			continue
		}
		records = append(records, entry)
	}
	return records, nil
}
