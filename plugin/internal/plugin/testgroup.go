package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// TestGroup 代表一组相关的测试场景，用于系统化地评估模型表现。
type TestGroup struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Category    string       `json:"category"` // reasoning, coding, creative, logic, math, knowledge
	Prompts     []TestPrompt `json:"prompts"`
	CreatedAt   time.Time    `json:"created_at"`
}

// TestPrompt 是测试组内的单个测试用例。
type TestPrompt struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Content     string            `json:"content"`
	Difficulty  string            `json:"difficulty"`   // easy, medium, hard, expert
	ExpectedMin float64           `json:"expected_min"` // 期望最低分数（用于回归检测）
	Tags        []string          `json:"tags"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// ModelTestConfig 定义要测试的模型配置。
type ModelTestConfig struct {
	Name        string            `json:"name"`
	Provider    string            `json:"provider"` // openai, anthropic, deepseek 等
	Model       string            `json:"model"`
	BaseURL     string            `json:"base_url"`
	APIKey      string            `json:"api_key"`
	Temperature float64           `json:"temperature,omitempty"`
	MaxTokens   int               `json:"max_tokens,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// TestRun 记录一次完整的测试执行。
type TestRun struct {
	ID        string         `json:"id"`
	GroupID   string         `json:"group_id"`
	ModelName string         `json:"model_name"`
	StartTime time.Time      `json:"start_time"`
	EndTime   time.Time      `json:"end_time"`
	Results   []TestResult   `json:"results"`
	Summary   TestRunSummary `json:"summary"`
	Status    string         `json:"status"` // running, completed, failed, partial
	Error     string         `json:"error,omitempty"`
}

// TestResult 记录单个测试提示词的评测结果。
type TestResult struct {
	PromptID        string    `json:"prompt_id"`
	PromptTitle     string    `json:"prompt_title"`
	RequestID       string    `json:"request_id,omitempty"`
	Score           *score    `json:"score,omitempty"`
	ResponsePreview string    `json:"response_preview"` // 响应前 200 字符
	PromptTokens    int       `json:"prompt_tokens"`
	OutputTokens    int       `json:"output_tokens"`
	ReasoningTokens int       `json:"reasoning_tokens"`
	Duration        int64     `json:"duration_ms"`
	Error           string    `json:"error,omitempty"`
	Timestamp       time.Time `json:"timestamp"`
}

// TestRunSummary 汇总测试运行的整体表现。
type TestRunSummary struct {
	TotalTests           int     `json:"total_tests"`
	SuccessfulTests      int     `json:"successful_tests"`
	FailedTests          int     `json:"failed_tests"`
	AvgOverall           float64 `json:"avg_overall"`
	AvgReasoningDepth    float64 `json:"avg_reasoning_depth"`
	AvgLogicCoherence    float64 `json:"avg_logic_coherence"`
	AvgTaskCompletion    float64 `json:"avg_task_completion"`
	AvgJuiceEfficiency   float64 `json:"avg_juice_efficiency"`
	TotalTokens          int     `json:"total_tokens"`
	TotalReasoningTokens int     `json:"total_reasoning_tokens"`
	TotalDuration        int64   `json:"total_duration_ms"`
}

// TestGroupManager 管理测试分组和模型测试。
type TestGroupManager struct {
	groups map[string]*TestGroup
	models map[string]*ModelTestConfig
}

// NewTestGroupManager 创建测试分组管理器。
func NewTestGroupManager() *TestGroupManager {
	mgr := &TestGroupManager{
		groups: make(map[string]*TestGroup),
		models: make(map[string]*ModelTestConfig),
	}
	mgr.loadBuiltinGroups()
	return mgr
}

