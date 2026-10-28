package plugin

import (
	"context"
	"testing"
)

func TestTestGroupManager_LoadBuiltinGroups(t *testing.T) {
	mgr := NewTestGroupManager()

	expectedGroups := []string{
		"reasoning-basic",
		"reasoning-advanced",
		"math-basic",
		"coding-basic",
		"creative-writing",
		"knowledge-general",
	}

	for _, id := range expectedGroups {
		group, ok := mgr.GetGroup(id)
		if !ok {
			t.Errorf("内置测试分组 %s 未加载", id)
			continue
		}
		if group.ID != id {
			t.Errorf("测试分组 ID 不匹配: got %s, want %s", group.ID, id)
		}
		if len(group.Prompts) == 0 {
			t.Errorf("测试分组 %s 没有测试用例", id)
		}
		for _, prompt := range group.Prompts {
			if prompt.ID == "" {
				t.Errorf("测试分组 %s 中有测试用例缺少 ID", id)
			}
			if prompt.Content == "" {
				t.Errorf("测试用例 %s 内容为空", prompt.ID)
			}
			if prompt.ExpectedMin < 0 || prompt.ExpectedMin > 100 {
				t.Errorf("测试用例 %s 期望最低分数越界: %v", prompt.ID, prompt.ExpectedMin)
			}
		}
	}
}

func TestTestGroupManager_ListGroups(t *testing.T) {
	mgr := NewTestGroupManager()
	groups := mgr.ListGroups()

	if len(groups) < 6 {
		t.Errorf("期望至少 6 个内置测试分组，实际: %d", len(groups))
	}

	// 确保每个分组都有有效的分类
	validCategories := map[string]bool{
		"reasoning": true,
		"math":      true,
		"coding":    true,
		"creative":  true,
		"knowledge": true,
		"logic":     true,
	}

	for _, group := range groups {
		if !validCategories[group.Category] {
			t.Errorf("测试分组 %s 的分类 %s 无效", group.ID, group.Category)
		}
	}
}

func TestTestGroupManager_AddGroup(t *testing.T) {
	mgr := NewTestGroupManager()

	customGroup := &TestGroup{
		ID:          "custom-test",
		Name:        "自定义测试",
		Description: "测试自定义分组功能",
		Category:    "custom",
		Prompts: []TestPrompt{
			{
				ID:          "custom-1",
				Title:       "测试提示",
				Content:     "这是一个测试提示",
				Difficulty:  "easy",
				ExpectedMin: 50,
				Tags:        []string{"test"},
			},
		},
	}

	err := mgr.AddGroup(customGroup)
	if err != nil {
		t.Fatalf("添加自定义测试分组失败: %v", err)
	}

	retrieved, ok := mgr.GetGroup("custom-test")
	if !ok {
		t.Fatal("无法获取刚添加的测试分组")
	}
	if retrieved.ID != "custom-test" {
		t.Errorf("获取的测试分组 ID 不匹配")
	}

	// 测试重复添加
	err = mgr.AddGroup(customGroup)
	if err == nil {
		t.Error("重复添加测试分组应该失败")
	}

	// 测试空 ID
	emptyIDGroup := &TestGroup{Name: "无效分组"}
	err = mgr.AddGroup(emptyIDGroup)
	if err == nil {
		t.Error("添加空 ID 的测试分组应该失败")
	}
}

func TestTestGroupManager_AddModel(t *testing.T) {
	mgr := NewTestGroupManager()

	model := &ModelTestConfig{
		Name:     "test-model",
		Provider: "openai",
		Model:    "gpt-4",
		BaseURL:  "https://api.openai.com/v1",
		APIKey:   "sk-test",
	}

	err := mgr.AddModel(model)
	if err != nil {
		t.Fatalf("添加模型配置失败: %v", err)
	}

	retrieved, ok := mgr.GetModel("test-model")
	if !ok {
		t.Fatal("无法获取刚添加的模型配置")
	}
	if retrieved.Name != "test-model" {
		t.Errorf("获取的模型名称不匹配")
	}

	// 测试重复添加
	err = mgr.AddModel(model)
	if err == nil {
		t.Error("重复添加模型配置应该失败")
	}

	// 测试空名称
	emptyNameModel := &ModelTestConfig{Provider: "test"}
	err = mgr.AddModel(emptyNameModel)
	if err == nil {
		t.Error("添加空名称的模型配置应该失败")
	}
}

