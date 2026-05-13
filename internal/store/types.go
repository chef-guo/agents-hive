package store

import (
	"context"
	"encoding/json"
	"time"
)

// SessionStore 定义会话存储的统一接口
// PostgresStore 实现此接口
type SessionStore interface {
	// CreateSession 创建新会话
	CreateSession(ctx context.Context, record *SessionRecord) error
	// SaveSession 保存或更新会话元数据
	SaveSession(ctx context.Context, record *SessionRecord) error
	// LoadSession 加载会话元数据
	LoadSession(ctx context.Context, sessionID string) (*SessionRecord, error)
	// DeleteSession 删除会话
	DeleteSession(ctx context.Context, sessionID string) error
	// ListSessions 列出所有会话（按最后访问时间倒序）
	ListSessions(ctx context.Context) ([]*SessionRecord, error)
	// ListSessionsByUser 按用户过滤会话列表（userID 为空且 isAdmin=false 时返回空列表）
	ListSessionsByUser(ctx context.Context, userID string, isAdmin bool) ([]*SessionRecord, error)
	// GetLastActiveSession 获取最近活跃的会话
	GetLastActiveSession(ctx context.Context) (*SessionRecord, error)

	// AddMessage 向会话追加消息
	AddMessage(ctx context.Context, sessionID, role, content string, metadata map[string]any) error
	// GetMessages 获取会话消息列表（limit=0 表示全部）
	GetMessages(ctx context.Context, sessionID string, limit int) ([]MessageRecord, error)

	// ForkSession 从指定会话和消息点创建分支
	ForkSession(ctx context.Context, parentID string, forkPoint int, newSessionID, newName, userID string) error
	// RevertSession 回滚会话到指定消息索引
	RevertSession(ctx context.Context, sessionID string, revertTo int) error

	// 收藏偏好
	UpsertSessionPref(ctx context.Context, userID, sessionID string, starred bool) error
	GetSessionStarred(ctx context.Context, userID, sessionID string) (bool, error)

	// 标签（独立更新，不走 SaveSession UPSERT）
	UpdateSessionTags(ctx context.Context, sessionID string, tags []string) error

	// Close 关闭存储（释放资源）
	Close() error
}

