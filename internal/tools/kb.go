package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/kb"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/toolctx"
	"github.com/chef-guo/agents-hive/internal/toolruntime"
)

// KBService is the narrow dependency required by the KB tool wrappers.
// Authorization facts are resolved from runtime context, never from model input.
type KBService interface {
	ResolveBinding(ctx context.Context, input kb.BindingResolveInput) ([]string, error)
	DocMeta(ctx context.Context, scope kb.Scope, input kb.DocMetaInput) (*kb.DocMetaResult, error)
	DocStructure(ctx context.Context, scope kb.Scope, input kb.DocStructureInput) (*kb.DocStructureResult, error)
	SectionText(ctx context.Context, scope kb.Scope, evidence kb.EvidenceScope, input kb.SectionTextInput) (*kb.SectionTextResult, error)
}

type KBRuntimeContext struct {
	DomainID          string
	OwnerScope        kb.OwnerScope
	OwnerID           string
	TenantID          string
	AgentID           string
	SessionTemplateID string
	SessionID         string
}

type kbRuntimeContextKey struct{}

// WithKBRuntimeContext injects server-derived KB routing facts for tool calls.
func WithKBRuntimeContext(ctx context.Context, runtime KBRuntimeContext) context.Context {
	return context.WithValue(ctx, kbRuntimeContextKey{}, runtime)
}

func KBRuntimeContextFromContext(ctx context.Context) (KBRuntimeContext, bool) {
	runtime, ok := ctx.Value(kbRuntimeContextKey{}).(KBRuntimeContext)
	return runtime, ok
}

