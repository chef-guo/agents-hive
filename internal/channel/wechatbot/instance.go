package wechatbot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/channel"
	"github.com/chef-guo/agents-hive/internal/imctx"
	"github.com/chef-guo/agents-hive/internal/observability"
	"github.com/chef-guo/agents-hive/internal/store"
)

// ErrNoContextToken 表示 SDK 尚无该联系人的可发送上下文。
var ErrNoContextToken = errors.New("wechatbot no context token")

const (
	defaultRecoverDelay       = 30 * time.Second
	defaultMaxRecoverAttempts = 3

	MetricAutoRecoverTotal = "wechatbot_auto_recover_total"
	MetricSDKErrorsTotal   = "wechatbot_sdk_errors_total"
	MetricActiveBots       = "wechatbot_active_bots"
	MetricLoginTotal       = "wechatbot_login_total"
	MetricInboundTotal     = "wechatbot_messages_inbound_total"
	MetricOutboundTotal    = "wechatbot_messages_outbound_total"
	MetricUnavailableTotal = "wechatbot_send_unavailable_total"
	MetricReconnectTotal   = "wechatbot_reconnect_total"
)

type InstanceOptions struct {
	OwnerUserID        string
	CredentialPath     string
	Backend            Backend
	Router             *channel.Router
	Store              Store
	Events             *eventHub
	Logger             *zap.Logger
	RecoverDelay       time.Duration
	MaxRecoverAttempts int
	MetricsWriter      observability.MetricsWriter
}

// BotInstance 是一个系统用户的一条个人微信连接。
type BotInstance struct {
	ownerUserID    string
	ownerAccountID string
	credentialPath string
	backend        Backend
	router         *channel.Router
	store          Store
	events         *eventHub
	logger         *zap.Logger
	metricsWriter  observability.MetricsWriter

	recoverDelay       time.Duration
	maxRecoverAttempts int

	mu            sync.RWMutex
	status        Status
	errMsg        string
	cancel        context.CancelFunc
	stopped       bool
	recovering    bool
	recoverCancel context.CancelFunc
}

func NewInstance(opts InstanceOptions) *BotInstance {
	logger := opts.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	recoverDelay := opts.RecoverDelay
	if recoverDelay <= 0 {
		recoverDelay = defaultRecoverDelay
	}
	maxRecoverAttempts := opts.MaxRecoverAttempts
	if maxRecoverAttempts <= 0 {
		maxRecoverAttempts = defaultMaxRecoverAttempts
	}
	return &BotInstance{
		ownerUserID:        opts.OwnerUserID,
		credentialPath:     opts.CredentialPath,
		backend:            opts.Backend,
		router:             opts.Router,
		store:              opts.Store,
		events:             opts.Events,
		logger:             logger,
		metricsWriter:      opts.MetricsWriter,
		recoverDelay:       recoverDelay,
		maxRecoverAttempts: maxRecoverAttempts,
		status:             StatusNotConnected,
	}
}

func (i *BotInstance) OwnerUserID() string {
	return i.ownerUserID
}

func (i *BotInstance) OwnerAccountID() string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.ownerAccountID
}

func (i *BotInstance) Status() Status {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.status
}

func (i *BotInstance) Error() string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.errMsg
}

func (i *BotInstance) SetMetricsWriter(w observability.MetricsWriter) {
	i.mu.Lock()
	i.metricsWriter = w
	i.mu.Unlock()
}

func (i *BotInstance) Login(ctx context.Context, force bool) error {
	return i.login(ctx, force, true)
}

func (i *BotInstance) login(ctx context.Context, force bool, resetRecovery bool) error {
	i.mu.Lock()
	i.stopped = false
	if resetRecovery {
		if i.recoverCancel != nil {
			i.recoverCancel()
			i.recoverCancel = nil
		}
		i.recovering = false
	}
	i.mu.Unlock()

	i.setStatus(StatusWaitingQRScan, "")
	creds, err := i.backend.Login(ctx, force)
	if err != nil {
		i.setStatus(StatusError, err.Error())
		i.emitMetric(MetricLoginTotal, map[string]any{"status": "failure"})
		return err
	}
	i.emitMetric(MetricLoginTotal, map[string]any{"status": "success"})

	i.mu.Lock()
	i.ownerAccountID = creds.AccountID
	i.mu.Unlock()
	if err := saveWechatIdentity(ctx, i.store, i.ownerUserID, creds); err != nil {
		i.setStatus(StatusError, err.Error())
		return err
	}
	i.enforceCredentialPermission()

	i.backend.OnMessage(func(msg *SDKMessage) {
		i.handleIncoming(context.Background(), msg)
	})
	runCtx, cancel := context.WithCancel(context.Background())
	i.mu.Lock()
	if i.cancel != nil {
		i.cancel()
	}
	i.cancel = cancel
	i.mu.Unlock()

	go i.runLoop(runCtx)
	i.setStatus(StatusOnline, "")
	return nil
}

