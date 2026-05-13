package master

import (
	"context"
	"encoding/json"
	"testing"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/security"
)

func TestActionGuardShellPolicy(t *testing.T) {
	guard := newDeterministicActionGuard()
	executor := security.NewSafeExecutor(nil, zap.NewNop())

	tests := []struct {
		name    string
		command string
		want    string
	}{
		{name: "rm root deny", command: "rm -rf /", want: ActionGuardDeny},
		{name: "force push ask", command: "git push --force origin main", want: ActionGuardAsk},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := guard.Decide(context.Background(), ActionGuardInput{
				ToolName:     "bash",
				Arguments:    mustRawMessage(t, map[string]string{"command": tt.command}),
				SafeExecutor: executor,
			})
			if decision.Action != tt.want {
				t.Fatalf("decision = %q, want %q, full=%+v", decision.Action, tt.want, decision)
			}
		})
	}
}

func TestActionGuardReadFileAllow(t *testing.T) {
	guard := newDeterministicActionGuard()

	decision := guard.Decide(context.Background(), ActionGuardInput{
		ToolName:  "read_file",
		Arguments: json.RawMessage(`{"path":"README.md"}`),
	})
	if decision.Action != ActionGuardAllow {
		t.Fatalf("decision = %q, want %q, full=%+v", decision.Action, ActionGuardAllow, decision)
	}
}

