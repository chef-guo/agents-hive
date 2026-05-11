package wechatbot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/channel"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/observability"
	"github.com/chef-guo/agents-hive/internal/store"
)

// Config 是 wechatbot 插件运行配置。
type Config struct {
	Enabled  bool
	BaseURL  string
	CredRoot string
	LogLevel string
}

func ConfigFromApp(cfg config.WeChatBotConfig, sessionsDir string) Config {
	credRoot := cfg.CredRoot
	if credRoot == "" {
		credRoot = filepath.Join(expandHome(sessionsDir), "..", "wechatbot")
	}
	logLevel := cfg.LogLevel
	if logLevel == "" {
		logLevel = "info"
	}
	return Config{
		Enabled:  cfg.Enabled,
		BaseURL:  cfg.BaseURL,
		CredRoot: credRoot,
		LogLevel: logLevel,
	}
}

type BackendFactory func(ownerUserID, credPath string, hooks BackendOptions) Backend

// BotRegistry 管理每个 owner 的 BotInstance。锁内只做 map 读写，不调用 SDK。
type BotRegistry struct {
	cfg     Config
	router  *channel.Router
	store   Store
	events  *eventHub
	logger  *zap.Logger
	factory BackendFactory
	metrics observability.MetricsWriter

	mu         sync.RWMutex
	instances  map[string]*BotInstance
	ownerLocks map[string]*sync.Mutex
}

func NewRegistry(cfg Config, router *channel.Router, st Store, logger *zap.Logger) *BotRegistry {
	if logger == nil {
		logger = zap.NewNop()
	}
	events := newEventHub()
	r := &BotRegistry{
		cfg:        cfg,
		router:     router,
		store:      st,
		events:     events,
		logger:     logger,
		instances:  make(map[string]*BotInstance),
		ownerLocks: make(map[string]*sync.Mutex),
	}
	r.factory = func(_ string, credPath string, hooks BackendOptions) Backend {
		cfg := r.configSnapshot()
		hooks.BaseURL = cfg.BaseURL
		hooks.CredPath = credPath
		hooks.LogLevel = cfg.LogLevel
		return NewBackend(hooks)
	}
	return r
}

func (r *BotRegistry) configSnapshot() Config {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg
}

func (r *BotRegistry) SetBackendFactory(factory BackendFactory) {
	r.mu.Lock()
	r.factory = factory
	r.mu.Unlock()
}

func (r *BotRegistry) SetMetricsWriter(writer observability.MetricsWriter) {
	r.mu.Lock()
	r.metrics = writer
	instances := make([]*BotInstance, 0, len(r.instances))
	for _, inst := range r.instances {
		instances = append(instances, inst)
	}
	r.mu.Unlock()
	for _, inst := range instances {
		inst.SetMetricsWriter(writer)
	}
}

func (r *BotRegistry) SetEnabled(enabled bool) {
	r.mu.Lock()
	r.cfg.Enabled = enabled
	r.mu.Unlock()
}

func (r *BotRegistry) SetConfig(cfg Config) {
	r.mu.Lock()
	r.cfg = cfg
	r.mu.Unlock()
}

func (r *BotRegistry) emitMetric(ctx context.Context, name string, value float64, labels map[string]any) {
	r.mu.RLock()
	writer := r.metrics
	r.mu.RUnlock()
	if writer == nil {
		return
	}
	_ = writer.Record(ctx, observability.Metric{
		Name:   name,
		Value:  value,
		Labels: labels,
		Ts:     time.Now(),
	})
}

func (r *BotRegistry) Get(ownerUserID string) (*BotInstance, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	inst, ok := r.instances[ownerUserID]
	return inst, ok
}

func (r *BotRegistry) Ensure(ctx context.Context, ownerUserID string, force bool) (*BotInstance, error) {
	r.mu.RLock()
	enabled := r.cfg.Enabled
	r.mu.RUnlock()
	if !enabled {
		return nil, errors.New("wechatbot disabled")
	}
	unlock := r.lockOwner(ownerUserID)
	defer unlock()

	r.mu.RLock()
	inst := r.instances[ownerUserID]
	r.mu.RUnlock()
	if inst != nil {
		if force {
			inst.Stop()
		} else {
			return inst, nil
		}
	}

	credPath, err := r.credentialPath(ownerUserID)
	if err != nil {
		return nil, err
	}
	r.mu.RLock()
	factory := r.factory
	metrics := r.metrics
	r.mu.RUnlock()
	hooks := BackendOptions{
		OnQRURL: func(url string) {
			r.events.Publish(ownerUserID, Event{Type: "qr", Status: StatusWaitingQRScan, QRURL: url})
		},
		OnScanned: func() {
			r.events.Publish(ownerUserID, Event{Type: "scanned", Status: StatusScanned})
		},
		OnExpired: func() {
			r.events.Publish(ownerUserID, Event{Type: "expired", Status: StatusReloginRequired})
		},
		OnError: func(err error) {
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			r.events.Publish(ownerUserID, Event{Type: "error", Status: StatusError, Error: msg})
		},
	}
	backend := factory(ownerUserID, credPath, hooks)
	inst = NewInstance(InstanceOptions{
		OwnerUserID:    ownerUserID,
		CredentialPath: credPath,
		Backend:        backend,
		Router:         r.router,
		Store:          r.store,
		Events:         r.events,
		Logger:         r.logger,
		MetricsWriter:  metrics,
	})

	r.mu.Lock()
	if current := r.instances[ownerUserID]; current != nil && !force {
		r.mu.Unlock()
		return current, nil
	}
	r.instances[ownerUserID] = inst
	activeCount := len(r.instances)
	r.mu.Unlock()
	r.emitMetric(ctx, MetricActiveBots, float64(activeCount), nil)

	if err := inst.Login(ctx, force); err != nil {
		return inst, err
	}
	return inst, nil
}