// Store 统一存储接口（PostgresStore 实现此接口）
// 在 SessionStore 基础上扩展配置管理、IM 通道和 MCP 服务端管理能力
type Store interface {
	SessionStore // 已有：会话/消息 CRUD

	// 配置管理（键值对）
	GetConfig(ctx context.Context, key string) (string, error)
	SetConfig(ctx context.Context, key, value string) error
	GetAllConfig(ctx context.Context) (map[string]string, error)

	// IM 通道配置
	GetChannelConfig(ctx context.Context, platform string) (*ChannelConfigRecord, error)
	SaveChannelConfig(ctx context.Context, rec *ChannelConfigRecord) error
	ListChannelConfigs(ctx context.Context) ([]*ChannelConfigRecord, error)

	// 官方 wechatbot 用户绑定与会话映射
	UpsertUserExternalID(ctx context.Context, rec *UserExternalIDRecord) error
	GetUserExternalID(ctx context.Context, userID, providerType string) (*UserExternalIDRecord, error)
	DeleteUserExternalID(ctx context.Context, userID, providerType string) error
	UpsertWechatConversation(ctx context.Context, rec *WechatConversationRecord) error
	GetWechatConversationBySessionID(ctx context.Context, sessionID string) (*WechatConversationRecord, error)
	GetWechatConversationByOwnerPeer(ctx context.Context, ownerUserID, peerWxid string) (*WechatConversationRecord, error)
	ListWechatConversationsByOwner(ctx context.Context, ownerUserID string) ([]*WechatConversationRecord, error)
	UpdateWechatConversationSendState(ctx context.Context, ownerUserID, peerWxid string, canSend bool, sendState string) error
	UpdateWechatConversationContextToken(ctx context.Context, ownerUserID, peerWxid, contextToken string) error
	GetWechatConversationContextToken(ctx context.Context, ownerUserID, peerWxid string) (string, error)
	ClearWechatConversationContextTokens(ctx context.Context, ownerUserID string) error

	// 通道 Push 定时任务
	SaveScheduledPush(ctx context.Context, rec *ScheduledPushRecord) error
	GetScheduledPush(ctx context.Context, id string) (*ScheduledPushRecord, error)
	DeleteScheduledPush(ctx context.Context, id string) error
	ListScheduledPushes(ctx context.Context, platform string) ([]*ScheduledPushRecord, error)
	UpdateScheduledPushRun(ctx context.Context, id string, lastRunAt, nextRunAt time.Time, lastError string) error

	// Agent 定时任务
	SaveScheduledTask(ctx context.Context, rec *ScheduledTask) error
	GetScheduledTask(ctx context.Context, id string) (*ScheduledTask, error)
	DeleteScheduledTask(ctx context.Context, id string) error
	ListScheduledTasksByUser(ctx context.Context, createdBy string) ([]*ScheduledTask, error)
	ListAllScheduledTasks(ctx context.Context) ([]*ScheduledTask, error)
	ListEnabledScheduledTasks(ctx context.Context) ([]*ScheduledTask, error)
	ListScheduledTaskRuns(ctx context.Context, taskID string, limit int) ([]*ScheduledTaskRun, error)
	CountRecentScheduledTaskFailures(ctx context.Context, taskID string, limit int) (int, int, error)
	BulkMarkScheduledTaskReloadFailures(ctx context.Context, failures map[string]string) error
	EnsureScheduledTaskRunPartition(ctx context.Context, scheduledAt time.Time) error
	MaintainScheduledTaskRunPartitions(ctx context.Context, now time.Time, retainWeeks int) error
	ClaimDueScheduledTaskRun(ctx context.Context, taskID string, now time.Time, runID string, leaseUntil time.Time, nextRunAt time.Time, claimedBy string) (*ScheduledTaskRun, error)
	ClaimManualScheduledTaskRun(ctx context.Context, taskID string, now time.Time, runID string, leaseUntil time.Time, claimedBy string) (*ScheduledTaskRun, error)
	FinishScheduledTaskRun(ctx context.Context, run *ScheduledTaskRun) error

	// MCP 服务端配置
	GetMCPServer(ctx context.Context, name string) (*MCPServerRecord, error)
	SaveMCPServer(ctx context.Context, rec *MCPServerRecord) error
	DeleteMCPServer(ctx context.Context, name string) error
	ListMCPServers(ctx context.Context) ([]*MCPServerRecord, error)

	// 外部资源配置
	GetExternalResource(ctx context.Context, name string) (*ExternalResourceRecord, error)
	SaveExternalResource(ctx context.Context, rec *ExternalResourceRecord) error
	DeleteExternalResource(ctx context.Context, name string) error
	ListExternalResources(ctx context.Context) ([]*ExternalResourceRecord, error)

	// LLM 提供商配置
	GetLLMProvider(ctx context.Context, name string) (*LLMProviderRecord, error)
	SaveLLMProvider(ctx context.Context, rec *LLMProviderRecord) error
	DeleteLLMProvider(ctx context.Context, name string) error
	ListLLMProviders(ctx context.Context) ([]*LLMProviderRecord, error)
	// SetDefaultLLMProvider 原子化地将指定 Provider 设为默认（事务保证唯一性）
	SetDefaultLLMProvider(ctx context.Context, name string) error

	// LLM 模型配置
	GetLLMModel(ctx context.Context, name string) (*LLMModelRecord, error)
	SaveLLMModel(ctx context.Context, rec *LLMModelRecord) error
	UpdateLLMModel(ctx context.Context, oldName string, rec *LLMModelRecord) error
	DeleteLLMModel(ctx context.Context, name string) error
	ListLLMModels(ctx context.Context) ([]*LLMModelRecord, error)
	// SetDefaultLLMModel 原子化地将指定 Model 设为默认（事务保证唯一性）
	SetDefaultLLMModel(ctx context.Context, name string) error

	// 权限管理
	SaveGrant(ctx context.Context, rec *PermissionGrantRecord) error
	LoadGrants(ctx context.Context) ([]PermissionGrantRecord, error)
	DeleteGrant(ctx context.Context, id int64) error
	DeleteAllGrants(ctx context.Context) error

	// OAuth Token 管理
	SaveOAuthToken(ctx context.Context, token *OAuthTokenRecord) error
	LoadOAuthToken(ctx context.Context, serverURL string) (*OAuthTokenRecord, error)
	DeleteOAuthToken(ctx context.Context, serverURL string) error

	// 配置变更通知（PG: LISTEN/NOTIFY）
	OnConfigChange(handler func(key string))

	// Close() 继承自 SessionStore，无需重复声明
}