func (i *BotInstance) Stop() {
	i.mu.Lock()
	i.stopped = true
	if i.recoverCancel != nil {
		i.recoverCancel()
		i.recoverCancel = nil
	}
	i.recovering = false
	if i.cancel != nil {
		i.cancel()
		i.cancel = nil
	}
	i.mu.Unlock()
	i.backend.Stop()
	i.setStatus(StatusOffline, "")
}

func (i *BotInstance) Send(ctx context.Context, peerWxid, content string) error {
	if i.Status() != StatusOnline {
		i.emitMetric(MetricUnavailableTotal, map[string]any{"reason": "offline"})
		i.emitMetric(MetricOutboundTotal, map[string]any{"status": "offline"})
		return errors.New("wechatbot offline")
	}
	err := i.backend.Send(ctx, peerWxid, content)
	if err != nil {
		if strings.Contains(err.Error(), "no context_token") {
			if i.store != nil {
				_ = i.store.UpdateWechatConversationSendState(ctx, i.ownerUserID, peerWxid, false, "no_context")
			}
			i.emitMetric(MetricUnavailableTotal, map[string]any{"reason": "no_context"})
			i.emitMetric(MetricOutboundTotal, map[string]any{"status": "no_context"})
			return ErrNoContextToken
		}
		i.emitMetric(MetricSDKErrorsTotal, map[string]any{"reason": "send_failed"})
		i.emitMetric(MetricOutboundTotal, map[string]any{"status": "failure"})
		return err
	}
	if i.store != nil {
		_ = i.store.UpdateWechatConversationSendState(ctx, i.ownerUserID, peerWxid, true, "ready")
	}
	i.emitMetric(MetricOutboundTotal, map[string]any{"status": "success"})
	return nil
}

func (i *BotInstance) runLoop(ctx context.Context) {
	var runErr error
	defer func() {
		if rec := recover(); rec != nil {
			i.logger.Error("wechatbot 运行 panic 已恢复",
				zap.String("owner_user_hash", safeWechatID(i.ownerUserID)),
				zap.Any("panic", rec))
			runErr = fmt.Errorf("wechatbot run panic: %v", rec)
		}
		if runErr != nil && ctx.Err() == nil {
			i.handleRunFailure(runErr)
		}
	}()
	if err := i.backend.Run(ctx); err != nil && ctx.Err() == nil {
		i.logger.Error("wechatbot 运行失败",
			zap.String("owner_user_hash", safeWechatID(i.ownerUserID)),
			zap.Error(err))
		runErr = err
	}
}

func (i *BotInstance) handleRunFailure(err error) {
	msg := "wechatbot run failed"
	if err != nil {
		msg = err.Error()
	}
	i.setStatus(StatusError, msg)
	i.emitMetric(MetricSDKErrorsTotal, map[string]any{"reason": "run_failed"})

	i.mu.Lock()
	if i.stopped || i.recovering {
		i.mu.Unlock()
		return
	}
	i.recovering = true
	recoverCtx, cancel := context.WithCancel(context.Background())
	i.recoverCancel = cancel
	delay := i.recoverDelay
	maxAttempts := i.maxRecoverAttempts
	i.mu.Unlock()

	go i.autoRecoverLoop(recoverCtx, msg, delay, maxAttempts)
}

func (i *BotInstance) autoRecoverLoop(ctx context.Context, lastErr string, delay time.Duration, maxAttempts int) {
	defer func() {
		i.mu.Lock()
		if i.recoverCancel != nil {
			i.recoverCancel = nil
		}
		i.recovering = false
		i.mu.Unlock()
	}()

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		i.setStatus(StatusRecovering, lastErr)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}
		if i.isStopped() {
			return
		}

		loginCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := i.login(loginCtx, false, false)
		cancel()
		if err == nil {
			i.emitMetric(MetricReconnectTotal, nil)
			i.emitMetric(MetricAutoRecoverTotal, map[string]any{"result": "success"})
			return
		}
		lastErr = err.Error()
		i.emitMetric(MetricAutoRecoverTotal, map[string]any{"result": "failed"})
		i.logger.Warn("wechatbot 自动恢复失败",
			zap.String("owner_user_hash", safeWechatID(i.ownerUserID)),
			zap.Int("attempt", attempt),
			zap.Error(err))
	}

	i.setStatus(StatusReloginRequired, lastErr)
	i.emitMetric(MetricAutoRecoverTotal, map[string]any{"result": "manual_required"})
}

func (i *BotInstance) isStopped() bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.stopped
}

func (i *BotInstance) enforceCredentialPermission() {
	if i.credentialPath == "" {
		return
	}
	if err := os.Chmod(i.credentialPath, 0600); err != nil && !os.IsNotExist(err) {
		i.logger.Warn("wechatbot 凭证文件权限收紧失败",
			zap.String("owner_user_hash", safeWechatID(i.ownerUserID)),
			zap.Error(err))
	}
}

func (i *BotInstance) emitMetric(name string, labels map[string]any) {
	i.mu.RLock()
	writer := i.metricsWriter
	i.mu.RUnlock()
	if writer == nil {
		return
	}
	_ = writer.Record(context.Background(), observability.Metric{
		Name:   name,
		Value:  1,
		Labels: labels,
		Ts:     time.Now(),
	})
}

