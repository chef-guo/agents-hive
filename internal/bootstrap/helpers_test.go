package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apiPkg "github.com/chef-guo/agents-hive/internal/api"
	"github.com/chef-guo/agents-hive/internal/channel"
	"github.com/chef-guo/agents-hive/internal/channel/feishu"
	"github.com/chef-guo/agents-hive/internal/channel/wechatbot"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/master"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/subagent"
	"go.uber.org/zap"
)

type pushReloadableStub struct {
	calls   int
	lastCfg config.FeishuConfig
}

func (p *pushReloadableStub) ReloadFromConfig(cfg config.FeishuConfig) error {
	p.calls++
	p.lastCfg = cfg
	return nil
}

type fakeSubmitter struct {
	calls []master.InputResponse
}

type fakeConfigStore struct {
	store.Store
	cfg map[string]string
}

func (f fakeConfigStore) GetAllConfig(context.Context) (map[string]string, error) {
	return f.cfg, nil
}

func (f *fakeSubmitter) SubmitInput(resp master.InputResponse) error {
	f.calls = append(f.calls, resp)
	return nil
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	return b
}

func TestBuildLLMExtraConfig_Empty(t *testing.T) {
	cfg := &config.Config{}
	extra := BuildLLMExtraConfig(cfg)
	if extra == nil {
		t.Fatal("expected non-nil map")
	}
	if len(extra) != 0 {
		t.Errorf("expected empty map for zero-value config, got %d entries: %v", len(extra), extra)
	}
}

func TestLoadAllConfigFromDB_PlanRuntimeEnabledDefaultsTrueAndCanBeDisabled(t *testing.T) {
	cfg := config.Default()
	if !cfg.Agent.PlanRuntime.Enabled {
		t.Fatal("test precondition: Plan Runtime should default enabled")
	}

	LoadAllConfigFromDB(fakeConfigStore{cfg: map[string]string{
		"agent.plan_runtime.enabled": "false",
	}}, cfg, zap.NewNop())

	if cfg.Agent.PlanRuntime.Enabled {
		t.Fatal("Agent.PlanRuntime.Enabled = true, want DB config false to disable it")
	}
}

func TestLoadAllConfigFromDB_FirstTokenDefaultsAndCanBeDisabled(t *testing.T) {
	cfg := config.Default()
	if !cfg.Agent.FirstToken.FastPathEnabled {
		t.Fatal("test precondition: first-token fast path should default enabled")
	}
	if cfg.Agent.FirstToken.PreflightClassifierTimeout != 300*time.Millisecond {
		t.Fatalf("test precondition: timeout = %v, want 300ms", cfg.Agent.FirstToken.PreflightClassifierTimeout)
	}
	if cfg.Agent.MaxModelVisibleTools != 8 {
		t.Fatalf("test precondition: max_model_visible_tools = %d, want 8", cfg.Agent.MaxModelVisibleTools)
	}

	LoadAllConfigFromDB(fakeConfigStore{cfg: map[string]string{
		"agent.timeout": "10m",
	}}, cfg, zap.NewNop())

	if !cfg.Agent.FirstToken.FastPathEnabled {
		t.Fatal("Agent.FirstToken.FastPathEnabled = false, want missing DB key to preserve default true")
	}
	if cfg.Agent.FirstToken.PreflightClassifierTimeout != 300*time.Millisecond {
		t.Fatalf("Agent.FirstToken.PreflightClassifierTimeout = %v, want missing DB key to preserve 300ms", cfg.Agent.FirstToken.PreflightClassifierTimeout)
	}
	if cfg.Agent.MaxModelVisibleTools != 8 {
		t.Fatalf("Agent.MaxModelVisibleTools = %d, want missing DB key to preserve 8", cfg.Agent.MaxModelVisibleTools)
	}

	LoadAllConfigFromDB(fakeConfigStore{cfg: map[string]string{
		"agent.first_token.fast_path_enabled":            "false",
		"agent.first_token.preflight_classifier_timeout": "75ms",
		"agent.max_model_visible_tools":                  "0",
	}}, cfg, zap.NewNop())

	if cfg.Agent.FirstToken.FastPathEnabled {
		t.Fatal("Agent.FirstToken.FastPathEnabled = true, want DB config false to disable it")
	}
	if cfg.Agent.FirstToken.PreflightClassifierTimeout != 75*time.Millisecond {
		t.Fatalf("Agent.FirstToken.PreflightClassifierTimeout = %v, want DB config 75ms", cfg.Agent.FirstToken.PreflightClassifierTimeout)
	}
	if cfg.Agent.MaxModelVisibleTools != 0 {
		t.Fatalf("Agent.MaxModelVisibleTools = %d, want DB rollback config 0", cfg.Agent.MaxModelVisibleTools)
	}
}

func TestLoadAllConfigFromDB_ActionGuardDefaultsTrueAndCanBeDisabled(t *testing.T) {
	cfg := config.Default()
	if !cfg.Agent.ActionGuardEnabled {
		t.Fatal("test precondition: ActionGuard should default enabled")
	}

	LoadAllConfigFromDB(fakeConfigStore{cfg: map[string]string{
		"agent.timeout": "10m",
	}}, cfg, zap.NewNop())

	if !cfg.Agent.ActionGuardEnabled {
		t.Fatal("Agent.ActionGuardEnabled = false, want missing DB key to preserve default true")
	}

	LoadAllConfigFromDB(fakeConfigStore{cfg: map[string]string{
		"agent.action_guard_enabled": "false",
	}}, cfg, zap.NewNop())

	if cfg.Agent.ActionGuardEnabled {
		t.Fatal("Agent.ActionGuardEnabled = true, want DB config false to disable it")
	}
}

type channelConfigMemoryStore struct {
	store.Store
	records []*store.ChannelConfigRecord
}

func (s *channelConfigMemoryStore) ListChannelConfigs(context.Context) ([]*store.ChannelConfigRecord, error) {
	out := make([]*store.ChannelConfigRecord, len(s.records))
	for i, rec := range s.records {
		cp := *rec
		out[i] = &cp
	}
	return out, nil
}

func (s *channelConfigMemoryStore) ListMCPServers(context.Context) ([]*store.MCPServerRecord, error) {
	return nil, nil
}

func (s *channelConfigMemoryStore) SaveChannelConfig(_ context.Context, rec *store.ChannelConfigRecord) error {
	cp := *rec
	s.records = append(s.records, &cp)
	return nil
}

