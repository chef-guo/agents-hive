package router

import (
	"encoding/json"
	"testing"

	"github.com/chef-guo/agents-hive/internal/mcphost"
)

func TestEvaluateToolPolicyUnifiedMatrix(t *testing.T) {
	tests := []struct {
		name         string
		def          mcphost.ToolDefinition
		intent       IntentFrame
		input        json.RawMessage
		wantAction   ToolPolicyAction
		wantRoute    ToolRouteStatus
		wantCallable bool
		wantApproval bool
	}{
		{
			name:         "builtin read only allow",
			def:          mcphost.ToolDefinition{Name: "read_file", Core: true},
			intent:       IntentFrame{Kind: IntentRead},
			wantAction:   ToolPolicyAllow,
			wantRoute:    ToolRouteCallableReadOnly,
			wantCallable: true,
		},
		{
			name:         "concurrency safe custom allow",
			def:          mcphost.ToolDefinition{Name: "project_status", Description: "查询项目状态", IsConcurrencySafe: true},
			intent:       IntentFrame{Kind: IntentAnswer},
			wantAction:   ToolPolicyAllow,
			wantRoute:    ToolRouteCallableReadOnly,
			wantCallable: true,
		},
		{
			name:         "unknown custom deny",
			def:          mcphost.ToolDefinition{Name: "opaque_candidate", Description: "opaque extension"},
			intent:       IntentFrame{Kind: IntentAnswer},
			wantAction:   ToolPolicyDeny,
			wantRoute:    ToolRouteBlockedUnknown,
			wantApproval: true,
		},
		{
			name:         "trusted remote read allow",
			def:          mcphost.ToolDefinition{Name: "metamcp__query_prometheus", Description: "Query Prometheus metrics", SourceServer: "metamcp", Trusted: true},
			intent:       IntentFrame{Kind: IntentExternalRead, RequiresExternal: true},
			wantAction:   ToolPolicyAllow,
			wantRoute:    ToolRouteCallableReadOnly,
			wantCallable: true,
		},
		{
			name:         "trusted remote side effect requires write intent",
			def:          mcphost.ToolDefinition{Name: "metamcp__create_annotation", Description: "Create Grafana annotation", SourceServer: "metamcp", Trusted: true},
			intent:       IntentFrame{Kind: IntentExternalRead, RequiresExternal: true},
			wantAction:   ToolPolicyAsk,
			wantRoute:    ToolRouteRequiresSideEffectIntent,
			wantApproval: true,
		},
		{
			name:         "trusted remote side effect asks under write intent",
			def:          mcphost.ToolDefinition{Name: "metamcp__create_annotation", Description: "Create Grafana annotation", SourceServer: "metamcp", Trusted: true},
			intent:       IntentFrame{Kind: IntentExternalWrite, RequiresExternal: true, AllowsSideEffects: true},
			wantAction:   ToolPolicyAsk,
			wantRoute:    ToolRouteRequiresSideEffectIntent,
			wantCallable: true,
			wantApproval: true,
		},
		{
			name:         "trusted remote destructive deny",
			def:          mcphost.ToolDefinition{Name: "metamcp__delete_dashboard", Description: "Delete Grafana dashboard", SourceServer: "metamcp", Trusted: true},
			intent:       IntentFrame{Kind: IntentExternalWrite, RequiresExternal: true, AllowsSideEffects: true},
			wantAction:   ToolPolicyDeny,
			wantRoute:    ToolRouteBlockedDangerous,
			wantApproval: true,
		},
		{
			name:         "untrusted remote deny",
			def:          mcphost.ToolDefinition{Name: "github__create_issue", Description: "Create GitHub issue"},
			intent:       IntentFrame{Kind: IntentExternalWrite, RequiresExternal: true, AllowsSideEffects: true},
			wantAction:   ToolPolicyDeny,
			wantRoute:    ToolRouteBlockedDangerous,
			wantApproval: true,
		},
		{
			name:         "mixed read action allow with constraints",
			def:          mcphost.ToolDefinition{Name: "memory", Core: true},
			intent:       IntentFrame{Kind: IntentRead},
			input:        json.RawMessage(`{"operation":"search","query":"tool policy"}`),
			wantAction:   ToolPolicyAllow,
			wantRoute:    ToolRouteCallableWithActionConstraints,
			wantCallable: true,
		},
		{
			name:         "mixed dangerous action asks",
			def:          mcphost.ToolDefinition{Name: "memory", Core: true},
			intent:       IntentFrame{Kind: IntentWriteLocal, AllowsSideEffects: true},
			input:        json.RawMessage(`{"operation":"delete","id":"m1"}`),
			wantAction:   ToolPolicyAsk,
			wantRoute:    ToolRouteRequiresSideEffectIntent,
			wantCallable: true,
			wantApproval: true,
		},
		{
			name:         "runtime exec read intent blocked",
			def:          mcphost.ToolDefinition{Name: "bash", Core: true},
			intent:       IntentFrame{Kind: IntentRead},
			input:        json.RawMessage(`{"command":"pwd"}`),
			wantAction:   ToolPolicyDeny,
			wantRoute:    ToolRouteBlockedDangerous,
			wantApproval: true,
		},
		{
			name:         "runtime exec manage intent still requires safe executor approval path",
			def:          mcphost.ToolDefinition{Name: "bash", Core: true},
			intent:       IntentFrame{Kind: IntentManageTool, AllowsSideEffects: true},
			input:        json.RawMessage(`{"command":"pwd"}`),
			wantAction:   ToolPolicyAsk,
			wantRoute:    ToolRouteRequiresMatchingIntent,
			wantCallable: true,
			wantApproval: true,
		},
		{
			name:         "mixed external send action requires approval",
			def:          mcphost.ToolDefinition{Name: "feishu_api", Core: true},
			intent:       IntentFrame{Kind: IntentExternalWrite, RequiresExternal: true, AllowsSideEffects: true},
			input:        json.RawMessage(`{"action":"upload_file","data":"base64","filename":"a.txt"}`),
			wantAction:   ToolPolicyAsk,
			wantRoute:    ToolRouteRequiresSideEffectIntent,
			wantCallable: true,
			wantApproval: true,
		},
		{
			name:         "mixed external send action blocked for read intent",
			def:          mcphost.ToolDefinition{Name: "feishu_api", Core: true},
			intent:       IntentFrame{Kind: IntentRead},
			input:        json.RawMessage(`{"action":"send_message","chat_id":"oc_1","content":"hi"}`),
			wantAction:   ToolPolicyAsk,
			wantRoute:    ToolRouteRequiresSideEffectIntent,
			wantApproval: true,
		},
		{
			name:         "mixed unknown action blocked",
			def:          mcphost.ToolDefinition{Name: "memory", Core: true},
			intent:       IntentFrame{Kind: IntentWriteLocal, AllowsSideEffects: true},
			input:        json.RawMessage(`{"operation":"saev","content":"typo"}`),
			wantAction:   ToolPolicyDeny,
			wantRoute:    ToolRouteBlockedUnknown,
			wantApproval: true,
		},
		{
			name:         "unclassified deny still requires attention",
			def:          mcphost.ToolDefinition{Name: "question", Core: true},
			intent:       IntentFrame{Kind: IntentAnswer},
			input:        json.RawMessage(`{}`),
			wantAction:   ToolPolicyDeny,
			wantRoute:    ToolRouteBlockedUnknown,
			wantApproval: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := InferToolProfile(tt.def, ProfileHint{})
			if tt.name == "unclassified deny still requires attention" {
				profile.ReadOnly = false
				profile.Risk = RiskReadOnly
			}
			got := EvaluateToolPolicy(profile, ToolPolicyContext{
				Intent:    tt.intent,
				Input:     tt.input,
				ForRoute:  true,
				ForAction: true,
			})
			if got.Action != tt.wantAction {
				t.Fatalf("Action = %q, want %q, full=%+v profile=%+v", got.Action, tt.wantAction, got, profile)
			}
			if got.RouteStatus != tt.wantRoute {
				t.Fatalf("RouteStatus = %q, want %q, full=%+v profile=%+v", got.RouteStatus, tt.wantRoute, got, profile)
			}
			if got.CallableNow != tt.wantCallable {
				t.Fatalf("CallableNow = %v, want %v, full=%+v profile=%+v", got.CallableNow, tt.wantCallable, got, profile)
			}
			if got.RequiresApproval != tt.wantApproval {
				t.Fatalf("RequiresApproval = %v, want %v, full=%+v profile=%+v", got.RequiresApproval, tt.wantApproval, got, profile)
			}
		})
	}
}
