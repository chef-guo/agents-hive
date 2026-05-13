package llm

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"

	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/mcphost"
)

func stableToolDefinitions(tools []mcphost.ToolDefinition) []mcphost.ToolDefinition {
	if len(tools) <= 1 {
		return tools
	}
	out := append([]mcphost.ToolDefinition(nil), tools...)
	sort.SliceStable(out, func(i, j int) bool {
		left := strings.TrimSpace(out[i].Name)
		right := strings.TrimSpace(out[j].Name)
		if left == right {
			return out[i].Description < out[j].Description
		}
		return left < right
	})
	return out
}

func convertToolsForChatCompletions(tools []mcphost.ToolDefinition) ([]openai.ChatCompletionToolParam, error) {
	result := make([]openai.ChatCompletionToolParam, 0, len(tools))
	for _, tool := range stableToolDefinitions(tools) {
		var inputSchema map[string]interface{}
		if err := json.Unmarshal(tool.InputSchema, &inputSchema); err != nil {
			return nil, errs.Wrap(errs.CodePlanGenFailed, fmt.Sprintf("解析工具输入 schema 失败 %s", tool.Name), err)
		}

		result = append(result, openai.ChatCompletionToolParam{
			Function: openai.FunctionDefinitionParam{
				Name:        tool.Name,
				Description: openai.String(tool.Description),
				Parameters:  openai.FunctionParameters(inputSchema),
			},
		})
	}
	return result, nil
}

// convertToolsForResponses 将 mcphost.ToolDefinition 列表转换为 Responses API 工具格式。
func convertToolsForResponses(tools []mcphost.ToolDefinition) ([]responses.ToolUnionParam, error) {
	result := make([]responses.ToolUnionParam, 0, len(tools))
	for _, tool := range stableToolDefinitions(tools) {
		var params map[string]any
		if tool.InputSchema != nil {
			if err := json.Unmarshal(tool.InputSchema, &params); err != nil {
				return nil, errs.Wrap(errs.CodePlanGenFailed, fmt.Sprintf("解析工具输入 schema 失败 %s", tool.Name), err)
			}
		}

		ft := &responses.FunctionToolParam{
			Name:       tool.Name,
			Parameters: params,
		}
		if tool.Description != "" {
			ft.Description = param.NewOpt(tool.Description)
		}

		result = append(result, responses.ToolUnionParam{
			OfFunction: ft,
		})
	}
	return result, nil
}