// loadBuiltinGroups 加载内置测试分组（参考 iqtest.codezsy.com）。
func (m *TestGroupManager) loadBuiltinGroups() {
	m.groups["reasoning-basic"] = &TestGroup{
		ID:          "reasoning-basic",
		Name:        "基础推理",
		Description: "测试模型的基本逻辑推理能力",
		Category:    "reasoning",
		CreatedAt:   time.Now(),
		Prompts: []TestPrompt{
			{
				ID:          "reasoning-basic-1",
				Title:       "简单三段论",
				Content:     "所有哺乳动物都是温血动物。所有狗都是哺乳动物。请问：所有狗都是温血动物吗？请解释你的推理过程。",
				Difficulty:  "easy",
				ExpectedMin: 70,
				Tags:        []string{"logic", "deduction"},
			},
			{
				ID:          "reasoning-basic-2",
				Title:       "逻辑关系推理",
				Content:     "小明比小红高，小红比小李高。请问：小明和小李谁更高？详细说明推理步骤。",
				Difficulty:  "easy",
				ExpectedMin: 75,
				Tags:        []string{"comparison", "transitive"},
			},
			{
				ID:          "reasoning-basic-3",
				Title:       "假设推理",
				Content:     "如果明天下雨，我就不去公园。今天天气预报说明天会下雨。请问我明天会去公园吗？解释你的推理。",
				Difficulty:  "medium",
				ExpectedMin: 70,
				Tags:        []string{"conditional", "modus-ponens"},
			},
		},
	}

	m.groups["reasoning-advanced"] = &TestGroup{
		ID:          "reasoning-advanced",
		Name:        "高级推理",
		Description: "测试复杂逻辑推理和多步骤思考能力",
		Category:    "reasoning",
		CreatedAt:   time.Now(),
		Prompts: []TestPrompt{
			{
				ID:          "reasoning-advanced-1",
				Title:       "多条件推理",
				Content:     "在一个小镇上，理发师给所有不给自己理发的人理发，且只给这些人理发。请问：理发师给自己理发吗？这是一个悖论吗？请深入分析。",
				Difficulty:  "hard",
				ExpectedMin: 60,
				Tags:        []string{"paradox", "self-reference", "russell"},
			},
			{
				ID:          "reasoning-advanced-2",
				Title:       "概率推理",
				Content:     "有三个门，一个门后有奖品，两个门后是空的。你选择了一号门。主持人知道哪个门后有奖品，他打开了三号门，显示是空的。现在你可以选择换到二号门或坚持一号门。换门会提高中奖概率吗？为什么？",
				Difficulty:  "hard",
				ExpectedMin: 65,
				Tags:        []string{"probability", "monty-hall", "bayesian"},
			},
			{
				ID:          "reasoning-advanced-3",
				Title:       "因果推理",
				Content:     "研究发现，每天喝咖啡的人平均寿命更长。我们能得出结论说咖啡能延长寿命吗？请从因果关系的角度分析这个问题，考虑可能的混淆因素。",
				Difficulty:  "expert",
				ExpectedMin: 70,
				Tags:        []string{"causation", "correlation", "confounding"},
			},
		},
	}

	m.groups["math-basic"] = &TestGroup{
		ID:          "math-basic",
		Name:        "基础数学",
		Description: "测试基本数学计算和应用能力",
		Category:    "math",
		CreatedAt:   time.Now(),
		Prompts: []TestPrompt{
			{
				ID:          "math-basic-1",
				Title:       "四则运算",
				Content:     "计算：(23 + 47) × 3 - 18 ÷ 2 = ? 请写出详细计算步骤。",
				Difficulty:  "easy",
				ExpectedMin: 80,
				Tags:        []string{"arithmetic", "order-of-operations"},
			},
			{
				ID:          "math-basic-2",
				Title:       "百分比计算",
				Content:     "一件商品原价 200 元，打 8 折后又在折扣价基础上再减 10%。最终价格是多少？请展示计算过程。",
				Difficulty:  "medium",
				ExpectedMin: 75,
				Tags:        []string{"percentage", "discount"},
			},
			{
				ID:          "math-basic-3",
				Title:       "应用题",
				Content:     "一辆汽车以 60 公里/小时的速度行驶了 2.5 小时，然后以 80 公里/小时的速度又行驶了 1.5 小时。总共行驶了多少公里？平均速度是多少？",
				Difficulty:  "medium",
				ExpectedMin: 70,
				Tags:        []string{"distance", "speed", "average"},
			},
		},
	}

	m.groups["coding-basic"] = &TestGroup{
		ID:          "coding-basic",
		Name:        "基础编程",
		Description: "测试基本编程思维和算法能力",
		Category:    "coding",
		CreatedAt:   time.Now(),
		Prompts: []TestPrompt{
			{
				ID:          "coding-basic-1",
				Title:       "数组反转",
				Content:     "用 Python 写一个函数，将输入的列表反转。要求不使用内置的 reverse() 方法或切片 [::-1]。请解释你的算法思路。",
				Difficulty:  "easy",
				ExpectedMin: 70,
				Tags:        []string{"algorithm", "array", "python"},
			},
			{
				ID:          "coding-basic-2",
				Title:       "斐波那契数列",
				Content:     "写一个函数计算斐波那契数列的第 n 项。请提供递归和迭代两种实现，并分析它们的时间复杂度。",
				Difficulty:  "medium",
				ExpectedMin: 65,
				Tags:        []string{"recursion", "iteration", "complexity"},
			},
			{
				ID:          "coding-basic-3",
				Title:       "字符串处理",
				Content:     "写一个函数判断一个字符串是否是回文（正读和反读都一样）。要求：忽略大小写，忽略空格和标点符号。",
				Difficulty:  "medium",
				ExpectedMin: 70,
				Tags:        []string{"string", "palindrome", "parsing"},
			},
		},
	}

	m.groups["creative-writing"] = &TestGroup{
		ID:          "creative-writing",
		Name:        "创意写作",
		Description: "测试创造性思维和语言表达能力",
		Category:    "creative",
		CreatedAt:   time.Now(),
		Prompts: []TestPrompt{
			{
				ID:          "creative-writing-1",
				Title:       "故事开头",
				Content:     "用"那是一个下着雨的夜晚"开头，写一段 200 字的故事。要求有悬念，有画面感。",
				Difficulty:  "medium",
				ExpectedMin: 60,
				Tags:        []string{"narrative", "atmosphere"},
			},
			{
				ID:          "creative-writing-2",
				Title:       "概念类比",
				Content:     "用三个不同的比喻来解释"时间"这个概念，每个比喻都从不同的角度切入。",
				Difficulty:  "medium",
				ExpectedMin: 65,
				Tags:        []string{"metaphor", "analogy", "abstraction"},
			},
			{
				ID:          "creative-writing-3",
				Title:       "逆向思维",
				Content:     "写一篇短文，从"失败"的角度论述为什么失败比成功更有价值。要求论点新颖，论证充分。",
				Difficulty:  "hard",
				ExpectedMin: 60,
				Tags:        []string{"argumentation", "paradox", "perspective"},
			},
		},
	}

	m.groups["knowledge-general"] = &TestGroup{
		ID:          "knowledge-general",
		Name:        "通用知识",
		Description: "测试基本常识和知识整合能力",
		Category:    "knowledge",
		CreatedAt:   time.Now(),
		Prompts: []TestPrompt{
			{
				ID:          "knowledge-general-1",
				Title:       "科学常识",
				Content:     "为什么天空是蓝色的？请用简单易懂的语言解释光的散射原理。",
				Difficulty:  "easy",
				ExpectedMin: 70,
				Tags:        []string{"science", "physics", "explanation"},
			},
			{
				ID:          "knowledge-general-2",
				Title:       "历史事件",
				Content:     "简述工业革命对人类社会的三个主要影响，并解释它们之间的关联。",
				Difficulty:  "medium",
				ExpectedMin: 65,
				Tags:        []string{"history", "causation", "synthesis"},
			},
			{
				ID:          "knowledge-general-3",
				Title:       "跨领域整合",
				Content:     "从生物学、经济学和社会学三个角度分析"竞争"这个概念。它们有什么共同点和不同点？",
				Difficulty:  "hard",
				ExpectedMin: 60,
				Tags:        []string{"interdisciplinary", "synthesis", "comparison"},
			},
		},
	}
}