func TestTestGroupManager_ListModels(t *testing.T) {
	mgr := NewTestGroupManager()

	// 初始应该没有模型
	models := mgr.ListModels()
	if len(models) != 0 {
		t.Errorf("初始状态应该没有模型，实际: %d", len(models))
	}

	// 添加几个模型
	model1 := &ModelTestConfig{
		Name:     "model-1",
		Provider: "openai",
		Model:    "gpt-4",
	}
	model2 := &ModelTestConfig{
		Name:     "model-2",
		Provider: "anthropic",
		Model:    "claude-3",
	}

	_ = mgr.AddModel(model1)
	_ = mgr.AddModel(model2)

	models = mgr.ListModels()
	if len(models) != 2 {
		t.Errorf("期望 2 个模型，实际: %d", len(models))
	}
}

func TestTestGroupManager_RunTest(t *testing.T) {
	mgr := NewTestGroupManager()

	// 注册测试模型
	model := &ModelTestConfig{
		Name:     "test-model",
		Provider: "test",
		Model:    "test-v1",
		BaseURL:  "https://test.example.com/v1",
		APIKey:   "test-key",
	}
	err := mgr.AddModel(model)
	if err != nil {
		t.Fatalf("添加模型失败: %v", err)
	}

	// 创建评分器（使用测试配置）
	cfg := &Config{
		IQAPI: IQAPIConfig{
			BaseURL:    "https://test.example.com/v1",
			Model:      "test-judge",
			TimeoutSec: 30,
			MaxRetries: 2,
		},
	}
	scorer := newScorer(cfg, nil)

	ctx := context.Background()

	// 测试不存在的分组
	_, err = mgr.RunTest(ctx, "non-existent", "test-model", scorer)
	if err == nil {
		t.Error("测试不存在的分组应该失败")
	}

	// 测试不存在的模型
	_, err = mgr.RunTest(ctx, "reasoning-basic", "non-existent-model", scorer)
	if err == nil {
		t.Error("测试不存在的模型应该失败")
	}

	// 注意：这里不实际运行完整测试，因为需要真实的评分 API
	// 在真实环境中，应该使用 mock scorer 或集成测试
}

func TestTestGroupManager_ExportTestRun(t *testing.T) {
	mgr := NewTestGroupManager()

	run := &TestRun{
		ID:        "test-run-1",
		GroupID:   "reasoning-basic",
		ModelName: "test-model",
		Status:    "completed",
		Results: []TestResult{
			{
				PromptID:    "test-1",
				PromptTitle: "测试用例",
				Score: &score{
					Overall:         75.5,
					ReasoningDepth:  80.0,
					LogicCoherence:  85.0,
					TaskCompletion:  70.0,
					JuiceEfficiency: 0.6,
				},
				PromptTokens: 100,
				OutputTokens: 200,
				Duration:     1000,
			},
		},
		Summary: TestRunSummary{
			TotalTests:      1,
			SuccessfulTests: 1,
			AvgOverall:      75.5,
		},
	}

	data, err := mgr.ExportTestRun(run)
	if err != nil {
		t.Fatalf("导出测试运行失败: %v", err)
	}

	if len(data) == 0 {
		t.Error("导出的数据为空")
	}

	// 验证可以重新解析
	var imported TestRun
	err = json.Unmarshal(data, &imported)
	if err != nil {
		t.Fatalf("重新解析导出数据失败: %v", err)
	}

	if imported.ID != run.ID {
		t.Errorf("导入的 ID 不匹配: got %s, want %s", imported.ID, run.ID)
	}
}