func TestActionGuardExternalSendAsks(t *testing.T) {
	guard := newDeterministicActionGuard()

	tests := []struct {
		name string
		tool string
		raw  string
	}{
		{name: "feishu send message", tool: "feishu_api", raw: `{"action":"send_message","content":"hi"}`},
		{name: "feishu send file", tool: "feishu_api", raw: `{"action":"send_file","file_key":"file"}`},
		{name: "feishu send image", tool: "feishu_api", raw: `{"action":"send_image","image_key":"img"}`},
		{name: "feishu upload file", tool: "feishu_api", raw: `{"action":"upload_file","data":"base64","filename":"a.txt"}`},
		{name: "feishu upload image", tool: "feishu_api", raw: `{"action":"upload_image","data":"base64"}`},
		{name: "send im message", tool: "send_im_message", raw: `{"platform":"feishu","content":"hi"}`},
		{name: "im api send", tool: "im_api", raw: `{"action":"send_message","content":"hi"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := guard.Decide(context.Background(), ActionGuardInput{
				ToolName:  tt.tool,
				Arguments: json.RawMessage(tt.raw),
			})
			if decision.Action != ActionGuardAsk {
				t.Fatalf("decision = %q, want %q, full=%+v", decision.Action, ActionGuardAsk, decision)
			}
		})
	}
}

func TestActionGuardUnknownToolDeny(t *testing.T) {
	guard := newDeterministicActionGuard()

	decision := guard.Decide(context.Background(), ActionGuardInput{
		ToolName:  "unknown_tool",
		Arguments: json.RawMessage(`{"anything":true}`),
	})
	if decision.Action != ActionGuardDeny {
		t.Fatalf("decision = %q, want %q, full=%+v", decision.Action, ActionGuardDeny, decision)
	}
}

func TestActionGuardConcurrencySafeCustomToolAllow(t *testing.T) {
	guard := newDeterministicActionGuard()
	def := mcphost.ToolDefinition{
		Name:              "project_status",
		Description:       "查询项目状态",
		IsConcurrencySafe: true,
	}

	decision := guard.Decide(context.Background(), ActionGuardInput{
		ToolName:  def.Name,
		Arguments: json.RawMessage(`{"project":"agents-hive"}`),
		ToolDef:   &def,
	})
	if decision.Action != ActionGuardAllow {
		t.Fatalf("decision = %q, want %q, full=%+v", decision.Action, ActionGuardAllow, decision)
	}
}

func TestActionGuardTrustedMCPReadOnlyAllow(t *testing.T) {
	guard := newDeterministicActionGuard()
	def := mcphost.ToolDefinition{
		Name:         "metamcp__query_prometheus",
		Description:  "Query Prometheus metrics",
		SourceServer: "metamcp",
		Trusted:      true,
	}

	decision := guard.Decide(context.Background(), ActionGuardInput{
		ToolName:  def.Name,
		Arguments: json.RawMessage(`{"query":"up"}`),
		ToolDef:   &def,
	})
	if decision.Action != ActionGuardAllow {
		t.Fatalf("decision = %q, want %q, full=%+v", decision.Action, ActionGuardAllow, decision)
	}
}

func TestActionGuardTrustedMCPSideEffectAsk(t *testing.T) {
	guard := newDeterministicActionGuard()
	def := mcphost.ToolDefinition{
		Name:         "metamcp__create_annotation",
		Description:  "Create Grafana annotation",
		SourceServer: "metamcp",
		Trusted:      true,
	}

	decision := guard.Decide(context.Background(), ActionGuardInput{
		ToolName:  def.Name,
		Arguments: json.RawMessage(`{"text":"deploy started"}`),
		ToolDef:   &def,
	})
	if decision.Action != ActionGuardAsk {
		t.Fatalf("decision = %q, want %q, full=%+v", decision.Action, ActionGuardAsk, decision)
	}
}

func TestActionGuardTrustedMCPSQLWriteAsk(t *testing.T) {
	guard := newDeterministicActionGuard()
	def := mcphost.ToolDefinition{
		Name:         "metamcp__dbhub__execute_sql",
		Description:  "Execute SQL against read-only database",
		SourceServer: "metamcp",
		Trusted:      true,
	}

	decision := guard.Decide(context.Background(), ActionGuardInput{
		ToolName:  def.Name,
		Arguments: json.RawMessage(`{"sql":"DROP TABLE users"}`),
		ToolDef:   &def,
	})
	if decision.Action != ActionGuardAsk {
		t.Fatalf("decision = %q, want %q, full=%+v", decision.Action, ActionGuardAsk, decision)
	}
}

func TestActionGuardTrustedMCPReadSQLAllow(t *testing.T) {
	guard := newDeterministicActionGuard()
	def := mcphost.ToolDefinition{
		Name:         "metamcp__dbhub__execute_sql",
		Description:  "Execute SQL against read-only database",
		SourceServer: "metamcp",
		Trusted:      true,
	}

	decision := guard.Decide(context.Background(), ActionGuardInput{
		ToolName:  def.Name,
		Arguments: json.RawMessage(`{"sql":"SELECT count(*) FROM users"}`),
		ToolDef:   &def,
	})
	if decision.Action != ActionGuardAllow {
		t.Fatalf("decision = %q, want %q, full=%+v", decision.Action, ActionGuardAllow, decision)
	}
}

func TestActionGuardTrustedMCPArgumentScannerIgnoresNonActionSubstrings(t *testing.T) {
	guard := newDeterministicActionGuard()
	def := mcphost.ToolDefinition{
		Name:         "metamcp__query_postgres_rows",
		Description:  "Query Postgres rows",
		SourceServer: "metamcp",
		Trusted:      true,
	}

	decision := guard.Decide(context.Background(), ActionGuardInput{
		ToolName:  def.Name,
		Arguments: json.RawMessage(`{"database":"postgres","column":"updated_at","query_name":"daily_user_count"}`),
		ToolDef:   &def,
	})
	if decision.Action != ActionGuardAllow {
		t.Fatalf("decision = %q, want %q, full=%+v", decision.Action, ActionGuardAllow, decision)
	}
}

func TestActionGuardTrustedMCPActionFieldAsksForDangerousOperation(t *testing.T) {
	guard := newDeterministicActionGuard()
	def := mcphost.ToolDefinition{
		Name:         "metamcp__query_prometheus",
		Description:  "Query Prometheus metrics",
		SourceServer: "metamcp",
		Trusted:      true,
	}

	decision := guard.Decide(context.Background(), ActionGuardInput{
		ToolName:  def.Name,
		Arguments: json.RawMessage(`{"action":"delete","target":"dashboard"}`),
		ToolDef:   &def,
	})
	if decision.Action != ActionGuardAsk {
		t.Fatalf("decision = %q, want %q, full=%+v", decision.Action, ActionGuardAsk, decision)
	}
}

func TestActionGuardTrustedMCPDestructiveDeny(t *testing.T) {
	guard := newDeterministicActionGuard()
	def := mcphost.ToolDefinition{
		Name:         "metamcp__delete_dashboard",
		Description:  "Delete Grafana dashboard",
		SourceServer: "metamcp",
		Trusted:      true,
	}

	decision := guard.Decide(context.Background(), ActionGuardInput{
		ToolName:  def.Name,
		Arguments: json.RawMessage(`{"uid":"abc"}`),
		ToolDef:   &def,
	})
	if decision.Action != ActionGuardDeny {
		t.Fatalf("decision = %q, want %q, full=%+v", decision.Action, ActionGuardDeny, decision)
	}
}

func TestActionGuardMemoryDeleteAsk(t *testing.T) {
	guard := newDeterministicActionGuard()

	decision := guard.Decide(context.Background(), ActionGuardInput{
		ToolName:  "memory",
		Arguments: json.RawMessage(`{"operation":"delete","id":1}`),
	})
	if decision.Action != ActionGuardAsk {
		t.Fatalf("decision = %q, want %q, full=%+v", decision.Action, ActionGuardAsk, decision)
	}
}

func mustRawMessage(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	return raw
}
