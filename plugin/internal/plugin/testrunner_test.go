package plugin

import (
	"context"
	"encoding/json"
	"testing"
)

func TestTestRunner_MatchAnswer(t *testing.T) {
	runner := &TestRunner{}

	tests := []struct {
		name     string
		answer   string
		expected string
		want     bool
	}{
		{
			name:     "JSON 数字匹配",
			answer:   `{"number":21}`,
			expected: `{"number":21}`,
			want:     true,
		},
		{
			name:     "JSON 数字不匹配",
			answer:   `{"number":22}`,
			expected: `{"number":21}`,
			want:     false,
		},
		{
			name:     "JSON 字段顺序无关",
			answer:   `{"a":1,"b":2}`,
			expected: `{"b":2,"a":1}`,
			want:     true,
		},
		{
			name:     "空期望值始终通过",
			answer:   "any answer",
			expected: "",
			want:     true,
		},
		{
			name:     "字符串精确匹配",
			answer:   "  hello world  ",
			expected: "hello world",
			want:     true,
		},
		{
			name:     "字符串不匹配",
			answer:   "hello",
			expected: "world",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runner.matchAnswer(tt.answer, tt.expected)
			if got != tt.want {
				t.Errorf("matchAnswer() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTestRunner_RunGroup_EmptyConfig(t *testing.T) {
	cfg := &Config{
		TestGroups: TestGroupsConfig{
			Enabled: false,
			Groups:  map[string]TestGroup{},
		},
	}

	runner := newTestRunner(cfg, nil, nil)
	ctx := context.Background()

	_, err := runner.RunAllGroups(ctx)
	if err == nil {
		t.Error("RunAllGroups() 应该在功能未启用时失败")
	}

	cfg.TestGroups.Enabled = true
	_, err = runner.RunAllGroups(ctx)
	if err == nil {
		t.Error("RunAllGroups() 应该在没有分组时失败")
	}
}

func TestTestRunner_RunGroup_BasicFlow(t *testing.T) {
	cfg := &Config{
		IQAPI: IQAPIConfig{
			BaseURL:    "https://test.example.com/v1",
			Model:      "test-model",
			TimeoutSec: 30,
		},
		TestGroups: TestGroupsConfig{
			Enabled: true,
			Groups: map[string]TestGroup{
				"test-group": {
					Name:        "测试分组",
					Description: "用于单元测试",
					Prompts: []TestPrompt{
						{
							ID:             "prompt-1",
							Title:          "测试提示",
							Content:        "这是一个测试提示",
							ExpectedAnswer: `{"result":"ok"}`,
							MinScore:       60,
						},
					},
				},
			},
		},
	}

	runner := newTestRunner(cfg, nil, nil)

	// 测试不存在的分组
	_, err := runner.RunGroup(context.Background(), "non-existent")
	if err == nil {
		t.Error("RunGroup() 应该在分组不存在时失败")
	}

	// 测试存在的分组（会因为没有真实 API 而失败，这是预期的）
	result, err := runner.RunGroup(context.Background(), "test-group")
	if err != nil {
		t.Logf("RunGroup() 在没有真实 API 时失败，符合预期: %v", err)
	}

	// 验证结果结构
	if result != nil {
		if result.GroupID != "test-group" {
			t.Errorf("GroupID = %v, want %v", result.GroupID, "test-group")
		}
		if result.TotalTests != 1 {
			t.Errorf("TotalTests = %v, want %v", result.TotalTests, 1)
		}
	}
}

func TestConfigValidation_TestGroups(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{
			name: "空配置有效",
			config: Config{
				TestGroups: TestGroupsConfig{
					Enabled: false,
					Groups:  map[string]TestGroup{},
				},
			},
			wantErr: false,
		},
		{
			name: "有效的测试分组",
			config: Config{
				TestGroups: TestGroupsConfig{
					Enabled: true,
					Groups: map[string]TestGroup{
						"test-1": {
							Name:        "测试 1",
							Description: "描述",
							Prompts: []TestPrompt{
								{
									ID:             "p1",
									Title:          "提示 1",
									Content:        "内容",
									ExpectedAnswer: "{}",
									MinScore:       70,
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "空分组 ID 无效",
			config: Config{
				TestGroups: TestGroupsConfig{
					Enabled: true,
					Groups: map[string]TestGroup{
						"": {
							Name: "测试",
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "空分组名称无效",
			config: Config{
				TestGroups: TestGroupsConfig{
					Enabled: true,
					Groups: map[string]TestGroup{
						"test": {
							Name: "  ",
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "空提示词 ID 无效",
			config: Config{
				TestGroups: TestGroupsConfig{
					Enabled: true,
					Groups: map[string]TestGroup{
						"test": {
							Name: "测试",
							Prompts: []TestPrompt{
								{
									ID:      "",
									Content: "内容",
								},
							},
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "空提示词内容无效",
			config: Config{
				TestGroups: TestGroupsConfig{
					Enabled: true,
					Groups: map[string]TestGroup{
						"test": {
							Name: "测试",
							Prompts: []TestPrompt{
								{
									ID:      "p1",
									Content: "  ",
								},
							},
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "MinScore 越界无效",
			config: Config{
				TestGroups: TestGroupsConfig{
					Enabled: true,
					Groups: map[string]TestGroup{
						"test": {
							Name: "测试",
							Prompts: []TestPrompt{
								{
									ID:       "p1",
									Content:  "内容",
									MinScore: 150,
								},
							},
						},
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 补齐必需的默认配置字段
			if tt.config.Accounts == nil {
				tt.config.Accounts = map[string]AccountRule{}
			}
			if tt.config.IQAPI.TimeoutSec == 0 {
				tt.config.IQAPI.TimeoutSec = 30
			}
			if tt.config.Queue.WorkerCount == 0 {
				tt.config.Queue.WorkerCount = 4
			}
			if tt.config.Queue.MaxSize == 0 {
				tt.config.Queue.MaxSize = 256
			}
			if tt.config.Capture.MaxRequestBodyBytes == 0 {
				tt.config.Capture.MaxRequestBodyBytes = 256 * 1024
			}
			if tt.config.Capture.MaxResponseBodyBytes == 0 {
				tt.config.Capture.MaxResponseBodyBytes = 256 * 1024
			}

			err := validate(&tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestTestGroupResult_JSONSerialization(t *testing.T) {
	result := TestGroupResult{
		GroupID:   "test-group",
		GroupName: "测试分组",
		Passed:    2,
		Failed:    1,
		Results: []TestPromptResult{
			{
				PromptID:       "p1",
				PromptTitle:    "提示 1",
				Answer:         `{"result":"ok"}`,
				ExpectedAnswer: `{"result":"ok"}`,
				Matched:        true,
				Score: &score{
					Overall:         75.5,
					ReasoningDepth:  80,
					LogicCoherence:  85,
					TaskCompletion:  70,
					JuiceEfficiency: 0.6,
				},
			},
		},
		Summary: TestGroupSummary{
			AvgOverall: 75.5,
			PassRate:   66.67,
		},
	}

	// 序列化
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	// 反序列化
	var decoded TestGroupResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	// 验证关键字段
	if decoded.GroupID != result.GroupID {
		t.Errorf("GroupID = %v, want %v", decoded.GroupID, result.GroupID)
	}
	if decoded.Passed != result.Passed {
		t.Errorf("Passed = %v, want %v", decoded.Passed, result.Passed)
	}
	if len(decoded.Results) != len(result.Results) {
		t.Errorf("Results length = %v, want %v", len(decoded.Results), len(result.Results))
	}
}
