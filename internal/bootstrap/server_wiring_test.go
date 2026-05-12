package bootstrap

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/imcore"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"go.uber.org/zap"
)

func TestRegisterIMAPIServiceRespectsEnabledFlag(t *testing.T) {
	host := mcphost.NewHost(zap.NewNop())
	cfg := config.Default()
	cfg.Agent.IMAPI.Enabled = false

	registerIMAPIService(&ServerComponents{MCPHost: host}, cfg, zap.NewNop(), imcore.NewService())

	if _, err := host.GetTool("im_api"); err == nil {
		t.Fatal("im_api should not be registered when agent.im_api.enabled=false")
	}
}

func TestRegisterIMAPIServiceRegistersToolWithDryRunOption(t *testing.T) {
	host := mcphost.NewHost(zap.NewNop())
	cfg := config.Default()
	cfg.Agent.IMAPI.Enabled = true
	cfg.Agent.IMAPI.ForceDryRun = true
	adapter := &bootstrapIMAdapter{platform: imcore.PlatformFeishu}

	registerIMAPIService(&ServerComponents{MCPHost: host}, cfg, zap.NewNop(), imcore.NewService(adapter))

	if _, err := host.GetTool("im_api"); err != nil {
		t.Fatalf("im_api should be registered: %v", err)
	}
	result, err := host.ExecuteTool(context.Background(), "im_api", json.RawMessage(`{"action":"send_message","platform":"feishu","recipient_id":"ou_1","content":"hi"}`))
	if err != nil {
		t.Fatalf("execute im_api: %v", err)
	}
	if result.IsError {
		t.Fatalf("im_api returned error: %s", result.DecodeContent())
	}
	if !adapter.lastTarget.DryRun {
		t.Fatal("ForceDryRun should force adapter target dry_run=true")
	}
}

type bootstrapIMAdapter struct {
	platform   imcore.Platform
	lastTarget imcore.SendTarget
}

func (a *bootstrapIMAdapter) Platform() imcore.Platform { return a.platform }

func (a *bootstrapIMAdapter) Capabilities() []imcore.Capability {
	return []imcore.Capability{imcore.CapabilitySendText}
}

func (a *bootstrapIMAdapter) SearchRecipients(context.Context, imcore.CallerScope, string, int) ([]imcore.Recipient, error) {
	return nil, nil
}

func (a *bootstrapIMAdapter) ListRecentConversations(context.Context, imcore.CallerScope, int) ([]imcore.Recipient, error) {
	return nil, nil
}

func (a *bootstrapIMAdapter) ResolveRecipient(_ context.Context, _ imcore.CallerScope, input imcore.RecipientLookup) (imcore.Recipient, error) {
	return imcore.Recipient{Platform: a.platform, ID: input.RecipientID, Kind: "user", CanSend: true, SendState: "ready"}, nil
}

func (a *bootstrapIMAdapter) SendMessage(_ context.Context, _ imcore.CallerScope, target imcore.SendTarget) (imcore.SendResult, error) {
	a.lastTarget = target
	return imcore.SendResult{Platform: a.platform, TargetID: target.RecipientID, TargetKind: "user", Delivered: !target.DryRun, DryRun: target.DryRun}, nil
}