// ChannelConfigRecord IM 通道配置记录
type ChannelConfigRecord struct {
	Platform   string    `json:"platform"` // "dingtalk" | "feishu" | "wecom" | "wechatbot"
	Enabled    bool      `json:"enabled"`
	ConfigJSON string    `json:"config_json"` // JSON 序列化的平台特定配置
	UpdatedAt  time.Time `json:"updated_at"`
}

// UserExternalIDRecord 记录非登录型外部账号绑定，例如官方 wechatbot 账号。
type UserExternalIDRecord struct {
	ID           int64           `json:"id"`
	UserID       string          `json:"user_id"`
	ProviderType string          `json:"provider_type"`
	ExternalID   string          `json:"external_id"`
	DisplayName  string          `json:"display_name,omitempty"`
	AvatarURL    string          `json:"avatar_url,omitempty"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
	CreatedAt    time.Time       `json:"created_at,omitempty"`
	UpdatedAt    time.Time       `json:"updated_at,omitempty"`
}

// WechatConversationRecord 记录个人微信联系人与内部 session 的映射。
type WechatConversationRecord struct {
	ID                 int64           `json:"id"`
	OwnerUserID        string          `json:"owner_user_id"`
	OwnerAccountID     string          `json:"owner_account_id"`
	PeerWxid           string          `json:"peer_wxid"`
	SessionID          string          `json:"session_id"`
	PeerNickname       string          `json:"peer_nickname,omitempty"`
	PeerAvatarURL      string          `json:"peer_avatar_url,omitempty"`
	ChatType           string          `json:"chat_type"`
	LastMessagePreview string          `json:"last_message_preview,omitempty"`
	LastMessageAt      *time.Time      `json:"last_message_at,omitempty"`
	CanSend            bool            `json:"can_send"`
	SendState          string          `json:"send_state"`
	ContextToken       string          `json:"-"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
	CreatedAt          time.Time       `json:"created_at,omitempty"`
	UpdatedAt          time.Time       `json:"updated_at,omitempty"`
}

// ScheduledPushRecord 是持久化的通道定时推送任务。
type ScheduledPushRecord struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Platform    string    `json:"platform"`
	Prompt      string    `json:"prompt"`
	IntervalSec int       `json:"interval_sec"`
	Enabled     bool      `json:"enabled"`
	CreatedBy   string    `json:"created_by,omitempty"`
	LastRunAt   time.Time `json:"last_run_at,omitempty"`
	NextRunAt   time.Time `json:"next_run_at,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

// ScheduledTask 是 Agent 定时任务。物理表沿用 scheduled_pushes。
type ScheduledTask struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Description    string         `json:"description,omitempty"`
	TargetType     string         `json:"target_type"`
	TargetConfig   map[string]any `json:"target_config"`
	Platform       string         `json:"platform,omitempty"`
	Prompt         string         `json:"prompt"`
	CronExpr       string         `json:"cron_expr,omitempty"`
	IntervalSec    int            `json:"interval_sec,omitempty"`
	Timezone       string         `json:"timezone"`
	Enabled        bool           `json:"enabled"`
	CreatedBy      string         `json:"created_by"`
	LastRunAt      *time.Time     `json:"last_run_at,omitempty"`
	NextRunAt      *time.Time     `json:"next_run_at,omitempty"`
	LastError      string         `json:"last_error,omitempty"`
	ActiveRunID    string         `json:"active_run_id,omitempty"`
	LeaseExpiresAt *time.Time     `json:"lease_expires_at,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

// ScheduledTaskRun 是一次定时任务运行记录。
type ScheduledTaskRun struct {
	ScheduledAt    time.Time  `json:"scheduled_at"`
	ID             string     `json:"id"`
	TaskID         string     `json:"task_id"`
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	Status         string     `json:"status"`
	AttemptCount   int        `json:"attempt_count"`
	Output         string     `json:"output"`
	Error          string     `json:"error"`
	SessionID      string     `json:"session_id,omitempty"`
	ClaimedBy      string     `json:"claimed_by,omitempty"`
	ClaimExpiresAt *time.Time `json:"claim_expires_at,omitempty"`
}