func (i *BotInstance) handleIncoming(ctx context.Context, msg *SDKMessage) {
	if msg == nil {
		i.logger.Warn("wechatbot 入站消息为空，已丢弃",
			zap.String("owner_user_hash", safeWechatID(i.ownerUserID)))
		return
	}
	if msg.UserID == "" {
		i.logger.Warn("wechatbot 入站消息缺少 peer wxid，已丢弃",
			zap.String("owner_user_hash", safeWechatID(i.ownerUserID)),
			zap.String("msg_type", string(msg.Type)),
			zap.Int("content_len", len(messageContent(msg))),
			zap.Bool("has_context_token", msg.ContextToken != ""),
			zap.String("message_id", messageID(msg)))
		return
	}
	i.emitMetric(MetricInboundTotal, map[string]any{"msg_type": string(msg.Type)})
	now := time.Now()
	content := messageContent(msg)
	i.logger.Info("收到 wechatbot 入站消息",
		zap.String("owner_user_hash", safeWechatID(i.ownerUserID)),
		zap.String("peer_wxid_hash", safeWechatID(msg.UserID)),
		zap.String("msg_type", string(msg.Type)),
		zap.Int("content_len", len(content)),
		zap.Bool("has_context_token", msg.ContextToken != ""),
		zap.String("message_id", messageID(msg)))
	sessionID, err := imctx.BuildSessionID(imctx.PlatformWeChatBot, i.ownerUserID, msg.UserID)
	if err != nil {
		i.logger.Error("构造微信 session_id 失败",
			zap.String("owner_user_hash", safeWechatID(i.ownerUserID)),
			zap.String("peer_wxid_hash", safeWechatID(msg.UserID)),
			zap.Error(err))
		return
	}
	ownerAccountID := i.OwnerAccountID()
	if ownerAccountID == "" {
		ownerAccountID = i.ownerUserID
	}

	if i.store != nil {
		_ = i.store.SaveSession(ctx, buildSessionRecord(sessionID, i.ownerUserID, msg.UserID, now))
		_ = i.store.UpsertWechatConversation(ctx, &store.WechatConversationRecord{
			OwnerUserID:        i.ownerUserID,
			OwnerAccountID:     ownerAccountID,
			PeerWxid:           msg.UserID,
			SessionID:          sessionID,
			ChatType:           string(channel.ChatDirect),
			LastMessagePreview: content,
			LastMessageAt:      &now,
			CanSend:            msg.ContextToken != "",
			SendState:          map[bool]string{true: "ready", false: "unknown"}[msg.ContextToken != ""],
			Metadata: metadataJSON(map[string]any{
				"message_type": string(msg.Type),
			}),
		})
	}

	if i.router == nil {
		i.logger.Warn("wechatbot 入站消息未路由：router 未配置",
			zap.String("owner_user_hash", safeWechatID(i.ownerUserID)),
			zap.String("peer_wxid_hash", safeWechatID(msg.UserID)),
			zap.String("message_id", messageID(msg)))
		return
	}
	inbound := channel.InboundMessage{
		MessageID:   messageID(msg),
		Platform:    channel.PlatformWeChatBot,
		TenantKey:   i.ownerUserID,
		OwnerUserID: i.ownerUserID,
		ChatType:    channel.ChatDirect,
		ChatID:      msg.UserID,
		SenderID:    msg.UserID,
		Content:     content,
		MessageType: string(msg.Type),
		NoDebounce:  false,
		Timestamp:   msg.Timestamp,
	}
	if inbound.Timestamp.IsZero() {
		inbound.Timestamp = now
	}
	if err := i.router.HandleMessage(ctx, inbound); err != nil {
		i.logger.Error("路由微信入站消息失败",
			zap.String("owner_user_hash", safeWechatID(i.ownerUserID)),
			zap.String("peer_wxid_hash", safeWechatID(msg.UserID)),
			zap.Error(err))
	}
}

func safeWechatID(raw string) string {
	return imctx.SafeSenderID(raw)
}

func (i *BotInstance) setStatus(status Status, errMsg string) {
	i.mu.Lock()
	i.status = status
	i.errMsg = errMsg
	i.mu.Unlock()
	if i.events != nil {
		i.events.Publish(i.ownerUserID, Event{
			Type:   "status",
			Status: status,
			Error:  errMsg,
		})
	}
}

func messageContent(msg *SDKMessage) string {
	text := strings.TrimSpace(msg.Text)
	if text != "" {
		return text
	}
	switch msg.Type {
	case "image":
		return "[图片]"
	case "voice":
		return "[语音]"
	case "file":
		return "[文件]"
	case "video":
		return "[视频]"
	default:
		return "[微信消息]"
	}
}

func messageID(msg *SDKMessage) string {
	if msg == nil || msg.Raw == nil {
		return ""
	}
	if msg.Raw.MessageID != 0 {
		return "wechatbot-" + strconvFormatInt(msg.Raw.MessageID)
	}
	if msg.Raw.Seq != 0 {
		return "wechatbot-seq-" + strconvFormatInt(msg.Raw.Seq)
	}
	return ""
}

func strconvFormatInt(v int64) string {
	return strconv.FormatInt(v, 10)
}