type kbDocMetaToolInput struct {
	NamespaceID string `json:"namespace_id,omitempty"`
	Query       string `json:"query,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

type kbDocStructureToolInput struct {
	NamespaceID string `json:"namespace_id,omitempty"`
	DocumentID  string `json:"doc_id"`
}

type kbSectionTextToolInput struct {
	NamespaceID string   `json:"namespace_id,omitempty"`
	DocumentID  string   `json:"doc_id"`
	NodeIDs     []string `json:"node_ids"`
	PageRanges  []string `json:"page_ranges,omitempty"`
}

func registerKBTools(host *mcphost.Host, logger *zap.Logger, service KBService) {
	registerKBDocMeta(host, logger, service)
	registerKBDocStructure(host, logger, service)
	registerKBSectionText(host, logger, service)
}

func registerKBDocMeta(host *mcphost.Host, logger *zap.Logger, service KBService) {
	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"namespace_id": map[string]any{"type": "string", "description": "可选。仅用于收窄到当前会话已绑定的 namespace。"},
			"query":        map[string]any{"type": "string", "description": "可选。按标题或描述过滤文档。"},
			"limit":        map[string]any{"type": "integer", "description": "可选。最多返回文档数。"},
		},
	})
	host.RegisterTool(mcphost.ToolDefinition{
		Name:              "kb.doc.meta",
		Description:       "列出当前会话已授权知识库（KB）中的 active 文档元数据。用于回答用户关于项目知识库、业务知识库、上传文档、namespace 或文档目录的问题。namespace_id 只能收窄已绑定 namespace，不能授权新 namespace。",
		InputSchema:       schema,
		Core:              true,
		IsConcurrencySafe: true,
	}, func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
		var params kbDocMetaToolInput
		if err := json.Unmarshal(input, &params); err != nil {
			return kbRecoverableErrorResult("kb_invalid_input", "kb.doc.meta 输入无效: "+err.Error()), nil
		}
		scope, _, err := kbScopeFromContext(ctx, service, params.NamespaceID)
		if err != nil {
			return kbServiceErrorResult("kb.doc.meta", err), nil
		}
		result, err := service.DocMeta(ctx, scope, kb.DocMetaInput{
			NamespaceID: params.NamespaceID,
			Query:       params.Query,
			Limit:       params.Limit,
		})
		if err != nil {
			return kbServiceErrorResult("kb.doc.meta", err), nil
		}
		return kbJSONResult(result)
	})
	_ = logger
}

func registerKBDocStructure(host *mcphost.Host, logger *zap.Logger, service KBService) {
	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"namespace_id": map[string]any{"type": "string", "description": "可选。仅用于收窄到当前会话已绑定的 namespace。"},
			"doc_id":       map[string]any{"type": "string", "description": "kb.doc.meta 返回的文档 ID。"},
		},
		"required": []string{"doc_id"},
	})
	host.RegisterTool(mcphost.ToolDefinition{
		Name:              "kb.doc.structure",
		Description:       "读取当前会话已授权知识库（KB）文档的树结构，不返回节点原文。使用节点 ID 继续调用 kb.section.text 取证。",
		InputSchema:       schema,
		Core:              true,
		IsConcurrencySafe: true,
	}, func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
		var params kbDocStructureToolInput
		if err := json.Unmarshal(input, &params); err != nil {
			return kbRecoverableErrorResult("kb_invalid_input", "kb.doc.structure 输入无效: "+err.Error()), nil
		}
		if strings.TrimSpace(params.DocumentID) == "" {
			return kbRecoverableErrorResult("kb_invalid_input", "kb.doc.structure 需要 doc_id。"), nil
		}
		scope, _, err := kbScopeFromContext(ctx, service, params.NamespaceID)
		if err != nil {
			return kbServiceErrorResult("kb.doc.structure", err), nil
		}
		result, err := service.DocStructure(ctx, scope, kb.DocStructureInput{
			NamespaceID: params.NamespaceID,
			DocumentID:  params.DocumentID,
		})
		if err != nil {
			return kbServiceErrorResult("kb.doc.structure", err), nil
		}
		return kbJSONResult(result)
	})
	_ = logger
}

func registerKBSectionText(host *mcphost.Host, logger *zap.Logger, service KBService) {
	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"namespace_id": map[string]any{"type": "string", "description": "可选。仅用于收窄到当前会话已绑定的 namespace。"},
			"doc_id":       map[string]any{"type": "string", "description": "kb.doc.meta 返回的文档 ID。"},
			"node_ids": map[string]any{
				"type":        "array",
				"description": "kb.doc.structure 返回的节点 ID 列表。优先用于 Markdown/DOCX 或已判断出精确节点时。",
				"items":       map[string]any{"type": "string"},
			},
			"page_ranges": map[string]any{
				"type":        "array",
				"description": "可选。PDF 页级检索范围，例如 [\"5-7\", \"12\"]。当 kb.doc.structure 返回 start_page/end_page 时，可用 tight page ranges 取正文。",
				"items":       map[string]any{"type": "string"},
			},
		},
		"required": []string{"doc_id"},
	})
	host.RegisterTool(mcphost.ToolDefinition{
		Name:              "kb.section.text",
		Description:       "读取当前会话已授权知识库（KB）文档节点或 PDF 页范围原文，并返回服务端 evidence token 用于 citation。使用流程：先 kb.doc.meta，再 kb.doc.structure，根据标题/摘要/页锚选择少量 node_ids 或 tight page_ranges。",
		InputSchema:       schema,
		Core:              true,
		IsConcurrencySafe: true,
	}, func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
		var params kbSectionTextToolInput
		if err := json.Unmarshal(input, &params); err != nil {
			return kbRecoverableErrorResult("kb_invalid_input", "kb.section.text 输入无效: "+err.Error()), nil
		}
		if strings.TrimSpace(params.DocumentID) == "" || (len(params.NodeIDs) == 0 && len(params.PageRanges) == 0) {
			return kbRecoverableErrorResult("kb_invalid_input", "kb.section.text 需要 doc_id，并至少提供 node_ids 或 page_ranges。"), nil
		}
		if len(params.NodeIDs) > 0 && len(params.PageRanges) > 0 {
			return kbRecoverableErrorResult("kb_invalid_input", "kb.section.text 的 node_ids 和 page_ranges 不能同时提供；请选择一种精确取证方式。"), nil
		}
		scope, evidence, err := kbScopeFromContext(ctx, service, params.NamespaceID)
		if err != nil {
			return kbServiceErrorResult("kb.section.text", err), nil
		}
		result, err := service.SectionText(ctx, scope, evidence, kb.SectionTextInput{
			NamespaceID: params.NamespaceID,
			DocumentID:  params.DocumentID,
			NodeIDs:     params.NodeIDs,
			PageRanges:  params.PageRanges,
		})
		if err != nil {
			return kbServiceErrorResult("kb.section.text", err), nil
		}
		return kbJSONResult(result)
	})
	_ = logger
}

func kbScopeFromContext(ctx context.Context, service KBService, namespaceNarrowing string) (kb.Scope, kb.EvidenceScope, error) {
	if service == nil {
		return kb.Scope{}, kb.EvidenceScope{}, errors.New("kb service not configured")
	}
	now := time.Now()
	runtime := kbRuntimeFromContext(ctx)
	userID := auth.UserIDFrom(ctx)
	ownerScope := runtime.OwnerScope
	if ownerScope == "" {
		ownerScope = kb.OwnerScopeUser
	}
	ownerID := strings.TrimSpace(runtime.OwnerID)
	if ownerID == "" {
		ownerID = userID
	}
	sessionID := strings.TrimSpace(runtime.SessionID)
	if sessionID == "" {
		sessionID = toolctx.GetSessionID(ctx)
	}
	resolveInput := kb.BindingResolveInput{
		DomainID:          runtime.DomainID,
		OwnerScope:        ownerScope,
		OwnerID:           ownerID,
		UserID:            userID,
		TenantID:          runtime.TenantID,
		AgentID:           runtime.AgentID,
		SessionTemplateID: runtime.SessionTemplateID,
		SessionID:         sessionID,
		Now:               now,
	}
	namespaceIDs, err := service.ResolveBinding(ctx, resolveInput)
	if err != nil {
		return kb.Scope{}, kb.EvidenceScope{}, err
	}
	scope := kb.Scope{
		DomainID:           runtime.DomainID,
		OwnerScope:         ownerScope,
		OwnerID:            ownerID,
		NamespaceIDs:       namespaceIDs,
		NamespaceNarrowing: strings.TrimSpace(namespaceNarrowing),
		Now:                now,
	}
	tc := toolctx.GetToolContext(ctx)
	evidence := kb.EvidenceScope{
		SessionID:  sessionID,
		TurnID:     tc.TurnIDOrTraceID(),
		TraceID:    tc.TraceID,
		ToolCallID: tc.ToolCallID,
		DomainID:   scope.DomainID,
		OwnerScope: scope.OwnerScope,
		OwnerID:    scope.OwnerID,
		Now:        now,
	}
	return scope, evidence, nil
}

func kbRuntimeFromContext(ctx context.Context) KBRuntimeContext {
	runtime, _ := KBRuntimeContextFromContext(ctx)
	return runtime
}

func kbServiceErrorResult(toolName string, err error) *mcphost.ToolResult {
	if err == nil {
		return nil
	}
	if kb.IsRecoverable(err) || strings.Contains(err.Error(), "not configured") {
		return kbRecoverableErrorResult("kb_unavailable_or_not_bound", fmt.Sprintf("%s 未执行: %v", toolName, err))
	}
	return errorResult(fmt.Sprintf("%s 执行失败: %v", toolName, err))
}

func kbRecoverableErrorResult(kind, detail string) *mcphost.ToolResult {
	return errorResult(toolruntime.RecoverableToolCallErrorContent(kind, detail))
}

func kbJSONResult(value any) (*mcphost.ToolResult, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return errorResult("KB 工具结果序列化失败: " + err.Error()), nil
	}
	return textResult(string(data)), nil
}