// LLMProviderRecord LLM 提供商配置记录
type LLMProviderRecord struct {
	Name         string `json:"name"`
	ProviderType string `json:"provider_type"` // "openai"/"deepseek"/"anthropic"/"google"/"azure"/"groq"/"mistral"/"xai"/"custom"
	APIKey       string `json:"api_key"`
	BaseURL      string `json:"base_url"`
	IsDefault    bool   `json:"is_default"`
	Enabled      bool   `json:"enabled"`
	ConfigJSON   string `json:"config_json"`  // 提供商特有配置（Azure endpoint、AWS region 等）
	APIFormat    string `json:"api_format"`   // "chat"（Chat Completions）或 "responses"（Responses API），默认 "chat"
	ServiceType  string `json:"service_type"` // "llm"/"image_gen"/"video_gen"/"tts"/"stt"/"embedding"，默认 "llm"
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// LLMModelRecord LLM 模型配置记录
type LLMModelRecord struct {
	Name         string `json:"name"`
	ProviderName string `json:"provider_name"` // 关联 llm_providers.name
	Model        string `json:"model"`         // 发送到 API 的模型 ID
	BaseURL      string `json:"base_url"`      // 可覆盖提供商的 base_url
	APIKey       string `json:"api_key"`       // 可覆盖提供商的 api_key
	IsDefault    bool   `json:"is_default"`
	Enabled      bool   `json:"enabled"`
	ServiceType  string `json:"service_type"` // 服务类型，空字符串表示继承 provider 的 service_type
	ConfigJSON   string `json:"config_json"`  // 模型特有配置（reasoning_effort 等）
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// MCPServerRecord MCP 服务端配置记录
type MCPServerRecord struct {
	Name      string    `json:"name"`
	Transport string    `json:"transport"` // "stdio" | "sse" | "http"
	Command   string    `json:"command"`
	Args      string    `json:"args"` // JSON 数组
	Env       string    `json:"env"`  // JSON 对象，如 {"VAR":"value"}
	URL       string    `json:"url"`
	Headers   string    `json:"headers"` // JSON 对象
	Timeout   string    `json:"timeout"`
	Enabled   bool      `json:"enabled"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ExternalResourceRecord 外部资源配置记录
type ExternalResourceRecord struct {
	Name        string    `json:"name"`        // 唯一标识，如 "mysql_prod", "grafana_test"
	Type        string    `json:"type"`        // "database", "monitoring", "api", "cache"
	Environment string    `json:"environment"` // "production", "staging", "testing", "development"
	Description string    `json:"description"` // 资源描述
	Connection  string    `json:"connection"`  // 连接命令/连接串
	Endpoint    string    `json:"endpoint"`    // API 端点 URL
	Credentials string    `json:"credentials"` // JSON 对象，凭证信息
	ReadOnly    bool      `json:"read_only"`   // 是否只读
	Enabled     bool      `json:"enabled"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TaskRecord 是持久化的任务记录
type TaskRecord struct {
	ID         string          `json:"id"`
	Request    string          `json:"request"`
	Status     string          `json:"status"`
	PlanJSON   json.RawMessage `json:"plan,omitempty"`
	ResultJSON json.RawMessage `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// MessageRecord 是会话中的对话消息
type MessageRecord struct {
	ID        int64           `json:"id"`
	SessionID string          `json:"session_id"`
	Role      string          `json:"role"`
	Content   string          `json:"content"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// PermissionGrantRecord 持久化的权限授予记录
type PermissionGrantRecord struct {
	ID        int64  `json:"id"`
	Tool      string `json:"tool"`
	Pattern   string `json:"pattern"`
	Action    string `json:"action"` // "allow" 或 "deny"
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"` // 空字符串表示永不过期
}

// OAuthTokenRecord OAuth token 持久化记录
type OAuthTokenRecord struct {
	ID           int64  `json:"id"`
	ServerURL    string `json:"server_url"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Scopes       string `json:"scopes"`
	ExpiresAt    string `json:"expires_at"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// SessionRecord 表示完整的会话记录
type SessionRecord struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
	LastAccessedAt string   `json:"last_accessed_at"`
	MessageCount   int      `json:"message_count"`
	TotalTokens    int      `json:"total_tokens"`
	Deleted        bool     `json:"deleted"`
	Tags           []string `json:"tags"`

	// Fork/Revert 支持
	ParentID  string   `json:"parent_id,omitempty"`
	ForkPoint int      `json:"fork_point,omitempty"`
	Children  []string `json:"children,omitempty"`

	// 用户关联（Phase 2 写入，Phase 1 预留字段）
	UserID    string `json:"user_id,omitempty"`
	IsStarred bool   `json:"is_starred,omitempty"` // 新增
}
