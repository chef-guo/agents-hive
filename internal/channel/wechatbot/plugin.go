package wechatbot

import (
	"context"
	"errors"
	"net/http"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/channel"
	"github.com/chef-guo/agents-hive/internal/observability"
)

// Plugin 把官方 wechatbot 接入统一 IM Router。
type Plugin struct {
	registry *BotRegistry
	logger   *zap.Logger
}

func NewPlugin(registry *BotRegistry, logger *zap.Logger) *Plugin {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Plugin{registry: registry, logger: logger}
}

func (p *Plugin) Platform() channel.Platform {
	return channel.PlatformWeChatBot
}

func (p *Plugin) Send(ctx context.Context, msg channel.OutboundMessage) error {
	if msg.OwnerUserID == "" {
		return errors.New("wechatbot send requires owner_user_id")
	}
	if msg.TenantKey != msg.OwnerUserID {
		return errors.New("wechatbot send requires tenant_key == owner_user_id")
	}
	if msg.ChatID == "" {
		return errors.New("wechatbot send requires chat_id")
	}
	if p.registry == nil {
		return errors.New("wechatbot registry not initialized")
	}
	inst, ok := p.registry.Get(msg.OwnerUserID)
	if !ok {
		return errors.New("wechatbot not connected")
	}
	return inst.Send(ctx, msg.ChatID, msg.Content)
}

func (p *Plugin) WebhookHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "wechatbot uses long polling", http.StatusNotFound)
	}
}

func (p *Plugin) Verify(*http.Request) bool {
	return true
}

func (p *Plugin) Stop() error {
	if p.registry == nil {
		return nil
	}
	return p.registry.Stop()
}

func (p *Plugin) SetMetricsWriter(w observability.MetricsWriter) {
	if p == nil || p.registry == nil {
		return
	}
	p.registry.SetMetricsWriter(w)
}

func (p *Plugin) SetConfig(cfg Config) {
	if p == nil || p.registry == nil {
		return
	}
	p.registry.SetConfig(cfg)
}

func (p *Plugin) Registry() *BotRegistry {
	return p.registry
}
