package eval

// Case 描述一条 memory/context 注入回归用例。
type Case struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Query           string          `json:"query"`
	UserID          string          `json:"user_id"`
	SessionID       string          `json:"session_id,omitempty"`
	Target          string          `json:"target,omitempty"`
	Kind            string          `json:"kind,omitempty"`
	SubjectType     string          `json:"subject_type,omitempty"`
	RuntimeContext  RuntimeContext  `json:"runtime_context,omitempty"`
	SkipInjection   bool            `json:"skip_injection,omitempty"`
	ExposeCrossUser bool            `json:"expose_cross_user,omitempty"`
	MinScore        *float64        `json:"min_score,omitempty"`
	Hybrid          *HybridFixture  `json:"hybrid,omitempty"`
	Memories        []MemoryFixture `json:"memories"`
	WantInjectedIDs []int64         `json:"want_injected_ids"`
	WantSkippedIDs  []int64         `json:"want_skipped_ids"`
	WantOrderIDs    []int64         `json:"want_order_ids"`
	WantBatchGetIDs []int64         `json:"want_batch_get_ids,omitempty"`
	WantGetCalls    *int            `json:"want_get_calls,omitempty"`
	WantMetrics     []MetricWant    `json:"want_metrics,omitempty"`
	WantMetadata    []MetadataWant  `json:"want_metadata,omitempty"`
	ScopeAssertions []ScopeWant     `json:"scope_assertions,omitempty"`
	WantSQLContains []string        `json:"want_sql_contains,omitempty"`
	WantSQLArgs     []string        `json:"want_sql_args,omitempty"`
	ForbiddenText   []string        `json:"forbidden_text"`
	Required        bool            `json:"required"`
}

// MemoryFixture 是 eval fixture 中的记忆输入。
type MemoryFixture struct {
	ID              int64    `json:"id"`
	UserID          string   `json:"user_id"`
	Type            string   `json:"type"`
	Content         string   `json:"content"`
	Confidence      float64  `json:"confidence,omitempty"`
	Score           *float64 `json:"score,omitempty"`
	Tags            []string `json:"tags,omitempty"`
	SessionID       string   `json:"session_id,omitempty"`
	Target          string   `json:"target,omitempty"`
	TargetScope     string   `json:"target_scope,omitempty"`
	TargetID        string   `json:"target_id,omitempty"`
	Visibility      string   `json:"visibility,omitempty"`
	TargetUserID    string   `json:"target_user_id,omitempty"`
	TenantID        string   `json:"tenant_id,omitempty"`
	WorkspaceID     string   `json:"workspace_id,omitempty"`
	ProjectID       string   `json:"project_id,omitempty"`
	RepoID          string   `json:"repo_id,omitempty"`
	TargetSessionID string   `json:"target_session_id,omitempty"`
	AgentName       string   `json:"agent_name,omitempty"`
	SkillName       string   `json:"skill_name,omitempty"`
	Kind            string   `json:"kind,omitempty"`
	MemoryKind      string   `json:"memory_kind,omitempty"`
	SubjectType     string   `json:"subject_type,omitempty"`
	ExpiresAt       string   `json:"expires_at,omitempty"`
	Source          string   `json:"source,omitempty"`
}

// RuntimeContext 描述 fixture 中用于 ScopePolicy/SQLFilter 断言的调用上下文。
type RuntimeContext struct {
	UserID       string   `json:"user_id,omitempty"`
	TenantID     string   `json:"tenant_id,omitempty"`
	WorkspaceID  string   `json:"workspace_id,omitempty"`
	ProjectID    string   `json:"project_id,omitempty"`
	RepoID       string   `json:"repo_id,omitempty"`
	SessionID    string   `json:"session_id,omitempty"`
	AgentName    string   `json:"agent_name,omitempty"`
	SkillName    string   `json:"skill_name,omitempty"`
	TaskType     string   `json:"task_type,omitempty"`
	CurrentFiles []string `json:"current_files,omitempty"`
	ToolIntent   string   `json:"tool_intent,omitempty"`
}

// HybridFixture 开启 fixture 内的混合检索路径。
type HybridFixture struct {
	Enabled       bool                  `json:"enabled,omitempty"`
	Embedding     []float64             `json:"embedding,omitempty"`
	EmbedError    string                `json:"embed_error,omitempty"`
	VectorResults []VectorResultFixture `json:"vector_results,omitempty"`
	VectorError   string                `json:"vector_error,omitempty"`
	VectorSpace   string                `json:"vector_space,omitempty"`
}

type VectorResultFixture struct {
	ID    int64   `json:"id"`
	Score float64 `json:"score"`
}

type MetricWant struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
}

type MetadataWant struct {
	MemoryID     int64  `json:"memory_id"`
	TargetScope  string `json:"target_scope,omitempty"`
	TargetID     string `json:"target_id,omitempty"`
	Visibility   string `json:"visibility,omitempty"`
	TargetUserID string `json:"target_user_id,omitempty"`
	WorkspaceID  string `json:"workspace_id,omitempty"`
	ProjectID    string `json:"project_id,omitempty"`
	RepoID       string `json:"repo_id,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	AgentName    string `json:"agent_name,omitempty"`
	SkillName    string `json:"skill_name,omitempty"`
	Kind         string `json:"kind,omitempty"`
	SubjectType  string `json:"subject_type,omitempty"`
	Source       string `json:"source,omitempty"`
}

type ScopeWant struct {
	MemoryID       int64          `json:"memory_id"`
	RuntimeContext RuntimeContext `json:"runtime_context,omitempty"`
	Allowed        bool           `json:"allowed"`
	Reason         string         `json:"reason,omitempty"`
}
