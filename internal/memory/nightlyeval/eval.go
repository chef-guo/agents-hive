package nightlyeval

import (
	"context"
	"fmt"
	"math"
	"strings"
)

type Case struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Task                string   `json:"task"`
	Memory              string   `json:"memory,omitempty"`
	TaskTokenBudget     int      `json:"task_token_budget,omitempty"`
	ExpectedWithMemory  []string `json:"expected_with_memory,omitempty"`
	ExpectedWithoutMiss []string `json:"expected_without_miss,omitempty"`
	Required            bool     `json:"required"`
}

type Result struct {
	CaseID              string  `json:"case_id"`
	Name                string  `json:"name,omitempty"`
	WithMemoryPassed    bool    `json:"with_memory_passed"`
	WithoutMemoryPassed bool    `json:"without_memory_passed"`
	MemoryTokens        int     `json:"memory_tokens"`
	TaskTokens          int     `json:"task_tokens"`
	TokenROI            float64 `json:"token_roi"`
	Reason              string  `json:"reason,omitempty"`
}

type Summary struct {
	CaseCount                int      `json:"case_count"`
	RequiredCount            int      `json:"required_count"`
	WithMemorySuccessRate    float64  `json:"with_memory_success_rate"`
	WithoutMemorySuccessRate float64  `json:"without_memory_success_rate"`
	SuccessRateDelta         float64  `json:"success_rate_delta"`
	MemoryTokenROI           float64  `json:"memory_token_roi"`
	Passed                   bool     `json:"passed"`
	RequiredFailed           []string `json:"required_failed,omitempty"`
	Results                  []Result `json:"results"`
}

type Evaluator interface {
	Evaluate(ctx context.Context, c Case, withMemory bool) (bool, string, error)
}

type DeterministicEvaluator struct{}

func (e DeterministicEvaluator) Evaluate(ctx context.Context, c Case, withMemory bool) (bool, string, error) {
	if err := ctx.Err(); err != nil {
		return false, "", err
	}
	content := c.Task
	if withMemory {
		content += "\n" + c.Memory
		for _, token := range c.ExpectedWithMemory {
			if !strings.Contains(strings.ToLower(content), strings.ToLower(token)) {
				return false, "with-memory missing expected token: " + token, nil
			}
		}
		return true, "", nil
	}
	for _, token := range c.ExpectedWithoutMiss {
		if strings.Contains(strings.ToLower(content), strings.ToLower(token)) {
			return true, "", nil
		}
	}
	if len(c.ExpectedWithoutMiss) > 0 {
		return false, "without-memory lacks required task context", nil
	}
	return true, "", nil
}

func DefaultCases() []Case {
	return []Case{
		{
			ID:                  "mn01_tool_pattern",
			Name:                "工具调用模式注入提升任务成功率",
			Task:                "修复 Go 测试失败，并选择合适的验证命令。",
			Memory:              "历史经验：Go 测试在 sandbox 下需要设置 GOCACHE=/tmp/go-build 后再运行 go test。",
			TaskTokenBudget:     1200,
			ExpectedWithMemory:  []string{"GOCACHE=/tmp/go-build", "go test"},
			ExpectedWithoutMiss: []string{"GOCACHE=/tmp/go-build"},
			Required:            true,
		},
		{
			ID:                  "mn02_project_decision",
			Name:                "项目决策记忆减少错误实现",
			Task:                "给 memory 监控补面板，必须使用项目已有监控体系。",
			Memory:              "项目决策：Hive 使用自有 React Admin 监控面板，指标默认写入 hive_metrics，不引入 Grafana 强耦合。",
			TaskTokenBudget:     1400,
			ExpectedWithMemory:  []string{"React Admin", "hive_metrics"},
			ExpectedWithoutMiss: []string{"hive_metrics"},
			Required:            true,
		},
		{
			ID:                  "mn03_procedural_promotion",
			Name:                "procedural 记忆帮助复用流程",
			Task:                "执行计划时先核对 dirty worktree，再分阶段验证。",
			Memory:              "流程：先 git status --short 核对并行改动，再运行局部测试，最后运行 go test ./... 与前端 build。",
			TaskTokenBudget:     1600,
			ExpectedWithMemory:  []string{"git status --short", "go test ./..."},
			ExpectedWithoutMiss: []string{"git status --short"},
			Required:            true,
		},
	}
}

func Run(ctx context.Context, cases []Case, evaluator Evaluator) (Summary, error) {
	if evaluator == nil {
		evaluator = DeterministicEvaluator{}
	}
	if len(cases) == 0 {
		cases = DefaultCases()
	}
	summary := Summary{CaseCount: len(cases), Results: make([]Result, 0, len(cases)), Passed: true}
	var withPassed, withoutPassed int
	var totalMemoryTokens, totalTaskTokens int
	for _, c := range cases {
		if strings.TrimSpace(c.ID) == "" {
			return Summary{}, fmt.Errorf("nightly eval case id is required")
		}
		if c.Required {
			summary.RequiredCount++
		}
		withOK, withReason, err := evaluator.Evaluate(ctx, c, true)
		if err != nil {
			return Summary{}, err
		}
		withoutOK, withoutReason, err := evaluator.Evaluate(ctx, c, false)
		if err != nil {
			return Summary{}, err
		}
		memoryTokens := estimateTokens(c.Memory)
		taskTokens := c.TaskTokenBudget
		if taskTokens <= 0 {
			taskTokens = estimateTokens(c.Task) + memoryTokens
		}
		totalMemoryTokens += memoryTokens
		totalTaskTokens += taskTokens
		if withOK {
			withPassed++
		}
		if withoutOK {
			withoutPassed++
		}
		reason := strings.TrimSpace(withReason)
		if reason == "" {
			reason = strings.TrimSpace(withoutReason)
		}
		if c.Required && !withOK {
			summary.Passed = false
			summary.RequiredFailed = append(summary.RequiredFailed, c.ID)
		}
		summary.Results = append(summary.Results, Result{
			CaseID:              c.ID,
			Name:                c.Name,
			WithMemoryPassed:    withOK,
			WithoutMemoryPassed: withoutOK,
			MemoryTokens:        memoryTokens,
			TaskTokens:          taskTokens,
			TokenROI:            ratio(memoryTokens, taskTokens),
			Reason:              reason,
		})
	}
	summary.WithMemorySuccessRate = ratio(withPassed, len(cases))
	summary.WithoutMemorySuccessRate = ratio(withoutPassed, len(cases))
	summary.SuccessRateDelta = summary.WithMemorySuccessRate - summary.WithoutMemorySuccessRate
	summary.MemoryTokenROI = ratio(totalMemoryTokens, totalTaskTokens)
	if summary.MemoryTokenROI > 0.05 {
		summary.Passed = false
	}
	return summary, nil
}

func estimateTokens(text string) int {
	words := len(strings.Fields(text))
	runes := len([]rune(text))
	estimate := int(math.Ceil(float64(runes) / 4))
	if words > estimate {
		estimate = words
	}
	if estimate < 0 {
		return 0
	}
	return estimate
}

func ratio[T ~int | ~float64](n T, d T) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}
