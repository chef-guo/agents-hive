package imcore

import "context"

type Platform string

const (
	PlatformDingTalk  Platform = "dingtalk"
	PlatformFeishu    Platform = "feishu"
	PlatformWeCom     Platform = "wecom"
	PlatformWeChatBot Platform = "wechatbot"
)

type Capability string

const (
	CapabilitySearchRecipients            Capability = "search_recipients"
	CapabilityListRecentConversations     Capability = "list_recent_conversations"
	CapabilitySendText                    Capability = "send_text"
	CapabilitySendImage                   Capability = "send_image"
	CapabilityRequiresOwnerContext        Capability = "requires_owner_context"
	CapabilityRequiresConversationContext Capability = "requires_conversation_context"
)

type CallerScope struct {
	OwnerUserID string
	TenantKey   string
	TraceID     string
}

type Recipient struct {
	Platform       Platform          `json:"platform"`
	ID             string            `json:"id"`
	DisplayName    string            `json:"display_name"`
	Kind           string            `json:"kind"`
	ExternalIDType string            `json:"external_id_type"`
	CanSend        bool              `json:"can_send"`
	SendState      string            `json:"send_state"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

type RecipientLookup struct {
	Query          string
	RecipientID    string
	ConversationID string
	ExternalIDType string
}

type SendTarget struct {
	Platform       Platform
	RecipientID    string
	ConversationID string
	ExternalIDType string
	Content        string
	DryRun         bool
}

type SendResult struct {
	Platform   Platform `json:"platform"`
	TargetID   string   `json:"target_id"`
	TargetKind string   `json:"target_kind"`
	MessageID  string   `json:"message_id,omitempty"`
	Delivered  bool     `json:"delivered"`
	DryRun     bool     `json:"dry_run,omitempty"`
}

type Adapter interface {
	Platform() Platform
	Capabilities() []Capability
	SearchRecipients(ctx context.Context, scope CallerScope, query string, limit int) ([]Recipient, error)
	ListRecentConversations(ctx context.Context, scope CallerScope, limit int) ([]Recipient, error)
	ResolveRecipient(ctx context.Context, scope CallerScope, input RecipientLookup) (Recipient, error)
	SendMessage(ctx context.Context, scope CallerScope, target SendTarget) (SendResult, error)
}
