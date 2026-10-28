package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/feeeei/sub2api-plugin-framework/sdk/hostsvc"
	"github.com/feeeei/sub2api-plugin-framework/sdk/transport"
)

// TestRunner 负责执行测试分组。
type TestRunner struct {
	cfg    *Config
	scorer *scorer
	kv     *hostsvc.KV
}

// newTestRunner 创建测试执行器。
func newTestRunner(cfg *Config, scorer *scorer, kv *hostsvc.KV) *TestRunner {
	return &TestRunner{
		cfg:    cfg,
		scorer: scorer,
		kv:     kv,
	}
}

// TestGroupResult 是单个测试分组的运行结果。
type TestGroupResult struct {
	GroupID    string             `json:"group_id"`
	GroupName  string             `json:"group_name"`
	StartTime  time.Time          `json:"start_time"`
	EndTime    time.Time          `json:"end_time"`
	Duration   int64              `json:"duration_ms"`
	TotalTests int                `json:"total_tests"`
	Passed     int                `json:"passed"`
	Failed     int                `json:"failed"`
	Results    []TestPromptResult `json:"results"`
	Summary    TestGroupSummary   `json:"summary"`
}

// TestPromptResult 是单个测试用例的执行结果。
type TestPromptResult struct {
	PromptID        string    `json:"prompt_id"`
	PromptTitle     string    `json:"prompt_title"`
	Score           *score    `json:"score,omitempty"`
	Answer          string    `json:"answer"`
	ExpectedAnswer  string    `json:"expected_answer"`
	Matched         bool      `json:"matched"`
	Duration        int64     `json:"duration_ms"`
	PromptTokens    int       `json:"prompt_tokens"`
	OutputTokens    int       `json:"output_tokens"`
	ReasoningTokens int       `json:"reasoning_tokens"`
	Error           string    `json:"error,omitempty"`
	Timestamp       time.Time `json:"timestamp"`
}

// TestGroupSummary 是测试分组的汇总统计。
type TestGroupSummary struct {
	AvgOverall           float64 `json:"avg_overall"`
	AvgReasoningDepth    float64 `json:"avg_reasoning_depth"`
	AvgLogicCoherence    float64 `json:"avg_logic_coherence"`
	AvgTaskCompletion    float64 `json:"avg_task_completion"`
	AvgJuiceEfficiency   float64 `json:"avg_juice_efficiency"`
	TotalTokens          int     `json:"total_tokens"`
	TotalReasoningTokens int     `json:"total_reasoning_tokens"`
	PassRate             float64 `json:"pass_rate"`
}

// RunAllGroups 执行所有启用的测试分组。
func (r *TestRunner) RunAllGroups(ctx context.Context) ([]TestGroupResult, error) {
	if !r.cfg.TestGroups.Enabled {
		return nil, fmt.Errorf("测试分组功能未启用")
	}

	if len(r.cfg.TestGroups.Groups) == 0 {
		return nil, fmt.Errorf("没有配置测试分组")
	}

	var results []TestGroupResult
	for groupID, group := range r.cfg.TestGroups.Groups {
		result, err := r.runGroup(ctx, groupID, group)
		if err != nil {
			// 单个分组失败不影响其他分组
			result = &TestGroupResult{
				GroupID:   groupID,
				GroupName: group.Name,
				StartTime: time.Now(),
				EndTime:   time.Now(),
				Failed:    len(group.Prompts),
			}
			result.Results = []TestPromptResult{{Error: err.Error()}}
		}
		results = append(results, *result)
	}

	return results, nil
}

// RunGroup 执行单个测试分组。
func (r *TestRunner) RunGroup(ctx context.Context, groupID string) (*TestGroupResult, error) {
	if !r.cfg.TestGroups.Enabled {
		return nil, fmt.Errorf("测试分组功能未启用")
	}

	group, ok := r.cfg.TestGroups.Groups[groupID]
	if !ok {
		return nil, fmt.Errorf("测试分组 %s 不存在", groupID)
	}

	return r.runGroup(ctx, groupID, group)
}

// runGroup 实际执行测试分组。
func (r *TestRunner) runGroup(ctx context.Context, groupID string, group TestGroup) (*TestGroupResult, error) {
	result := &TestGroupResult{
		GroupID:    groupID,
		GroupName:  group.Name,
		StartTime:  time.Now(),
		TotalTests: len(group.Prompts),
		Results:    make([]TestPromptResult, 0, len(group.Prompts)),
	}

	var totalOverall, totalReasoning, totalLogic, totalTask, totalJuice float64
	var successCount int

	for _, prompt := range group.Prompts {
		promptResult := r.runPrompt(ctx, prompt)
		result.Results = append(result.Results, promptResult)

		if promptResult.Error == "" {
			if promptResult.Matched {
				result.Passed++
			} else {
				result.Failed++
			}

			if promptResult.Score != nil {
				successCount++
				totalOverall += promptResult.Score.Overall
				totalReasoning += promptResult.Score.ReasoningDepth
				totalLogic += promptResult.Score.LogicCoherence
				totalTask += promptResult.Score.TaskCompletion
				totalJuice += promptResult.Score.JuiceEfficiency
				result.Summary.TotalTokens += promptResult.PromptTokens + promptResult.OutputTokens
				result.Summary.TotalReasoningTokens += promptResult.ReasoningTokens
			}
		} else {
			result.Failed++
		}
	}

	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime).Milliseconds()

	if successCount > 0 {
		result.Summary.AvgOverall = totalOverall / float64(successCount)
		result.Summary.AvgReasoningDepth = totalReasoning / float64(successCount)
		result.Summary.AvgLogicCoherence = totalLogic / float64(successCount)
		result.Summary.AvgTaskCompletion = totalTask / float64(successCount)
		result.Summary.AvgJuiceEfficiency = totalJuice / float64(successCount)
	}

	if result.TotalTests > 0 {
		result.Summary.PassRate = float64(result.Passed) / float64(result.TotalTests) * 100
	}

	// 存储结果到宿主 KV
	r.storeResult(ctx, result)

	return result, nil
}