func TestMigrateConfigToDB_IncludesWechatbotOfficialFlag(t *testing.T) {
	cfg := config.Default()
	cfg.Channel.WeChatBot.Enabled = true
	db := &channelConfigMemoryStore{}

	MigrateConfigToDB(db, cfg, zap.NewNop())

	var found *store.ChannelConfigRecord
	for _, rec := range db.records {
		if rec.Platform == "wechatbot" {
			found = rec
			break
		}
	}
	if found == nil {
		t.Fatal("expected wechatbot channel config to be migrated")
	}
	if !found.Enabled {
		t.Fatal("wechatbot channel config Enabled = false, want true")
	}
	var decoded config.WeChatBotConfig
	if err := json.Unmarshal([]byte(found.ConfigJSON), &decoded); err != nil {
		t.Fatalf("wechatbot config JSON invalid: %v", err)
	}
	if !decoded.Enabled {
		t.Fatal("decoded wechatbot config Enabled = false, want true")
	}
}

func TestLoadChannelConfigsFromDB_LoadsWechatbotOfficialFlag(t *testing.T) {
	cfg := config.Default()
	db := &channelConfigMemoryStore{
		records: []*store.ChannelConfigRecord{{
			Platform:   "wechatbot",
			Enabled:    true,
			ConfigJSON: `{"enabled":true}`,
		}},
	}

	LoadChannelConfigsFromDB(db, cfg, zap.NewNop())

	if !cfg.Channel.WeChatBot.Enabled {
		t.Fatal("Channel.WeChatBot.Enabled = false, want true")
	}
}

func TestBuildLLMExtraConfig_AllFields(t *testing.T) {
	cfg := &config.Config{
		LLM: config.LLMConfig{
			GoogleAPIKey:           "gkey",
			AzureAPIKey:            "akey",
			AzureDeployment:        "deploy1",
			AzureEndpoint:          "https://azure.example.com",
			ReasoningEffort:        "high",
			DisableJSONMode:        true,
			StorePrivacy:           true,
			PromptCacheKeyEnabled:  true,
			InteractiveServiceTier: "priority",
			ModelRegistryURL:       "https://registry.example.com",
		},
	}

	extra := BuildLLMExtraConfig(cfg)

	expected := map[string]any{
		"google_api_key":           "gkey",
		"azure_api_key":            "akey",
		"azure_deployment":         "deploy1",
		"azure_endpoint":           "https://azure.example.com",
		"reasoning_effort":         "high",
		"disable_json_mode":        true,
		"store_privacy":            true,
		"prompt_cache_key_enabled": true,
		"interactive_service_tier": "priority",
		"model_registry_url":       "https://registry.example.com",
	}

	if len(extra) != len(expected) {
		t.Errorf("expected %d entries, got %d: %v", len(expected), len(extra), extra)
	}

	for k, want := range expected {
		got, ok := extra[k]
		if !ok {
			t.Errorf("missing key %q", k)
			continue
		}
		if got != want {
			t.Errorf("key %q: got %v, want %v", k, got, want)
		}
	}
}

func TestBuildLLMExtraConfig_PartialFields(t *testing.T) {
	tests := []struct {
		name     string
		cfg      config.LLMConfig
		wantKeys []string
		noKeys   []string
	}{
		{
			name:     "only Google",
			cfg:      config.LLMConfig{GoogleAPIKey: "gkey"},
			wantKeys: []string{"google_api_key"},
			noKeys:   []string{"azure_api_key", "reasoning_effort"},
		},
		{
			name: "only Azure",
			cfg: config.LLMConfig{
				AzureAPIKey:     "akey",
				AzureDeployment: "deploy",
				AzureEndpoint:   "https://ep",
			},
			wantKeys: []string{"azure_api_key", "azure_deployment", "azure_endpoint"},
			noKeys:   []string{"google_api_key"},
		},
		{
			name:     "only boolean flags",
			cfg:      config.LLMConfig{DisableJSONMode: true, StorePrivacy: true},
			wantKeys: []string{"disable_json_mode", "store_privacy"},
			noKeys:   []string{"google_api_key", "reasoning_effort"},
		},
		{
			name:     "DisableJSONMode false omitted",
			cfg:      config.LLMConfig{DisableJSONMode: false},
			wantKeys: nil,
			noKeys:   []string{"disable_json_mode"},
		},
		{
			name:     "StorePrivacy false omitted",
			cfg:      config.LLMConfig{StorePrivacy: false},
			wantKeys: nil,
			noKeys:   []string{"store_privacy"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{LLM: tt.cfg}
			extra := BuildLLMExtraConfig(cfg)

			for _, k := range tt.wantKeys {
				if _, ok := extra[k]; !ok {
					t.Errorf("expected key %q to be present", k)
				}
			}
			for _, k := range tt.noKeys {
				if _, ok := extra[k]; ok {
					t.Errorf("expected key %q to be absent", k)
				}
			}
		})
	}
}

func TestBuildLLMExtraConfig_ValuesAreCorrectTypes(t *testing.T) {
	cfg := &config.Config{
		LLM: config.LLMConfig{
			GoogleAPIKey:    "gkey",
			DisableJSONMode: true,
			StorePrivacy:    true,
		},
	}
	extra := BuildLLMExtraConfig(cfg)

	// String fields should be strings
	if v, ok := extra["google_api_key"].(string); !ok || v != "gkey" {
		t.Errorf("google_api_key: expected string \"gkey\", got %T %v", extra["google_api_key"], extra["google_api_key"])
	}

	// Boolean fields should be bools
	if v, ok := extra["disable_json_mode"].(bool); !ok || !v {
		t.Errorf("disable_json_mode: expected bool true, got %T %v", extra["disable_json_mode"], extra["disable_json_mode"])
	}
	if v, ok := extra["store_privacy"].(bool); !ok || !v {
		t.Errorf("store_privacy: expected bool true, got %T %v", extra["store_privacy"], extra["store_privacy"])
	}
}

