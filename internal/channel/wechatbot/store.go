package wechatbot

import (
	"context"
	"encoding/json"
	"time"

	"github.com/chef-guo/agents-hive/internal/imctx"
	"github.com/chef-guo/agents-hive/internal/store"
)

const providerType = "wechatbot"

// Store 是 wechatbot 插件需要的持久化能力最小集合。
type Store interface {
	SaveSession(ctx context.Context, record *store.SessionRecord) error
	AddMessage(ctx context.Context, sessionID, role, content string, metadata map[string]any) error
	UpsertUserExternalID(ctx context.Context, rec *store.UserExternalIDRecord) error
	GetUserExternalID(ctx context.Context, userID, providerType string) (*store.UserExternalIDRecord, error)
	DeleteUserExternalID(ctx context.Context, userID, providerType string) error
	UpsertWechatConversation(ctx context.Context, rec *store.WechatConversationRecord) error
	GetWechatConversationBySessionID(ctx context.Context, sessionID string) (*store.WechatConversationRecord, error)
	GetWechatConversationByOwnerPeer(ctx context.Context, ownerUserID, peerWxid string) (*store.WechatConversationRecord, error)
	ListWechatConversationsByOwner(ctx context.Context, ownerUserID string) ([]*store.WechatConversationRecord, error)
	UpdateWechatConversationSendState(ctx context.Context, ownerUserID, peerWxid string, canSend bool, sendState string) error
}

func metadataJSON(values map[string]any) json.RawMessage {
	data, err := json.Marshal(values)
	if err != nil {
		return nil
	}
	return data
}

func saveWechatIdentity(ctx context.Context, st Store, ownerUserID string, creds *Credentials) error {
	if st == nil || creds == nil || creds.AccountID == "" {
		return nil
	}
	return st.UpsertUserExternalID(ctx, &store.UserExternalIDRecord{
		UserID:       ownerUserID,
		ProviderType: providerType,
		ExternalID:   creds.AccountID,
		Metadata: metadataJSON(map[string]any{
			"sdk_user_id": creds.UserID,
			"saved_at":    creds.SavedAt,
		}),
	})
}

func buildSessionRecord(sessionID, ownerUserID, peerWxid string, now time.Time) *store.SessionRecord {
	nowText := now.Format(time.RFC3339)
	return &store.SessionRecord{
		ID:             sessionID,
		Name:           "微信会话 " + maskPeerID(peerWxid),
		CreatedAt:      nowText,
		UpdatedAt:      nowText,
		LastAccessedAt: nowText,
		Tags:           []string{"wechat"},
		Children:       []string{},
		UserID:         ownerUserID,
	}
}

func maskPeerID(peer string) string {
	safe := imctx.SafeSenderID(peer)
	if safe == "" {
		return "unknown"
	}
	return safe
}
