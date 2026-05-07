package mcphost

import (
	"encoding/json"
	"fmt"
	"strings"
)

// OutputSchemaDiagnostic 描述 output schema 校验告警。
// 当前实现只做轻量 JSON/required 顶层字段校验，用于记录诊断而不阻断结果。
type OutputSchemaDiagnostic struct {
	Keyword string `json:"keyword"`
	Message string `json:"message"`
}

// ValidateToolResult 校验工具结果是否满足定义中的 output schema。
// 返回诊断信息表示存在 mismatch；返回 error 仅表示 schema 自身无效，便于调用方区分配置问题。
func ValidateToolResult(def ToolDefinition, result *ToolResult) (*OutputSchemaDiagnostic, error) {
	if result == nil {
		return nil, nil
	}
	return ValidateToolOutput(def.OutputSchema, result.Content)
}

// ValidateToolOutput 对工具输出做最小 schema 校验。
// 当前只检查：
// 1. schema 是否为合法 JSON
// 2. content 是否为合法 JSON
// 3. schema.required 指定的顶层 key 是否存在
func ValidateToolOutput(schema json.RawMessage, content json.RawMessage) (*OutputSchemaDiagnostic, error) {
	if len(schema) == 0 {
		return nil, nil
	}

	var schemaDoc map[string]any
	if err := json.Unmarshal(schema, &schemaDoc); err != nil {
		return nil, fmt.Errorf("invalid output schema: %w", err)
	}

	var output any
	if err := json.Unmarshal(content, &output); err != nil {
		return &OutputSchemaDiagnostic{
			Keyword: "json_parse",
			Message: fmt.Sprintf("tool output is not valid json: %v", err),
		}, nil
	}

	required, ok, err := parseRequiredKeys(schemaDoc)
	if err != nil {
		return nil, err
	}
	if !ok || len(required) == 0 {
		return nil, nil
	}

	obj, ok := output.(map[string]any)
	if !ok {
		return &OutputSchemaDiagnostic{
			Keyword: "type",
			Message: "tool output must be a JSON object to satisfy required keys",
		}, nil
	}

	missing := make([]string, 0)
	for _, key := range required {
		if _, exists := obj[key]; !exists {
			missing = append(missing, key)
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}

	return &OutputSchemaDiagnostic{
		Keyword: "required",
		Message: fmt.Sprintf("tool output missing required keys: %s", strings.Join(missing, ", ")),
	}, nil
}

func parseRequiredKeys(schemaDoc map[string]any) ([]string, bool, error) {
	rawRequired, ok := schemaDoc["required"]
	if !ok {
		return nil, false, nil
	}

	items, ok := rawRequired.([]any)
	if !ok {
		return nil, false, fmt.Errorf("invalid output schema: required must be an array")
	}

	required := make([]string, 0, len(items))
	for _, item := range items {
		key, ok := item.(string)
		if !ok {
			return nil, false, fmt.Errorf("invalid output schema: required items must be strings")
		}
		required = append(required, key)
	}
	return required, true, nil
}