func TestTestGroupManager_CompareTestRuns(t *testing.T) {
	mgr := NewTestGroupManager()

	run1 := &TestRun{
		ID:        "run-1",
		ModelName: "model-v1",
		Summary: TestRunSummary{
			TotalTests:         3,
			SuccessfulTests:    3,
			AvgOverall:         70.0,
			AvgReasoningDepth:  75.0,
			AvgLogicCoherence:  80.0,
			AvgTaskCompletion:  65.0,
			AvgJuiceEfficiency: 0.5,
			TotalTokens:        1000,
			TotalDuration:      3000,
		},
	}

	run2 := &TestRun{
		ID:        "run-2",
		ModelName: "model-v2",
		Summary: TestRunSummary{
			TotalTests:         3,
			SuccessfulTests:    3,
			AvgOverall:         77.0,
			AvgReasoningDepth:  80.0,
			AvgLogicCoherence:  85.0,
			AvgTaskCompletion:  70.0,
			AvgJuiceEfficiency: 0.6,
			TotalTokens:        1200,
			TotalDuration:      3500,
		},
	}

	comparison := mgr.CompareTestRuns(run1, run2)

	// 验证比较结果包含必要字段
	if comparison["run1_id"] != "run-1" {
		t.Error("比较结果缺少 run1_id")
	}
	if comparison["run2_id"] != "run-2" {
		t.Error("比较结果缺少 run2_id")
	}

	// 验证分数差异
	overallDiff, ok := comparison["overall_diff"].(float64)
	if !ok {
		t.Error("overall_diff 类型错误")
	}
	expectedDiff := 7.0 // 77.0 - 70.0
	if overallDiff != expectedDiff {
		t.Errorf("overall_diff 计算错误: got %v, want %v", overallDiff, expectedDiff)
	}

	// 验证维度差异
	dimDiffs, ok := comparison["dimension_diffs"].(map[string]float64)
	if !ok {
		t.Fatal("dimension_diffs 类型错误")
	}

	expectedReasoningDiff := 5.0 // 80.0 - 75.0
	if dimDiffs["reasoning_depth"] != expectedReasoningDiff {
		t.Errorf("reasoning_depth diff 错误: got %v, want %v",
			dimDiffs["reasoning_depth"], expectedReasoningDiff)
	}

	// 验证效率指标
	efficiency, ok := comparison["efficiency"].(map[string]interface{})
	if !ok {
		t.Fatal("efficiency 类型错误")
	}

	tokenDiff, ok := efficiency["total_tokens_diff"].(int)
	if !ok {
		t.Error("total_tokens_diff 类型错误")
	}
	expectedTokenDiff := 200 // 1200 - 1000
	if tokenDiff != expectedTokenDiff {
		t.Errorf("total_tokens_diff 错误: got %v, want %v", tokenDiff, expectedTokenDiff)
	}
}

func TestTestPrompt_ValidDifficulty(t *testing.T) {
	mgr := NewTestGroupManager()
	validDifficulties := map[string]bool{
		"easy":   true,
		"medium": true,
		"hard":   true,
		"expert": true,
	}

	for _, group := range mgr.ListGroups() {
		for _, prompt := range group.Prompts {
			if !validDifficulties[prompt.Difficulty] {
				t.Errorf("测试用例 %s 的难度 %s 无效", prompt.ID, prompt.Difficulty)
			}
		}
	}
}

func TestTestGroupCategories(t *testing.T) {
	mgr := NewTestGroupManager()
	categoryCount := make(map[string]int)

	for _, group := range mgr.ListGroups() {
		categoryCount[group.Category]++
	}

	// 确保有多样化的分类
	if len(categoryCount) < 4 {
		t.Errorf("测试分类不够多样化，只有 %d 个分类", len(categoryCount))
	}

	// 确保 reasoning 分类有多个分组
	if categoryCount["reasoning"] < 2 {
		t.Errorf("reasoning 分类应该有多个分组，实际: %d", categoryCount["reasoning"])
	}
}