// GetGroup 获取指定 ID 的测试分组。
func (m *TestGroupManager) GetGroup(id string) (*TestGroup, bool) {
	g, ok := m.groups[id]
	return g, ok
}

// ListGroups 列出所有测试分组。
func (m *TestGroupManager) ListGroups() []*TestGroup {
	groups := make([]*TestGroup, 0, len(m.groups))
	for _, g := range m.groups {
		groups = append(groups, g)
	}
	return groups
}

// AddGroup 添加自定义测试分组。
func (m *TestGroupManager) AddGroup(group *TestGroup) error {
	if group.ID == "" {
		return fmt.Errorf("测试分组 ID 不能为空")
	}
	if _, exists := m.groups[group.ID]; exists {
		return fmt.Errorf("测试分组 %s 已存在", group.ID)
	}
	m.groups[group.ID] = group
	return nil
}

// AddModel 注册要测试的模型配置。
func (m *TestGroupManager) AddModel(model *ModelTestConfig) error {
	if model.Name == "" {
		return fmt.Errorf("模型名称不能为空")
	}
	if _, exists := m.models[model.Name]; exists {
		return fmt.Errorf("模型 %s 已注册", model.Name)
	}
	m.models[model.Name] = model
	return nil
}

// GetModel 获取模型配置。
func (m *TestGroupManager) GetModel(name string) (*ModelTestConfig, bool) {
	model, ok := m.models[name]
	return model, ok
}

