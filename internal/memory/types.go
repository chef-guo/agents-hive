package memory

import (
	"encoding/json"
	"time"
)

// MemoryType 记忆类型
type MemoryType string

const (
	// MemoryTypeUser 用户偏好、角色、知识
	MemoryTypeUser MemoryType = "user"
	// MemoryTypeProject 进行中的工作、目标、决策
	MemoryTypeProject MemoryType = "project"
	// MemoryTypeFeedback 修正建议、已验证的方法
	MemoryTypeFeedback MemoryType = "feedback"
	// MemoryTypeReference 外部系统指针、文档链接
	MemoryTypeReference MemoryType = "reference"
	// MemoryTypeProcedural 可复用流程、工具使用方法、操作手册
	MemoryTypeProcedural MemoryType = "procedural"
	// MemoryTypeEpisodic 具体会话、任务或事件经历
	MemoryTypeEpisodic MemoryType = "episodic"
)

// ValidMemoryTypes 所有合法的记忆类型
var ValidMemoryTypes = map[MemoryType]bool{
	MemoryTypeUser:       true,
	MemoryTypeProject:    true,
	MemoryTypeFeedback:   true,
	MemoryTypeReference:  true,
	MemoryTypeProcedural: true,
	MemoryTypeEpisodic:   true,
}

// MemoryRecord 记忆记录
type MemoryRecord struct {
	ID          int64           `json:"id"`
	UserID      string          `json:"user_id,omitempty"`
	Type        MemoryType      `json:"type"`
	Content     string          `json:"content"`
	Tags        []string        `json:"tags,omitempty"`
	SessionID   string          `json:"session_id,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
	Score       float64         `json:"score,omitempty"` // 搜索相关性得分（仅搜索结果填充）
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	AccessedAt  time.Time       `json:"accessed_at"`
	AccessCount int             `json:"access_count"`
}

// SearchOptions 搜索和列表操作的选项
type SearchOptions struct {
	Query     string     `json:"query,omitempty"`      // FTS5 全文搜索查询（Search 必填）
	UserID    string     `json:"user_id,omitempty"`    // 按用户过滤（非空时严格隔离）
	Type      MemoryType `json:"type,omitempty"`       // 按类型过滤
	Tags      []string   `json:"tags,omitempty"`       // 按标签过滤（AND 逻辑）
	SessionID string     `json:"session_id,omitempty"` // 按来源会话过滤
	Limit     int        `json:"limit,omitempty"`      // 最大返回数量（默认 10）
	MinScore  float64    `json:"min_score,omitempty"`  // 最低相关性阈值（BM25，值越小越相关）
}

// SearchResult 搜索或列表结果
type SearchResult struct {
	Memories []MemoryRecord `json:"memories"`
	Total    int            `json:"total"` // 匹配总数
}

// MemoryStats 记忆统计信息
type MemoryStats struct {
	Total    int            `json:"total"`
	ByType   map[string]int `json:"by_type"`
	OldestAt string         `json:"oldest_at,omitempty"`
	NewestAt string         `json:"newest_at,omitempty"`
}

// ScoredID 带相关性得分的记忆 ID（Phase 2 SearchEngine 接口用）
type ScoredID struct {
	ID    int64
	Score float64
}
