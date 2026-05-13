package llm

import (
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/responses"

	"github.com/chef-guo/agents-hive/internal/mcphost"
)

type responsesRequestOptions struct {
	Provider        string
	Model           string
	UserID          string
	PromptVersions  []string
	Tools           []mcphost.ToolDefinition
	CacheKeyEnabled bool
	ServiceTier     string
}

func applyResponsesRequestOptimizations(params *responses.ResponseNewParams, opts responsesRequestOptions) {
	if params == nil {
		return
	}
	if opts.CacheKeyEnabled && isProvider(opts.Provider, "openai") {
		if key := stablePromptCacheKey(opts.Model, opts.UserID, opts.PromptVersions, opts.Tools); key != "" {
			params.PromptCacheKey = openai.String(key)
		}
	}
	if tier := normalizeResponsesServiceTier(opts.ServiceTier); tier != "" {
		params.ServiceTier = tier
	}
}

func normalizeResponsesServiceTier(value string) responses.ResponseNewParamsServiceTier {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "auto":
		return responses.ResponseNewParamsServiceTierAuto
	case "default":
		return responses.ResponseNewParamsServiceTierDefault
	case "flex":
		return responses.ResponseNewParamsServiceTierFlex
	case "scale":
		return responses.ResponseNewParamsServiceTierScale
	case "priority":
		return responses.ResponseNewParamsServiceTierPriority
	default:
		return ""
	}
}