// ListModels 列出所有注册的模型。
func (m *TestGroupManager) ListModels() []*ModelTestConfig {
	models := make([]*ModelTestConfig, 0, len(m.models))
	for _, model := range m.models {
		models = append(models, model)
	}
	return models
}

// RunTest 执行单个测试组对单个模型的评测。
func (m *TestGroupManager) RunTest(ctx context.Context, groupID string, modelName string, scorer *scorer) (*TestRun, error) {
	group, ok := m.GetGroup(groupID)
	if !ok {
		return nil, fmt.Errorf("测试分组 %s 不存在", groupID)
	}

	model, ok := m.GetModel(modelName)
	if !ok {
		return nil, fmt.Errorf("模型 %s 未注册", modelName)
	}

	run := &TestRun{
		ID:        fmt.Sprintf("%s-%s-%d", groupID, modelName, time.Now().Unix()),
		GroupID:   groupID,
		ModelName: modelName,
		StartTime: time.Now(),
		Status:    "running",
		Results:   make([]TestResult, 0, len(group.Prompts)),
	}

	var totalOverall, totalReasoning, totalLogic, totalTask, totalJuice float64
	var successCount int

	for _, prompt := range group.Prompts {
		result := m.runSingleTest(ctx, prompt, model, scorer)
		run.Results = append(run.Results, result)

		if result.Score != nil {
			successCount++
			totalOverall += result.Score.Overall
			totalReasoning += result.Score.ReasoningDepth
			totalLogic += result.Score.LogicCoherence
			totalTask += result.Score.TaskCompletion
			totalJuice += result.Score.JuiceEfficiency
			run.Summary.TotalTokens += result.PromptTokens + result.OutputTokens
			run.Summary.TotalReasoningTokens += result.ReasoningTokens
		} else {
			run.Summary.FailedTests++
		}
		run.Summary.TotalDuration += result.Duration
	}

	run.EndTime = time.Now()
	run.Summary.TotalTests = len(group.Prompts)
	run.Summary.SuccessfulTests = successCount

	if successCount > 0 {
		run.Summary.AvgOverall = totalOverall / float64(successCount)
		run.Summary.AvgReasoningDepth = totalReasoning / float64(successCount)
		run.Summary.AvgLogicCoherence = totalLogic / float64(successCount)
		run.Summary.AvgTaskCompletion = totalTask / float64(successCount)
		run.Summary.AvgJuiceEfficiency = totalJuice / float64(successCount)
	}

	if run.Summary.FailedTests == 0 {
		run.Status = "completed"
	} else if successCount > 0 {
		run.Status = "partial"
	} else {
		run.Status = "failed"
		run.Error = "所有测试都失败了"
	}

	return run, nil
}