// runPrompt 执行单个测试提示词。
func (r *TestRunner) runPrompt(ctx context.Context, prompt TestPrompt) TestPromptResult {
	result := TestPromptResult{
		PromptID:       prompt.ID,
		PromptTitle:    prompt.Title,
		ExpectedAnswer: prompt.ExpectedAnswer,
		Timestamp:      time.Now(),
	}

	start := time.Now()

	// 调用模型获取答案
	answer, usage, err := r.callModel(ctx, prompt.Content)
	result.Duration = time.Since(start).Milliseconds()

	if err != nil {
		result.Error = err.Error()
		return result
	}

	result.Answer = answer
	result.PromptTokens = usage.PromptTokens
	result.OutputTokens = usage.CompletionTokens
	result.ReasoningTokens = usage.ReasoningTokens

	// 比对答案
	result.Matched = r.matchAnswer(answer, prompt.ExpectedAnswer)

	// 构造对话用于评分
	conv := &conversation{
		Model:          r.cfg.IQAPI.Model,
		UserPrompt:     prompt.Content,
		AssistantReply: answer,
		PromptTokens:   usage.PromptTokens,
		OutputTokens:   usage.CompletionTokens,
		ReasoningToks:  usage.ReasoningTokens,
	}

	// 调用评分器
	s, err := r.scorer.score(ctx, conv)
	if err != nil {
		// 评分失败不影响测试结果，只是没有详细分数
		result.Error = fmt.Sprintf("评分失败: %v", err)
	} else {
		result.Score = s
	}

	return result
}

// callModel 调用模型获取答案（复用 scorer 的 HTTP 调用模式）。
func (r *TestRunner) callModel(ctx context.Context, prompt string) (string, *usageInfo, error) {
	if r.cfg.IQAPI.BaseURL == "" {
		return "", nil, transport.NotSent(transport.CodeNotConfigured, "iq_api.base_url 为空")
	}
	if r.scorer == nil || r.scorer.pool == nil {
		return "", nil, transport.NotSent(transport.CodeNotConfigured, "传输池未初始化")
	}

	body, err := json.Marshal(map[string]any{
		"model": r.cfg.IQAPI.Model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"temperature": 0, // 固定温度保证可复现
		"max_tokens":  2000,
		"stream":      false,
	})
	if err != nil {
		return "", nil, err
	}

	endpoint := strings.TrimSuffix(r.cfg.IQAPI.BaseURL, "/") + "/chat/completions"
	timeout := time.Duration(r.cfg.IQAPI.TimeoutSec) * time.Second
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	if r.cfg.IQAPI.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.cfg.IQAPI.APIKey)
	}

	resp, err := r.scorer.pool.RoundTrip(req, "")
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens            int `json:"prompt_tokens"`
			CompletionTokens        int `json:"completion_tokens"`
			ReasoningTokens         int `json:"reasoning_tokens,omitempty"`
			CompletionTokensDetails struct {
				ReasoningTokens int `json:"reasoning_tokens,omitempty"`
			} `json:"completion_tokens_details,omitempty"`
		} `json:"usage"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", nil, err
	}

	if len(result.Choices) == 0 {
		return "", nil, fmt.Errorf("响应中没有 choices")
	}

	usage := &usageInfo{
		PromptTokens:     result.Usage.PromptTokens,
		CompletionTokens: result.Usage.CompletionTokens,
		ReasoningTokens:  result.Usage.ReasoningTokens,
	}
	if usage.ReasoningTokens == 0 && result.Usage.CompletionTokensDetails.ReasoningTokens > 0 {
		usage.ReasoningTokens = result.Usage.CompletionTokensDetails.ReasoningTokens
	}

	return result.Choices[0].Message.Content, usage, nil
}

// matchAnswer 比对答案是否匹配期望值。
func (r *TestRunner) matchAnswer(answer, expected string) bool {
	if expected == "" {
		return true // 没有期望答案时认为通过
	}

	// 尝试作为 JSON 比对
	var answerObj, expectedObj interface{}
	if json.Unmarshal([]byte(answer), &answerObj) == nil &&
		json.Unmarshal([]byte(expected), &expectedObj) == nil {
		answerJSON, _ := json.Marshal(answerObj)
		expectedJSON, _ := json.Marshal(expectedObj)
		return string(answerJSON) == string(expectedJSON)
	}

	// 回退到字符串比对
	return strings.TrimSpace(answer) == strings.TrimSpace(expected)
}

// storeResult 将测试结果存储到宿主 KV。
func (r *TestRunner) storeResult(ctx context.Context, result *TestGroupResult) {
	if r.kv == nil {
		return
	}

	key := fmt.Sprintf("testgroup.%s.%013d", sanitizeKeyPart(result.GroupID), result.StartTime.UnixMilli())
	data, err := json.Marshal(result)
	if err != nil {
		return
	}

	// 存储时使用 7 天 TTL
	ttl := time.Hour * 24 * 7
	_ = r.kv.Set(ctx, kvNamespace, key, data, ttl)
}

// usageInfo 记录 token 使用情况。
type usageInfo struct {
	PromptTokens     int
	CompletionTokens int
	ReasoningTokens  int
}