func (r *BotRegistry) lockOwner(ownerUserID string) func() {
	r.mu.Lock()
	lock := r.ownerLocks[ownerUserID]
	if lock == nil {
		lock = &sync.Mutex{}
		r.ownerLocks[ownerUserID] = lock
	}
	r.mu.Unlock()

	lock.Lock()
	return lock.Unlock
}

func (r *BotRegistry) Logout(ctx context.Context, ownerUserID string) error {
	unlock := r.lockOwner(ownerUserID)
	defer unlock()

	r.mu.Lock()
	inst := r.instances[ownerUserID]
	delete(r.instances, ownerUserID)
	activeCount := len(r.instances)
	r.mu.Unlock()
	r.emitMetric(ctx, MetricActiveBots, float64(activeCount), nil)
	if inst != nil {
		inst.Stop()
	}
	if r.store != nil {
		_ = r.store.DeleteUserExternalID(ctx, ownerUserID, providerType)
	}
	cfg := r.configSnapshot()
	credDir := filepath.Join(cfg.CredRoot, "users", ownerUserID)
	if err := os.RemoveAll(credDir); err != nil {
		return err
	}
	r.events.Publish(ownerUserID, Event{Type: "status", Status: StatusNotConnected})
	return nil
}

func (r *BotRegistry) Stop() error {
	r.mu.Lock()
	instances := make([]*BotInstance, 0, len(r.instances))
	for _, inst := range r.instances {
		instances = append(instances, inst)
	}
	r.instances = make(map[string]*BotInstance)
	r.mu.Unlock()
	r.emitMetric(context.Background(), MetricActiveBots, 0, nil)
	for _, inst := range instances {
		inst.Stop()
	}
	return nil
}

func (r *BotRegistry) Status(ctx context.Context, ownerUserID string) (ConnectionStatus, error) {
	r.mu.RLock()
	enabled := r.cfg.Enabled
	r.mu.RUnlock()
	status := ConnectionStatus{
		Enabled: enabled,
		Status:  StatusDisabled,
	}
	if !enabled {
		return status, nil
	}
	status.Status = StatusNotConnected
	if r.store != nil {
		if rec, err := r.store.GetUserExternalID(ctx, ownerUserID, providerType); err == nil {
			status.OwnerAccountID = rec.ExternalID
		}
		if convs, err := r.store.ListWechatConversationsByOwner(ctx, ownerUserID); err == nil {
			status.ConversationCount = len(convs)
		}
	}
	if inst, ok := r.Get(ownerUserID); ok {
		status.Status = inst.Status()
		status.Error = inst.Error()
		if accountID := inst.OwnerAccountID(); accountID != "" {
			status.OwnerAccountID = accountID
		}
	}
	return status, nil
}

func (r *BotRegistry) Subscribe(ownerUserID string) (<-chan Event, func()) {
	return r.events.Subscribe(ownerUserID)
}

func (r *BotRegistry) credentialPath(ownerUserID string) (string, error) {
	cfg := r.configSnapshot()
	dir := filepath.Join(cfg.CredRoot, "users", ownerUserID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	_ = os.Chmod(dir, 0700)
	path := filepath.Join(dir, "credentials.json")
	return path, nil
}

// ConnectionStatus 是 API 状态响应的内部 DTO。
type ConnectionStatus struct {
	Enabled           bool      `json:"enabled"`
	Status            Status    `json:"status"`
	OwnerAccountID    string    `json:"owner_account_id,omitempty"`
	DisplayName       string    `json:"display_name,omitempty"`
	AvatarURL         string    `json:"avatar_url,omitempty"`
	ConversationCount int       `json:"conversation_count"`
	LastConnectedAt   time.Time `json:"last_connected_at,omitempty"`
	Error             string    `json:"error,omitempty"`
}

// Conversation 是设置页最近联系人状态 DTO。
// 不向 Web 暴露内部 im-* session_id、消息内容预览或发送上下文。
type Conversation struct {
	PeerWxid      string     `json:"peer_wxid"`
	PeerNickname  string     `json:"peer_nickname,omitempty"`
	PeerAvatarURL string     `json:"peer_avatar_url,omitempty"`
	ChatType      string     `json:"chat_type"`
	LastMessageAt *time.Time `json:"last_message_at,omitempty"`
}

func conversationsFromRecords(records []*store.WechatConversationRecord) []Conversation {
	out := make([]Conversation, 0, len(records))
	for _, rec := range records {
		out = append(out, Conversation{
			PeerWxid:      rec.PeerWxid,
			PeerNickname:  rec.PeerNickname,
			PeerAvatarURL: rec.PeerAvatarURL,
			ChatType:      rec.ChatType,
			LastMessageAt: rec.LastMessageAt,
		})
	}
	return out
}

func expandHome(path string) string {
	if path == "" {
		return "."
	}
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