// runSingleTest 执行单个测试用例。
func (m *TestGroupManager) runSingleTest(ctx context.Context, prompt TestPrompt, model *ModelTestConfig, scorer *scorer) TestResult {
	result := TestResult{
		PromptID:    prompt.ID,
		PromptTitle: prompt.Title,
		Timestamp:   time.Now(),
	}

	start := time.Now()

	// 构造对话用于评分（这里简化处理，实际应该调用模型 API 获取响应）
	conv := &conversation{
		Model:          model.Model,
		UserPrompt:     prompt.Content,
		AssistantReply: "[此处应为模型实际响应，当前为测试模式]",
		PromptTokens:   len(prompt.Content) / 4, // 粗略估算
		OutputTokens:   100,
		ReasoningToks:  50,
	}

	// 调用评分器
	s, err := scorer.score(ctx, conv)
	result.Duration = time.Since(start).Milliseconds()

	if err != nil {
		result.Error = err.Error()
		return result
	}

	result.Score = s
	result.PromptTokens = conv.PromptTokens
	result.OutputTokens = conv.OutputTokens
	result.ReasoningTokens = conv.ReasoningToks
	result.ResponsePreview = truncateRunes(conv.AssistantReply, 200)

	return result
}

// ExportTestRun 导出测试运行结果为 JSON。
func (m *TestGroupManager) ExportTestRun(run *TestRun) ([]byte, error) {
	return json.MarshalIndent(run, "", "  ")
}

// CompareTestRuns 比较两次测试运行的结果。
func (m *TestGroupManager) CompareTestRuns(run1, run2 *TestRun) map[string]interface{} {
	comparison := map[string]interface{}{
		"run1_id":    run1.ID,
		"run2_id":    run2.ID,
		"run1_model": run1.ModelName,
		"run2_model": run2.ModelName,
	}

	// 比较总体分数
	scoreDiff := run2.Summary.AvgOverall - run1.Summary.AvgOverall
	comparison["overall_diff"] = scoreDiff
	comparison["overall_change_percent"] = (scoreDiff / run1.Summary.AvgOverall) * 100

	// 比较各维度
	comparison["dimension_diffs"] = map[string]float64{
		"reasoning_depth":  run2.Summary.AvgReasoningDepth - run1.Summary.AvgReasoningDepth,
		"logic_coherence":  run2.Summary.AvgLogicCoherence - run1.Summary.AvgLogicCoherence,
		"task_completion":  run2.Summary.AvgTaskCompletion - run1.Summary.AvgTaskCompletion,
		"juice_efficiency": run2.Summary.AvgJuiceEfficiency - run1.Summary.AvgJuiceEfficiency,
	}

	// 比较效率指标
	comparison["efficiency"] = map[string]interface{}{
		"total_tokens_diff":          run2.Summary.TotalTokens - run1.Summary.TotalTokens,
		"total_duration_diff_ms":     run2.Summary.TotalDuration - run1.Summary.TotalDuration,
		"avg_token_per_test_diff":    float64(run2.Summary.TotalTokens-run1.Summary.TotalTokens) / float64(run1.Summary.TotalTests),
	}

	return comparison
}