func TestBuildReloadChannelFunc_NilRouter(t *testing.T) {
	fn := BuildReloadChannelFunc(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	if fn != nil {
		t.Error("expected nil func when router is nil")
	}
}

func TestBuildReloadChannelFunc_FeishuAppliesReloadables(t *testing.T) {
	logger := zap.NewNop()
	router := channel.NewRouter(nil, logger)
	cfg := &config.Config{
		Channel: config.ChannelConfig{
			Feishu: config.FeishuConfig{
				Enabled: true,
				Push: config.FeishuPushConfig{
					Enabled:           true,
					PerChatPerMinute:  7,
					IdempotencyTTLSec: 33,
				},
			},
		},
	}
	var mu sync.RWMutex
	pushService := pushReloadableStub{}

	fn := BuildReloadChannelFunc(cfg, router, nil, nil, nil, nil, nil, nil, nil, nil, &mu, logger, &pushService)
	if fn == nil {
		t.Fatal("expected non-nil reload func")
	}
	if err := fn("feishu"); err != nil {
		t.Fatalf("reload feishu failed: %v", err)
	}
	if pushService.calls != 1 {
		t.Fatalf("reloadable calls = %d, want 1", pushService.calls)
	}
	if pushService.lastCfg.Push.PerChatPerMinute != 7 {
		t.Fatalf("reloadable cfg push.per_chat_per_minute = %d, want 7", pushService.lastCfg.Push.PerChatPerMinute)
	}
}

func TestBuildReloadChannelFunc_WechatbotTogglesExistingRegistry(t *testing.T) {
	logger := zap.NewNop()
	router := channel.NewRouter(nil, logger)
	cfg := config.Default()
	cfg.SessionsDir = t.TempDir()
	cfg.Channel.WeChatBot.Enabled = false
	var mu sync.RWMutex
	st := store.NewMemoryStore()

	fn := BuildReloadChannelFuncWithStore(cfg, router, st, nil, nil, nil, nil, nil, nil, nil, nil, &mu, logger)
	if fn == nil {
		t.Fatal("expected non-nil reload func")
	}
	if err := fn("wechatbot"); err != nil {
		t.Fatalf("reload disabled wechatbot failed: %v", err)
	}
	plugin, ok := router.GetPlugin(channel.PlatformWeChatBot)
	if !ok {
		t.Fatal("expected wechatbot plugin to stay registered for API service")
	}
	wbPlugin, ok := plugin.(*wechatbot.Plugin)
	if !ok {
		t.Fatalf("plugin type = %T, want *wechatbot.Plugin", plugin)
	}
	status, err := wbPlugin.Registry().Status(context.Background(), "owner-1")
	if err != nil {
		t.Fatalf("status disabled: %v", err)
	}
	if status.Enabled {
		t.Fatalf("status.Enabled = true, want false")
	}

	cfg.Channel.WeChatBot.Enabled = true
	if err := fn("wechatbot"); err != nil {
		t.Fatalf("reload enabled wechatbot failed: %v", err)
	}
	status, err = wbPlugin.Registry().Status(context.Background(), "owner-1")
	if err != nil {
		t.Fatalf("status enabled: %v", err)
	}
	if !status.Enabled {
		t.Fatalf("status.Enabled = false, want true")
	}
}

func TestBuildFeishuPlugin_AllowsLongconnIngressMode(t *testing.T) {
	logger := zap.NewNop()
	router := channel.NewRouter(nil, logger)

	plugin, err := buildFeishuPlugin(config.FeishuConfig{
		Enabled:     true,
		IngressMode: config.FeishuIngressModeLongconn,
	}, router, nil, nil, nil, logger)
	if err != nil {
		t.Fatalf("expected longconn ingress mode to build, got error: %v", err)
	}
	if plugin == nil {
		t.Fatal("expected non-nil plugin")
	}
}

func TestRestoreFeishuPushSchedules_LoadsEnabledJobsIntoMaster(t *testing.T) {
	appStore := store.NewMemoryStore()
	if err := appStore.SaveScheduledPush(context.Background(), &store.ScheduledPushRecord{
		ID:          "sched-1",
		Name:        "daily-report",
		Platform:    "feishu",
		Prompt:      "scheduled_push:task_done:chat_id=oc_sched_1:title=日报生成完成:summary=请查收",
		IntervalSec: 1,
		Enabled:     true,
	}); err != nil {
		t.Fatalf("SaveScheduledPush() error = %v", err)
	}

	m := &master.Master{}
	var fired int32
	m.SetScheduledPromptDispatcher(func(_ context.Context, prompt string) error {
		if prompt == "scheduled_push:task_done:chat_id=oc_sched_1:title=日报生成完成:summary=请查收" {
			atomic.AddInt32(&fired, 1)
		}
		return nil
	})

	if err := restoreFeishuPushSchedules(context.Background(), m, appStore, func(_ context.Context, prompt string) error {
		if prompt == "scheduled_push:task_done:chat_id=oc_sched_1:title=日报生成完成:summary=请查收" {
			atomic.AddInt32(&fired, 1)
		}
		return nil
	}, zap.NewNop()); err != nil {
		t.Fatalf("restoreFeishuPushSchedules() error = %v", err)
	}
	defer m.StopCron("scheduled-push:sched-1")
	time.Sleep(1200 * time.Millisecond)
	if atomic.LoadInt32(&fired) == 0 {
		t.Fatal("expected restored schedule to fire at least once")
	}
	got, err := appStore.GetScheduledPush(context.Background(), "sched-1")
	if err != nil {
		t.Fatalf("GetScheduledPush() error = %v", err)
	}
	if got.LastRunAt.IsZero() {
		t.Fatal("expected restored schedule to persist last_run_at")
	}
	if got.NextRunAt.IsZero() {
		t.Fatal("expected restored schedule to persist next_run_at")
	}
}

func TestValidateScheduledTaskReloadsDisablesOnlyBadTask(t *testing.T) {
	ctx := context.Background()
	appStore := store.NewMemoryStore()
	if err := appStore.SaveScheduledTask(ctx, &store.ScheduledTask{
		ID:         "task-good-reload",
		Name:       "good",
		TargetType: "session",
		Prompt:     "run",
		CronExpr:   "0 9 * * *",
		Timezone:   "Asia/Shanghai",
		Enabled:    true,
		CreatedBy:  "u1",
	}); err != nil {
		t.Fatalf("SaveScheduledTask good: %v", err)
	}
	if err := appStore.SaveScheduledTask(ctx, &store.ScheduledTask{
		ID:         "task-bad-reload",
		Name:       "bad",
		TargetType: "session",
		Prompt:     "run",
		CronExpr:   "bad cron",
		Timezone:   "UTC",
		Enabled:    true,
		CreatedBy:  "u1",
	}); err != nil {
		t.Fatalf("SaveScheduledTask bad: %v", err)
	}
	m := master.NewForRegressionTest(zap.NewNop(), nil)
	validateScheduledTaskReloads(ctx, m, appStore, zap.NewNop())

	bad, err := appStore.GetScheduledTask(ctx, "task-bad-reload")
	if err != nil {
		t.Fatalf("GetScheduledTask bad: %v", err)
	}
	if bad.Enabled || bad.LastError == "" {
		t.Fatalf("bad task should be disabled with last_error: %+v", bad)
	}
	good, err := appStore.GetScheduledTask(ctx, "task-good-reload")
	if err != nil {
		t.Fatalf("GetScheduledTask good: %v", err)
	}
	if !good.Enabled || good.LastError != "" {
		t.Fatalf("good task should remain enabled: %+v", good)
	}
}

func TestBuildFeishuPlugin_WiresHITLBridge(t *testing.T) {
	logger := zap.NewNop()
	router := channel.NewRouter(nil, logger)
	submitter := &fakeSubmitter{}

	plugin, err := buildFeishuPlugin(config.FeishuConfig{}, router, submitter, nil, nil, logger)
	if err != nil {
		t.Fatalf("buildFeishuPlugin returned error: %v", err)
	}
	if plugin == nil {
		t.Fatal("expected non-nil plugin")
	}

	handler := plugin.WebhookHandler()
	if handler == nil {
		t.Fatal("expected webhook handler to be initialized")
	}

	body := mustJSON(t, map[string]any{
		"schema": "2.0",
		"header": map[string]any{
			"event_id":    "evt-card",
			"token":       "",
			"create_time": "1700000000",
			"event_type":  "card.action.trigger",
			"tenant_key":  "tk-test",
		},
		"event": map[string]any{
			"operator": map[string]any{
				"open_id":    "ou_clicker",
				"tenant_key": "tk-test",
			},
			"action": map[string]any{
				"tag": "button",
				"value": map[string]any{
					"request_id": "req-via-bootstrap",
					"action":     "approve",
					"task_id":    "t-1",
				},
			},
			"context": map[string]any{
				"open_message_id": "om_card_1",
			},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("card action must return 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(submitter.calls) != 1 {
		t.Fatalf("expected 1 submit, got %d", len(submitter.calls))
	}
	if submitter.calls[0].RequestID != "req-via-bootstrap" || submitter.calls[0].Action != "approve" {
		t.Fatalf("unexpected submit: %+v", submitter.calls[0])
	}
}

func TestBuildFeishuPlugin_ClientBotOpenIDCanBePrewarmed(t *testing.T) {
	logger := zap.NewNop()
	router := channel.NewRouter(nil, logger)

	plugin, err := buildFeishuPlugin(config.FeishuConfig{}, router, nil, nil, nil, logger)
	if err != nil {
		t.Fatalf("buildFeishuPlugin returned error: %v", err)
	}
	if plugin == nil {
		t.Fatal("expected non-nil plugin")
	}
	if got := plugin.Client().BotOpenID(); got != "" {
		t.Fatalf("BotOpenID() without real backend = %q, want empty degrade", got)
	}
}

func TestBuildFeishuGovernance_WiresResetACLFromConfig(t *testing.T) {
	governance := buildFeishuGovernance(config.FeishuConfig{
		Governance: config.FeishuGovernanceConfig{
			CommandACL: config.FeishuCommandACLConfig{
				ResetAllowlist: map[string][]string{
					"tenant-a": {"ou-admin"},
				},
			},
			ModelAllowlist: []string{"gpt-5.2"},
		},
	}, &feishuReloadRepo{}, &feishuReloadTerminator{}, nil, nil, zap.NewNop())

	resp, nextSessionID, handled, err := governance.ExecuteCommand(context.Background(), channel.InboundMessage{
		Platform:  channel.PlatformFeishu,
		TenantKey: "tenant-a",
		ChatID:    "oc-chat",
		SenderID:  "ou-user",
		ChatType:  channel.ChatGroup,
	}, "sess-1", feishu.ParsedCommand{Name: "reset", Raw: "/reset"})
	if err != nil {
		t.Fatalf("unexpected deny error: %v", err)
	}
	if !handled || nextSessionID != "" || resp != "你没有权限执行 /reset" {
		t.Fatalf("unexpected deny result: handled=%v next=%q resp=%q", handled, nextSessionID, resp)
	}

	resp, nextSessionID, handled, err = governance.ExecuteCommand(context.Background(), channel.InboundMessage{
		Platform:  channel.PlatformFeishu,
		TenantKey: "tenant-a",
		ChatID:    "oc-chat",
		SenderID:  "ou-admin",
		ChatType:  channel.ChatGroup,
	}, "sess-1", feishu.ParsedCommand{Name: "reset", Raw: "/reset"})
	if err != nil {
		t.Fatalf("unexpected allow error: %v", err)
	}
	if !handled || nextSessionID == "" || resp == "" {
		t.Fatalf("unexpected allow result: handled=%v next=%q resp=%q", handled, nextSessionID, resp)
	}

	resp, nextSessionID, handled, err = governance.ExecuteCommand(context.Background(), channel.InboundMessage{
		Platform:  channel.PlatformFeishu,
		TenantKey: "tenant-a",
		ChatID:    "oc-chat",
		SenderID:  "ou-admin",
		ChatType:  channel.ChatGroup,
	}, "sess-1", feishu.ParsedCommand{Name: "model", Raw: "/model", Arg: "gpt-5.2"})
	if err != nil {
		t.Fatalf("unexpected model error: %v", err)
	}
	if !handled || nextSessionID != "" || resp != "已切换本群模型: gpt-5.2" {
		t.Fatalf("unexpected model result: handled=%v next=%q resp=%q", handled, nextSessionID, resp)
	}
}

func TestBuildFeishuGovernance_UsesGroupAdminACLWhenClientAvailable(t *testing.T) {
	logger := zap.NewNop()
	checker := stubBootstrapGroupAdminChecker{}

	governance := buildFeishuGovernance(config.FeishuConfig{}, &feishuReloadRepo{}, &feishuReloadTerminator{}, checker, nil, logger)
	if governance == nil {
		t.Fatal("expected governance")
	}

	resp, nextSessionID, handled, err := governance.ExecuteCommand(context.Background(), channel.InboundMessage{
		Platform:  channel.PlatformFeishu,
		TenantKey: "tenant-a",
		ChatID:    "oc-chat",
		SenderID:  "ou-user",
		ChatType:  channel.ChatGroup,
	}, "sess-1", feishu.ParsedCommand{Name: "reset", Raw: "/reset"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled || nextSessionID != "" || resp != "你没有权限执行 /reset" {
		t.Fatalf("unexpected result: handled=%v next=%q resp=%q", handled, nextSessionID, resp)
	}
}

type stubBootstrapGroupAdminChecker struct{}

func (stubBootstrapGroupAdminChecker) IsGroupAdmin(context.Context, string, string, string) (bool, error) {
	return false, nil
}

func TestBuildReloadChannelFunc_FeishuIngressSwitch_WebhookToLongconn(t *testing.T) {
	logger := zap.NewNop()
	router := channel.NewRouter(nil, logger)
	cfg := &config.Config{
		Channel: config.ChannelConfig{
			Feishu: config.FeishuConfig{
				Enabled:     true,
				IngressMode: config.FeishuIngressModeWebhook,
			},
		},
	}
	var mu sync.RWMutex
	committedMode := config.FeishuIngressModeWebhook
	originalBuildFn := buildFeishuPluginFn
	t.Cleanup(func() {
		buildFeishuPluginFn = originalBuildFn
	})
	buildFeishuPluginFn = func(
		cfg config.FeishuConfig,
		router *channel.Router,
		hitlSubmitter feishu.InputSubmitter,
		governance *feishu.GovernanceService,
		lifecycleHandler *feishu.LifecycleHandler,
		logger *zap.Logger,
	) (*feishu.Plugin, error) {
		plugin, err := buildFeishuPlugin(cfg, router, hitlSubmitter, governance, lifecycleHandler, logger)
		if err != nil {
			return nil, err
		}
		plugin.SetLongConnStartHookForTest(func(context.Context) error { return nil })
		return plugin, nil
	}

	fn := BuildReloadChannelFunc(
		cfg,
		router,
		nil,
		nil,
		nil,
		nil,
		func() config.FeishuIngressMode { return committedMode },
		func(mode config.FeishuIngressMode) { committedMode = mode },
		func() config.FeishuIngressMode { return committedMode },
		func(mode config.FeishuIngressMode) { committedMode = mode },
		&mu,
		logger,
	)
	if fn == nil {
		t.Fatal("expected non-nil reload func")
	}

	if err := fn("feishu"); err != nil {
		t.Fatalf("initial webhook reload failed: %v", err)
	}

	cfg.Channel.Feishu.IngressMode = config.FeishuIngressModeLongconn
	if err := fn("feishu"); err != nil {
		t.Fatalf("switch to longconn failed: %v", err)
	}

	plugin, ok := router.GetPlugin(channel.PlatformFeishu)
	if !ok {
		t.Fatal("expected feishu plugin after reload")
	}
	fsPlugin, ok := plugin.(*feishu.Plugin)
	if !ok {
		t.Fatalf("unexpected plugin type: %T", plugin)
	}
	if !fsPlugin.LongConnStartedForTest() {
		t.Fatal("expected longconn started after switch")
	}
	if committedMode != config.FeishuIngressModeLongconn {
		t.Fatalf("expected committed mode longconn, got %q", committedMode)
	}
}

func TestBuildReloadChannelFunc_FeishuIngressSwitch_LongconnToWebhook(t *testing.T) {
	logger := zap.NewNop()
	router := channel.NewRouter(nil, logger)
	cfg := &config.Config{
		Channel: config.ChannelConfig{
			Feishu: config.FeishuConfig{
				Enabled:     true,
				IngressMode: config.FeishuIngressModeLongconn,
			},
		},
	}
	var mu sync.RWMutex
	committedMode := config.FeishuIngressModeLongconn
	gateMode := config.FeishuIngressModeLongconn
	originalBuildFn := buildFeishuPluginFn
	t.Cleanup(func() {
		buildFeishuPluginFn = originalBuildFn
	})
	buildFeishuPluginFn = func(
		cfg config.FeishuConfig,
		router *channel.Router,
		hitlSubmitter feishu.InputSubmitter,
		governance *feishu.GovernanceService,
		lifecycleHandler *feishu.LifecycleHandler,
		logger *zap.Logger,
	) (*feishu.Plugin, error) {
		plugin, err := buildFeishuPlugin(cfg, router, hitlSubmitter, governance, lifecycleHandler, logger)
		if err != nil {
			return nil, err
		}
		plugin.SetLongConnStartHookForTest(func(context.Context) error { return nil })
		return plugin, nil
	}

	fn := BuildReloadChannelFunc(
		cfg,
		router,
		nil,
		nil,
		nil,
		nil,
		func() config.FeishuIngressMode { return committedMode },
		func(mode config.FeishuIngressMode) { committedMode = mode },
		func() config.FeishuIngressMode { return gateMode },
		func(mode config.FeishuIngressMode) { gateMode = mode },
		&mu,
		logger,
	)
	if fn == nil {
		t.Fatal("expected non-nil reload func")
	}

	if err := fn("feishu"); err != nil {
		t.Fatalf("initial longconn reload failed: %v", err)
	}

	plugin, ok := router.GetPlugin(channel.PlatformFeishu)
	if !ok {
		t.Fatal("expected feishu plugin after initial longconn reload")
	}
	fsPlugin, ok := plugin.(*feishu.Plugin)
	if !ok {
		t.Fatalf("unexpected plugin type: %T", plugin)
	}
	if !fsPlugin.LongConnStartedForTest() {
		t.Fatal("expected longconn started before switch")
	}

	cfg.Channel.Feishu.IngressMode = config.FeishuIngressModeWebhook
	if err := fn("feishu"); err != nil {
		t.Fatalf("switch to webhook failed: %v", err)
	}

	plugin, ok = router.GetPlugin(channel.PlatformFeishu)
	if !ok {
		t.Fatal("expected feishu plugin after webhook switch")
	}
	fsPlugin, ok = plugin.(*feishu.Plugin)
	if !ok {
		t.Fatalf("unexpected plugin type after switch: %T", plugin)
	}
	if fsPlugin.LongConnStartedForTest() {
		t.Fatal("expected longconn stopped in webhook mode")
	}
	if committedMode != config.FeishuIngressModeWebhook {
		t.Fatalf("expected committed mode webhook, got %q", committedMode)
	}
	if gateMode != config.FeishuIngressModeWebhook {
		t.Fatalf("expected gate mode webhook, got %q", gateMode)
	}
}

func TestBuildReloadChannelFunc_FeishuLongconnToWebhook_KeepsGateClosedUntilCommit(t *testing.T) {
	logger := zap.NewNop()
	router := channel.NewRouter(nil, logger)
	cfg := &config.Config{
		Channel: config.ChannelConfig{
			Feishu: config.FeishuConfig{
				Enabled:     true,
				IngressMode: config.FeishuIngressModeLongconn,
			},
		},
	}
	var mu sync.RWMutex
	committedMode := config.FeishuIngressModeLongconn
	gateMode := config.FeishuIngressModeLongconn
	var gateHistory []config.FeishuIngressMode
	originalBuildFn := buildFeishuPluginFn
	t.Cleanup(func() {
		buildFeishuPluginFn = originalBuildFn
	})
	buildFeishuPluginFn = func(
		cfg config.FeishuConfig,
		router *channel.Router,
		hitlSubmitter feishu.InputSubmitter,
		governance *feishu.GovernanceService,
		lifecycleHandler *feishu.LifecycleHandler,
		logger *zap.Logger,
	) (*feishu.Plugin, error) {
		plugin, err := buildFeishuPlugin(cfg, router, hitlSubmitter, governance, lifecycleHandler, logger)
		if err != nil {
			return nil, err
		}
		plugin.SetLongConnStartHookForTest(func(context.Context) error { return nil })
		return plugin, nil
	}

	fn := BuildReloadChannelFunc(
		cfg,
		router,
		nil,
		nil,
		nil,
		nil,
		func() config.FeishuIngressMode { return committedMode },
		func(mode config.FeishuIngressMode) { committedMode = mode },
		func() config.FeishuIngressMode { return gateMode },
		func(mode config.FeishuIngressMode) {
			gateMode = mode
			gateHistory = append(gateHistory, mode)
			if mode == config.FeishuIngressModeWebhook && committedMode != config.FeishuIngressModeWebhook {
				t.Fatalf("webhook gate opened before committed mode switched: committed=%q", committedMode)
			}
		},
		&mu,
		logger,
	)
	if fn == nil {
		t.Fatal("expected non-nil reload func")
	}

	if err := fn("feishu"); err != nil {
		t.Fatalf("initial longconn reload failed: %v", err)
	}
	cfg.Channel.Feishu.IngressMode = config.FeishuIngressModeWebhook
	if err := fn("feishu"); err != nil {
		t.Fatalf("switch to webhook failed: %v", err)
	}
	if len(gateHistory) == 0 {
		t.Fatal("expected gate transitions during reload")
	}
}

func TestBuildReloadChannelFunc_FeishuLongconnStartFailure_StaysFailClosed(t *testing.T) {
	logger := zap.NewNop()
	router := channel.NewRouter(nil, logger)
	cfg := &config.Config{
		Channel: config.ChannelConfig{
			Feishu: config.FeishuConfig{
				Enabled:     true,
				IngressMode: config.FeishuIngressModeWebhook,
			},
		},
	}
	var mu sync.RWMutex
	committedMode := config.FeishuIngressModeWebhook
	gateMode := config.FeishuIngressModeWebhook

	originalBuildFn := buildFeishuPluginFn
	t.Cleanup(func() {
		buildFeishuPluginFn = originalBuildFn
	})
	buildFeishuPluginFn = func(
		cfg config.FeishuConfig,
		router *channel.Router,
		hitlSubmitter feishu.InputSubmitter,
		governance *feishu.GovernanceService,
		lifecycleHandler *feishu.LifecycleHandler,
		logger *zap.Logger,
	) (*feishu.Plugin, error) {
		plugin, err := buildFeishuPlugin(cfg, router, hitlSubmitter, governance, lifecycleHandler, logger)
		if err != nil {
			return nil, err
		}
		plugin.SetLongConnStartHookForTest(func(context.Context) error { return nil })
		return plugin, nil
	}

	fn := BuildReloadChannelFunc(
		cfg,
		router,
		nil,
		nil,
		nil,
		nil,
		func() config.FeishuIngressMode { return committedMode },
		func(mode config.FeishuIngressMode) { committedMode = mode },
		func() config.FeishuIngressMode { return gateMode },
		func(mode config.FeishuIngressMode) { gateMode = mode },
		&mu,
		logger,
	)
	if fn == nil {
		t.Fatal("expected non-nil reload func")
	}
	if err := fn("feishu"); err != nil {
		t.Fatalf("initial webhook reload failed: %v", err)
	}

	buildFeishuPluginFn = func(
		cfg config.FeishuConfig,
		router *channel.Router,
		hitlSubmitter feishu.InputSubmitter,
		governance *feishu.GovernanceService,
		lifecycleHandler *feishu.LifecycleHandler,
		logger *zap.Logger,
	) (*feishu.Plugin, error) {
		plugin, err := buildFeishuPlugin(cfg, router, hitlSubmitter, governance, lifecycleHandler, logger)
		if err != nil {
			return nil, err
		}
		plugin.SetLongConnStartHookForTest(func(context.Context) error { return context.DeadlineExceeded })
		return plugin, nil
	}

	cfg.Channel.Feishu.IngressMode = config.FeishuIngressModeLongconn
	err := fn("feishu")
	if err == nil {
		t.Fatal("expected reload error when longconn startup fails")
	}
	if committedMode != config.FeishuIngressModeWebhook {
		t.Fatalf("expected committed mode rollback to previous webhook on failure, got %q", committedMode)
	}
	if gateMode != config.FeishuIngressModeWebhook {
		t.Fatalf("expected gate mode restored to previous webhook on failure, got %q", gateMode)
	}
}

func TestBuildReloadChannelFunc_FeishuInboundResolverToggle(t *testing.T) {
	logger := zap.NewNop()
	router := channel.NewRouter(nil, logger)
	cfg := &config.Config{
		Channel: config.ChannelConfig{
			Feishu: config.FeishuConfig{
				Enabled: true,
			},
		},
	}
	var mu sync.RWMutex

	fn := BuildReloadChannelFunc(cfg, router, nil, nil, nil, nil, nil, nil, nil, nil, &mu, logger)
	if fn == nil {
		t.Fatal("expected non-nil reload func")
	}

	if err := fn("feishu"); err != nil {
		t.Fatalf("reload feishu failed: %v", err)
	}
	if router.InboundContextResolver(channel.PlatformFeishu) == nil {
		t.Fatal("expected feishu resolver to be wired by default")
	}

	disabled := false
	cfg.Channel.Feishu.Inbound.EnableContextResolver = &disabled
	if err := fn("feishu"); err != nil {
		t.Fatalf("reload feishu with inbound disabled failed: %v", err)
	}
	if router.InboundContextResolver(channel.PlatformFeishu) != nil {
		t.Fatal("expected feishu resolver to be removed when inbound disabled")
	}
}

func TestBuildReloadChannelFunc_FeishuLifecycleHandlerPreserved(t *testing.T) {
	logger := zap.NewNop()
	router := channel.NewRouter(nil, logger)
	cfg := &config.Config{
		Channel: config.ChannelConfig{
			Feishu: config.FeishuConfig{
				Enabled: true,
			},
		},
	}
	var mu sync.RWMutex
	repo := &feishuReloadRepo{
		markEvictedRecord: &feishu.ChatStateRecord{
			SessionID: "sess-reload",
			State:     feishu.ChatStateEvicted,
		},
		markEvictedChanged: true,
	}
	terminator := &feishuReloadTerminator{}

	fn := BuildReloadChannelFunc(cfg, router, nil, repo, terminator, nil, nil, nil, nil, nil, &mu, logger)
	if fn == nil {
		t.Fatal("expected non-nil reload func")
	}

	if err := fn("feishu"); err != nil {
		t.Fatalf("reload feishu failed: %v", err)
	}

	handler := router.WebhookHandler(channel.PlatformFeishu)
	body := mustJSON(t, map[string]any{
		"schema": "2.0",
		"header": map[string]any{
			"event_id":    "evt-reload-removed",
			"token":       "",
			"create_time": "1700000100",
			"event_type":  "im.chat.member.bot.deleted_v1",
			"tenant_key":  "tenant-reload",
		},
		"event": map[string]any{
			"chat_id": "chat-reload",
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("bot removed after reload must return 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(repo.markEvictedCalls) != 1 {
		t.Fatalf("expected 1 lifecycle mark after reload, got %d", len(repo.markEvictedCalls))
	}
	if len(terminator.calls) != 1 {
		t.Fatalf("expected 1 terminate after reload, got %d", len(terminator.calls))
	}
}

func TestBuildReloadChannelFunc_FeishuInjectsChatStateRepoIntoPlugin(t *testing.T) {
	logger := zap.NewNop()
	router := channel.NewRouter(nil, logger)
	cfg := &config.Config{
		Channel: config.ChannelConfig{
			Feishu: config.FeishuConfig{
				Enabled: true,
			},
		},
	}
	var mu sync.RWMutex
	repo := &feishuReloadRepo{}

	fn := BuildReloadChannelFunc(cfg, router, nil, repo, nil, nil, nil, nil, nil, nil, &mu, logger)
	if fn == nil {
		t.Fatal("expected non-nil reload func")
	}
	if err := fn("feishu"); err != nil {
		t.Fatalf("reload feishu failed: %v", err)
	}

	plugin, ok := router.GetPlugin(channel.PlatformFeishu)
	if !ok {
		t.Fatal("expected feishu plugin after reload")
	}
	fsPlugin, ok := plugin.(*feishu.Plugin)
	if !ok {
		t.Fatalf("unexpected plugin type: %T", plugin)
	}

	if err := fsPlugin.ReplayPendingGapFetchForActiveChats(context.Background(), "tenant-reload"); err != nil {
		t.Fatalf("expected injected chat state repo to be callable, got err=%v", err)
	}
	if repo.listActiveCalls != 1 {
		t.Fatalf("expected chat state repo to be injected into plugin, got listActiveCalls=%d", repo.listActiveCalls)
	}
}

func TestBuildFeishuLifecycleHandler_DefaultsWelcomeSenderFromPluginClient(t *testing.T) {
	repo := &feishuReloadRepo{}
	plugin := &stubFeishuLifecyclePluginClient{}

	handler := buildFeishuLifecycleHandler(repo, &feishuReloadTerminator{}, nil, plugin, nil, zap.NewNop())

	if handler == nil {
		t.Fatal("expected lifecycle handler")
	}
	if buildFeishuWelcomeSender(nil, plugin, nil, zap.NewNop()) == nil {
		t.Fatal("expected default welcome sender from plugin client")
	}
}

func TestBuildReloadChannelFunc_FeishuWithoutLifecycleRepoSkipsLifecycleRegistration(t *testing.T) {
	logger := zap.NewNop()
	router := channel.NewRouter(nil, logger)
	cfg := &config.Config{
		Channel: config.ChannelConfig{
			Feishu: config.FeishuConfig{
				Enabled: true,
			},
		},
	}
	var mu sync.RWMutex

	fn := BuildReloadChannelFunc(cfg, router, nil, nil, nil, nil, nil, nil, nil, nil, &mu, logger)
	if fn == nil {
		t.Fatal("expected non-nil reload func")
	}
	if err := fn("feishu"); err != nil {
		t.Fatalf("reload feishu failed: %v", err)
	}

	handler := router.WebhookHandler(channel.PlatformFeishu)
	body := mustJSON(t, map[string]any{
		"schema": "2.0",
		"header": map[string]any{
			"event_id":    "evt-reload-no-repo",
			"token":       "",
			"create_time": "1700000101",
			"event_type":  "im.chat.member.bot.deleted_v1",
			"tenant_key":  "tenant-reload",
		},
		"event": map[string]any{
			"chat_id": "chat-reload",
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("bot removed without lifecycle repo must not panic, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestBuildFeishuPlugin_HITLClosedLoopResume(t *testing.T) {
	logger := zap.NewNop()
	skillReg := skills.NewRegistry(logger)
	agentReg := subagent.NewRegistry(logger)
	m := master.NewMaster(
		master.Config{Model: "test"},
		config.HITLConfig{Enabled: true, InputTimeout: 2 * time.Second},
		agentReg,
		skillReg,
		nil,
		logger,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	defer m.Stop()

	router := channel.NewRouter(nil, logger)
	plugin, err := buildFeishuPlugin(config.FeishuConfig{}, router, m.GetHITLBroker(), nil, nil, logger)
	if err != nil {
		t.Fatalf("buildFeishuPlugin returned error: %v", err)
	}

	subID, ch := m.SubscribeWSBroadcast()
	defer m.UnsubscribeWSBroadcast(subID)

	resultCh := make(chan error, 1)
	go func() {
		_, emitErr := m.EmitInputRequest(context.Background(), master.InputRequest{
			TaskID: "closed-loop",
			Type:   master.InputChoice,
			Prompt: "approve?",
		})
		resultCh <- emitErr
	}()

	var req *master.InputRequest
	select {
	case msg := <-ch:
		if msg.Type != master.EventTypeInputRequest {
			t.Fatalf("want input_request, got %q", msg.Type)
		}
		var ok bool
		req, ok = msg.Payload.(*master.InputRequest)
		if !ok || req == nil {
			t.Fatalf("payload not *InputRequest, got %T", msg.Payload)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("HITL input_request 未在 500ms 内广播")
	}

	body := mustJSON(t, map[string]any{
		"schema": "2.0",
		"header": map[string]any{
			"event_id":    "evt-hitl-resume",
			"token":       "",
			"create_time": "1700000000",
			"event_type":  "card.action.trigger",
			"tenant_key":  "tk-test",
		},
		"event": map[string]any{
			"operator": map[string]any{
				"open_id":    "ou_clicker",
				"tenant_key": "tk-test",
			},
			"action": map[string]any{
				"tag": "button",
				"value": map[string]any{
					"request_id": req.ID,
					"task_id":    req.TaskID,
					"action":     "approve",
				},
			},
			"context": map[string]any{
				"open_message_id": "om_hitl_resume",
			},
		},
	})

	httpReq := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	plugin.WebhookHandler()(rec, httpReq)

	if rec.Code != http.StatusOK {
		t.Fatalf("card action must return 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	select {
	case emitErr := <-resultCh:
		if emitErr != nil {
			t.Fatalf("EmitInputRequest should resume cleanly, got err=%v", emitErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("EmitInputRequest 未在按钮回调后恢复返回")
	}
}

type feishuReloadRepo struct {
	markEvictedRecord  *feishu.ChatStateRecord
	markEvictedChanged bool
	listActiveRecords  []feishu.ChatStateRecord
	listActiveCalls    int
	markEvictedCalls   []struct {
		tenantKey string
		chatID    string
		eventID   string
	}
}

func (r *feishuReloadRepo) Get(ctx context.Context, platform, tenantKey, chatID string) (*feishu.ChatStateRecord, error) {
	return nil, nil
}

func (r *feishuReloadRepo) ListActive(ctx context.Context, platform, tenantKey string) ([]feishu.ChatStateRecord, error) {
	r.listActiveCalls++
	return r.listActiveRecords, nil
}

func (r *feishuReloadRepo) Upsert(ctx context.Context, record feishu.ChatStateRecord) error {
	return nil
}

func (r *feishuReloadRepo) MarkEvicted(ctx context.Context, platform, tenantKey, chatID, eventID string, eventTime int64, updatedBy string) (*feishu.ChatStateRecord, bool, error) {
	r.markEvictedCalls = append(r.markEvictedCalls, struct {
		tenantKey string
		chatID    string
		eventID   string
	}{
		tenantKey: tenantKey,
		chatID:    chatID,
		eventID:   eventID,
	})
	return r.markEvictedRecord, r.markEvictedChanged, nil
}

func (r *feishuReloadRepo) MarkActive(ctx context.Context, platform, tenantKey, chatID, eventID string, eventTime int64, updatedBy string) (*feishu.ChatStateRecord, bool, error) {
	return &feishu.ChatStateRecord{State: feishu.ChatStateActive}, true, nil
}

func (r *feishuReloadRepo) SetSessionID(ctx context.Context, platform, tenantKey, chatID, sessionID, updatedBy string) error {
	return nil
}

func (r *feishuReloadRepo) SetMuteUntil(ctx context.Context, platform, tenantKey, chatID string, muteUntil *time.Time, updatedBy string) error {
	return nil
}

func (r *feishuReloadRepo) SetRolloutMode(ctx context.Context, platform, tenantKey, chatID string, mode feishu.GovernanceRolloutMode, updatedBy string) error {
	return nil
}

func (r *feishuReloadRepo) SetModelOverride(ctx context.Context, platform, tenantKey, chatID, modelOverride, updatedBy string) error {
	return nil
}

func (r *feishuReloadRepo) SetAgentProfile(ctx context.Context, platform, tenantKey, chatID, agentProfile, updatedBy string) error {
	return nil
}

type feishuReloadTerminator struct {
	calls []struct {
		sessionID string
		reason    string
	}
}

func (t *feishuReloadTerminator) TerminateSession(sessionID, reason string) error {
	t.calls = append(t.calls, struct {
		sessionID string
		reason    string
	}{
		sessionID: sessionID,
		reason:    reason,
	})
	return nil
}

type stubFeishuLifecyclePluginClient struct{}

func (s *stubFeishuLifecyclePluginClient) Client() *feishu.Client {
	return feishu.NewClient("app-id", "app-secret", zap.NewNop())
}

type failingFeishuWebhookOnlyPlugin struct {
	channel.ChannelPlugin
	startCalls int
}

func (p *failingFeishuWebhookOnlyPlugin) Platform() channel.Platform {
	return channel.PlatformFeishu
}

func (p *failingFeishuWebhookOnlyPlugin) Send(context.Context, channel.OutboundMessage) error {
	return nil
}

func (p *failingFeishuWebhookOnlyPlugin) WebhookHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (p *failingFeishuWebhookOnlyPlugin) Verify(*http.Request) bool {
	return true
}

type failingFeishuStoppablePlugin struct {
	failingFeishuWebhookOnlyPlugin
	stopCalls int
}

func (p *failingFeishuStoppablePlugin) Stop() error {
	p.stopCalls++
	return nil
}

func TestBuildReloadMCPFunc_NilHost(t *testing.T) {
	fn := BuildReloadMCPFunc(nil, nil, nil, nil, nil, nil)
	if fn != nil {
		t.Error("expected nil func when host is nil")
	}
}

func TestServerComponents_CancelRefresh_NilCancel(t *testing.T) {
	// Should not panic when refreshCancel is nil
	sc := &ServerComponents{}
	sc.CancelRefresh() // no panic = pass
}

func TestFeishuIngressModeBridge_BindSwitchesToLiveRuntimeState(t *testing.T) {
	bridge := newFeishuIngressModeBridge(config.FeishuIngressModeWebhook)

	if got := bridge.Get(); got != config.FeishuIngressModeWebhook {
		t.Fatalf("expected initial fallback webhook, got %q", got)
	}

	bridge.Set(config.FeishuIngressModeLongconn)
	if got := bridge.Get(); got != config.FeishuIngressModeLongconn {
		t.Fatalf("expected fallback update to longconn before bind, got %q", got)
	}

	liveMode := config.FeishuIngressModeWebhook
	bridge.Bind(
		func() config.FeishuIngressMode { return liveMode },
		func(mode config.FeishuIngressMode) { liveMode = mode },
		func() config.FeishuIngressMode { return liveMode },
		func(mode config.FeishuIngressMode) { liveMode = mode },
	)

	if got := bridge.Get(); got != config.FeishuIngressModeWebhook {
		t.Fatalf("expected bound getter to override fallback, got %q", got)
	}

	bridge.Set(config.FeishuIngressModeLongconn)
	if liveMode != config.FeishuIngressModeLongconn {
		t.Fatalf("expected bound setter to update live mode, got %q", liveMode)
	}
	if got := bridge.Get(); got != config.FeishuIngressModeLongconn {
		t.Fatalf("expected bound getter to read updated live mode, got %q", got)
	}
}

func TestBuildReloadChannelFunc_FeishuIngressBridgeDrivesServerGateAfterBind(t *testing.T) {
	logger := zap.NewNop()
	router := channel.NewRouter(nil, logger)
	cfg := config.Default()
	cfg.Channel.Feishu.Enabled = true
	cfg.Channel.Feishu.IngressMode = config.FeishuIngressModeWebhook

	bridge := newFeishuIngressModeBridge(cfg.Channel.Feishu.ResolvedIngressMode())
	originalBuildFn := buildFeishuPluginFn
	t.Cleanup(func() {
		buildFeishuPluginFn = originalBuildFn
	})
	buildFeishuPluginFn = func(
		cfg config.FeishuConfig,
		router *channel.Router,
		hitlSubmitter feishu.InputSubmitter,
		governance *feishu.GovernanceService,
		lifecycleHandler *feishu.LifecycleHandler,
		logger *zap.Logger,
	) (*feishu.Plugin, error) {
		plugin, err := buildFeishuPlugin(cfg, router, hitlSubmitter, governance, lifecycleHandler, logger)
		if err != nil {
			return nil, err
		}
		plugin.SetLongConnStartHookForTest(func(context.Context) error { return nil })
		return plugin, nil
	}
	var cfgMu sync.RWMutex
	reload := BuildReloadChannelFunc(
		cfg,
		router,
		nil,
		nil,
		nil,
		nil,
		bridge.Get,
		bridge.Set,
		bridge.GetGate,
		bridge.SetGate,
		&cfgMu,
		logger,
	)
	if reload == nil {
		t.Fatal("expected non-nil reload func")
	}

	srv := apiPkg.NewServer(
		config.ServerConfig{Port: 0},
		config.HITLConfig{},
		config.WebUIConfig{},
		nil,
		skills.NewOverlayRegistry(logger),
		cfg,
		"",
		router,
		nil,
		nil,
		logger,
	)
	bridge.Bind(
		srv.GetFeishuIngressMode,
		srv.SetFeishuIngressMode,
		srv.GetFeishuWebhookGateMode,
		srv.SetFeishuWebhookGateMode,
	)
	srv.SetChannelRouter(router)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/channel/feishu/webhook", bytes.NewReader(nil))

	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected webhook mode to forward into plugin handler, got %d", rec.Code)
	}

	cfgMu.Lock()
	cfg.Channel.Feishu.IngressMode = config.FeishuIngressModeLongconn
	cfgMu.Unlock()
	if err := reload("feishu"); err != nil {
		t.Fatalf("reload feishu failed: %v", err)
	}

	if got := srv.GetFeishuIngressMode(); got != config.FeishuIngressModeLongconn {
		t.Fatalf("expected server committed mode updated to longconn, got %q", got)
	}

	rec = httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected longconn committed mode to reject webhook with 404, got %d", rec.Code)
	}
}
