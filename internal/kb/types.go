package kb

import (
	"context"
	"time"
)

type OwnerScope string

const (
	OwnerScopeUser   OwnerScope = "user"
	OwnerScopeTenant OwnerScope = "tenant"
	OwnerScopeSystem OwnerScope = "system"
)

type DocumentStatus string

const (
	DocumentDraft    DocumentStatus = "draft"
	DocumentActive   DocumentStatus = "active"
	DocumentArchived DocumentStatus = "archived"
	DocumentRevoked  DocumentStatus = "revoked"
)

type Namespace struct {
	ID                     string
	Name                   string
	DomainID               string
	OwnerScope             OwnerScope
	OwnerID                string
	IndexStrategy          string
	ThinningEnabled        bool
	ThinningTokenThreshold int
	SummaryTokenThreshold  int
	SummaryModel           string
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

type Document struct {
	ID          string
	NamespaceID string
	DomainID    string
	OwnerScope  OwnerScope
	OwnerID     string
	SourceURI   string
	Title       string
	Description string
	ContentHash string
	Version     string
	Status      DocumentStatus
	EffectiveAt time.Time
	ExpiresAt   *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type TreeNode struct {
	ID            string
	DocumentID    string
	NamespaceID   string
	DomainID      string
	OwnerScope    OwnerScope
	OwnerID       string
	ParentNodeID  *string
	NodePath      string
	Title         string
	Level         int
	Text          string
	TokenCount    int
	Summary       string
	PrefixSummary string
	StartLine     int
	EndLine       int
	StartPage     int
	EndPage       int
	ContentHash   string
	CreatedAt     time.Time
}

type Scope struct {
	DomainID           string
	OwnerScope         OwnerScope
	OwnerID            string
	NamespaceIDs       []string
	NamespaceNarrowing string
	Now                time.Time
}

type ManagementScope struct {
	DomainID   string
	OwnerScope OwnerScope
	OwnerID    string
	Now        time.Time
}

type BindingType string

const (
	BindingTypeAgent           BindingType = "agent"
	BindingTypeDomain          BindingType = "domain"
	BindingTypeSessionTemplate BindingType = "session_template"
	BindingTypeSession         BindingType = "session"
	BindingTypeTenant          BindingType = "tenant"
	BindingTypeUser            BindingType = "user"
	BindingTypeSystem          BindingType = "system"
)

type Binding struct {
	ID            string
	DomainID      string
	OwnerScope    OwnerScope
	OwnerID       string
	NamespaceID   string
	BindingType   BindingType
	BindingTarget string
	Enabled       bool
	EffectiveAt   time.Time
	ExpiresAt     *time.Time
	CreatedBy     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type BindingResolveInput struct {
	DomainID          string
	OwnerScope        OwnerScope
	OwnerID           string
	UserID            string
	TenantID          string
	AgentID           string
	SessionTemplateID string
	SessionID         string
	Now               time.Time
}

type EvidenceScope struct {
	SessionID  string
	TurnID     string
	TraceID    string
	ToolCallID string
	DomainID   string
	OwnerScope OwnerScope
	OwnerID    string
	Now        time.Time
}

type EvidenceRef struct {
	Token           string `json:"token"`
	NamespaceID     string `json:"namespace_id"`
	DocumentID      string `json:"doc_id"`
	DocumentVersion string `json:"document_version"`
	NodeID          string `json:"node_id"`
	NodePath        string `json:"node_path"`
	StartPage       int    `json:"start_page,omitempty"`
	EndPage         int    `json:"end_page,omitempty"`
	CitationText    string `json:"citation_text"`
	Verified        bool   `json:"verified"`
}

type EvidenceViolation struct {
	Token  string
	Reason string
}

type EvidenceEvent struct {
	ID              string
	SessionID       string
	TurnID          string
	TraceID         string
	DomainID        string
	NamespaceID     string
	DocumentID      string
	DocumentVersion string
	NodeID          string
	NodePath        string
	StartPage       int
	EndPage         int
	OwnerScope      OwnerScope
	OwnerID         string
	EvidenceToken   string
	CitationText    string
	Verified        bool
	CreatedAt       time.Time
}

type NamespaceQuery struct {
	DomainID   string
	OwnerScope OwnerScope
	OwnerID    string
	Query      string
	Limit      int
}

type DocumentQuery struct {
	Limit  int
	Query  string
	Status DocumentStatus
}

type BindingQuery struct {
	DomainID      string
	OwnerScope    OwnerScope
	OwnerID       string
	NamespaceID   string
	BindingType   BindingType
	BindingTarget string
	Enabled       *bool
}

type NodeAsset struct {
	ID          string
	DomainID    string
	NamespaceID string
	DocumentID  string
	NodeID      string
	Line        int
	Page        int
	OwnerScope  OwnerScope
	OwnerID     string
	AssetURI    string
	ContentHash string
	MimeType    string
	AltText     string
	Caption     string
	CreatedAt   time.Time
}

type AssetRef struct {
	AssetURI    string `json:"asset_uri"`
	NodeID      string `json:"node_id"`
	Line        int    `json:"line,omitempty"`
	Page        int    `json:"page,omitempty"`
	AltText     string `json:"alt_text,omitempty"`
	Caption     string `json:"caption,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
	MimeType    string `json:"mime_type,omitempty"`
}

type StructureNode struct {
	ID            string          `json:"id"`
	ParentNodeID  *string         `json:"parent_node_id,omitempty"`
	NodePath      string          `json:"node_path"`
	Title         string          `json:"title"`
	Level         int             `json:"level"`
	TokenCount    int             `json:"token_count"`
	Summary       string          `json:"summary"`
	PrefixSummary string          `json:"prefix_summary"`
	StartLine     int             `json:"start_line"`
	EndLine       int             `json:"end_line"`
	StartPage     int             `json:"start_page,omitempty"`
	EndPage       int             `json:"end_page,omitempty"`
	Children      []StructureNode `json:"children,omitempty"`
}

type DocMetaInput struct {
	NamespaceID string `json:"namespace_id,omitempty"`
	Query       string `json:"query,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

type DocMetaDocument struct {
	ID          string         `json:"doc_id"`
	NamespaceID string         `json:"namespace_id"`
	Title       string         `json:"title"`
	Version     string         `json:"version"`
	Status      DocumentStatus `json:"status"`
	Description string         `json:"doc_description"`
	SourceURI   string         `json:"source_uri,omitempty"`
	PageCount   int            `json:"page_count,omitempty"`
	LineCount   int            `json:"line_count,omitempty"`
	NodeCount   int            `json:"node_count,omitempty"`
	EffectiveAt time.Time      `json:"effective_at"`
	ExpiresAt   *time.Time     `json:"expires_at,omitempty"`
}

type DocMetaResult struct {
	Documents []DocMetaDocument `json:"documents"`
	NoKBBound bool              `json:"no_kb_bound,omitempty"`
}

type DocStructureInput struct {
	NamespaceID string `json:"namespace_id,omitempty"`
	DocumentID  string `json:"doc_id"`
}

type DocStructureResult struct {
	DocumentID  string          `json:"doc_id"`
	NamespaceID string          `json:"namespace_id"`
	Nodes       []StructureNode `json:"nodes"`
}

type SectionTextInput struct {
	NamespaceID string   `json:"namespace_id,omitempty"`
	DocumentID  string   `json:"doc_id"`
	NodeIDs     []string `json:"node_ids"`
	PageRanges  []string `json:"page_ranges,omitempty"`
}

type SectionTextResult struct {
	DocumentID  string        `json:"doc_id"`
	NamespaceID string        `json:"namespace_id"`
	Sections    []SectionText `json:"sections"`
	Evidence    []EvidenceRef `json:"evidence"`
	AssetRefs   []AssetRef    `json:"asset_refs,omitempty"`
}

type SectionText struct {
	NodeID        string `json:"node_id"`
	NodePath      string `json:"node_path"`
	Title         string `json:"title"`
	Text          string `json:"text"`
	EvidenceToken string `json:"evidence_token"`
	StartLine     int    `json:"start_line,omitempty"`
	EndLine       int    `json:"end_line,omitempty"`
	StartPage     int    `json:"start_page,omitempty"`
	EndPage       int    `json:"end_page,omitempty"`
}

type IngestMarkdownInput struct {
	NamespaceID string
	Title       string
	SourceURI   string
	Version     string
	Content     string
	EffectiveAt time.Time
	ExpiresAt   *time.Time
}

type MarkdownAsset struct {
	Path     string
	Filename string
	MimeType string
	Data     []byte
	AltText  string
	Caption  string
}

type IngestMarkdownWithAssetsInput struct {
	IngestMarkdownInput
	Assets map[string]MarkdownAsset
}

type AssetUploader interface {
	Upload(ctx context.Context, data []byte, opts AssetUploadOptions) (string, string, error)
}

type AssetUploadOptions struct {
	Namespace  string
	Filename   string
	MimeType   string
	OwnerScope string
	OwnerID    string
	Tags       map[string]string
}
