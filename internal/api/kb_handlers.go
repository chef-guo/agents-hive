package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/fileconv"
	"github.com/chef-guo/agents-hive/internal/kb"
	"go.uber.org/zap"
)

const maxKBMarkdownBytes = 2 * 1024 * 1024
const maxKBIngestRequestBytes = 32 * 1024 * 1024

type kbManagementService interface {
	CreateNamespace(ctx context.Context, scope kb.ManagementScope, input kb.CreateNamespaceInput) (*kb.Namespace, error)
	ListNamespaces(ctx context.Context, scope kb.ManagementScope, input kb.ListNamespacesInput) ([]kb.Namespace, error)
	ListDocumentsForManagement(ctx context.Context, scope kb.ManagementScope, input kb.ListDocumentsInput) ([]kb.Document, error)
	DocumentTreeForManagement(ctx context.Context, scope kb.ManagementScope, documentID string, includeText bool) ([]kb.TreeNode, *kb.Document, error)
	ArchiveDocument(ctx context.Context, scope kb.ManagementScope, documentID string) error
	CreateBinding(ctx context.Context, scope kb.ManagementScope, input kb.CreateBindingInput) (*kb.Binding, error)
	ListBindingsForManagement(ctx context.Context, scope kb.ManagementScope, query kb.BindingQuery) ([]kb.Binding, error)
	UpdateBinding(ctx context.Context, scope kb.ManagementScope, bindingID string, input kb.UpdateBindingInput) (*kb.Binding, error)
	DisableBinding(ctx context.Context, scope kb.ManagementScope, bindingID string) (*kb.Binding, error)
	ActiveBindingHint(ctx context.Context, input kb.ActiveBindingHintInput) (string, bool, error)
	EffectiveBindings(ctx context.Context, input kb.BindingResolveInput) ([]kb.EffectiveBinding, error)
	DocMeta(ctx context.Context, scope kb.Scope, input kb.DocMetaInput) (*kb.DocMetaResult, error)
	DocStructure(ctx context.Context, scope kb.Scope, input kb.DocStructureInput) (*kb.DocStructureResult, error)
	SectionText(ctx context.Context, scope kb.Scope, evidence kb.EvidenceScope, input kb.SectionTextInput) (*kb.SectionTextResult, error)
	IngestMarkdown(ctx context.Context, scope kb.Scope, input kb.IngestMarkdownInput) (*kb.Document, error)
	IngestMarkdownWithAssets(ctx context.Context, scope kb.Scope, input kb.IngestMarkdownWithAssetsInput) (*kb.Document, error)
	ListNodeAssets(ctx context.Context, scope kb.ManagementScope, documentID string, nodeIDs []string) ([]kb.NodeAsset, error)
	CurrentTurnEvidence(ctx context.Context, scope kb.EvidenceScope) ([]kb.EvidenceRef, error)
}

type kbNamespaceResponse struct {
	ID                     string        `json:"id"`
	Name                   string        `json:"name"`
	DomainID               string        `json:"domain_id"`
	OwnerScope             kb.OwnerScope `json:"owner_scope"`
	OwnerID                string        `json:"owner_id"`
	IndexStrategy          string        `json:"index_strategy"`
	ThinningEnabled        bool          `json:"thinning_enabled"`
	ThinningTokenThreshold int           `json:"thinning_token_threshold"`
	SummaryTokenThreshold  int           `json:"summary_token_threshold"`
	SummaryModel           string        `json:"summary_model,omitempty"`
	CreatedAt              time.Time     `json:"created_at"`
	UpdatedAt              time.Time     `json:"updated_at"`
}

type kbDocumentResponse struct {
	ID          string            `json:"id"`
	NamespaceID string            `json:"namespace_id"`
	Title       string            `json:"title"`
	Version     string            `json:"version"`
	Status      kb.DocumentStatus `json:"status"`
	Description string            `json:"description,omitempty"`
	SourceURI   string            `json:"source_uri,omitempty"`
	EffectiveAt time.Time         `json:"effective_at"`
	ExpiresAt   *time.Time        `json:"expires_at,omitempty"`
}

type kbNodeResponse struct {
	DocumentID  string `json:"doc_id"`
	NamespaceID string `json:"namespace_id"`
	Node        any    `json:"node"`
}

type kbMarkdownPreviewAssetResponse struct {
	Path     string `json:"path"`
	Filename string `json:"filename,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	AltText  string `json:"alt_text,omitempty"`
	Caption  string `json:"caption,omitempty"`
	Size     int    `json:"size"`
	DataURL  string `json:"data_url,omitempty"`
}

type kbMarkdownPreviewResponse struct {
	Title    string                           `json:"title,omitempty"`
	Markdown string                           `json:"markdown"`
	Assets   []kbMarkdownPreviewAssetResponse `json:"assets,omitempty"`
	Quality  string                           `json:"quality,omitempty"`
	Provider string                           `json:"provider,omitempty"`
	Warnings []string                         `json:"warnings,omitempty"`
}

type kbIngestReport struct {
	IngestID        string                 `json:"ingest_id"`
	NamespaceID     string                 `json:"namespace_id"`
	DocumentID      string                 `json:"document_id,omitempty"`
	Title           string                 `json:"title,omitempty"`
	Version         string                 `json:"version,omitempty"`
	SourceFilename  string                 `json:"source_filename,omitempty"`
	ContentBytes    int                    `json:"content_bytes"`
	MarkdownLines   int                    `json:"markdown_lines"`
	Converted       bool                   `json:"converted"`
	Provider        string                 `json:"provider,omitempty"`
	Quality         string                 `json:"quality,omitempty"`
	UploadedAssets  int                    `json:"uploaded_assets"`
	ConvertedAssets int                    `json:"converted_assets"`
	ImageRefs       int                    `json:"image_refs"`
	TreeNodes       int                    `json:"tree_nodes"`
	BoundAssets     int                    `json:"bound_assets"`
	UnboundAssets   int                    `json:"unbound_assets"`
	DurationMs      int64                  `json:"duration_ms"`
	Warnings        []string               `json:"warnings,omitempty"`
	Stages          []kbStageEvent         `json:"stages"`
	AssetBindings   []kbAssetBindingReport `json:"asset_bindings,omitempty"`
}

type kbStageEvent struct {
	Name       string         `json:"name"`
	Status     string         `json:"status"`
	DurationMs int64          `json:"duration_ms"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

type kbAssetBindingReport struct {
	AssetURI    string `json:"asset_uri"`
	NodeID      string `json:"node_id,omitempty"`
	Line        int    `json:"line,omitempty"`
	Page        int    `json:"page,omitempty"`
	NodePath    string `json:"node_path,omitempty"`
	NodeTitle   string `json:"node_title,omitempty"`
	AltText     string `json:"alt_text,omitempty"`
	Caption     string `json:"caption,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
	MimeType    string `json:"mime_type,omitempty"`
	Bound       bool   `json:"bound"`
}

type kbIngestTracker struct {
	id        string
	startedAt time.Time
	logger    *zap.Logger
	stages    []kbStageEvent
}

func (s *Server) handleKBListNamespaces(w http.ResponseWriter, r *http.Request) {
	if !s.requireKBService(w) {
		return
	}
	scope, ok := kbManagementScope(w, r)
	if !ok {
		return
	}
	items, err := s.kbService.ListNamespaces(r.Context(), scope, kb.ListNamespacesInput{
		Query: strings.TrimSpace(r.URL.Query().Get("query")),
		Limit: parsePositiveInt(r.URL.Query().Get("limit"), 100),
	})
	if err != nil {
		s.writeKBError(w, err)
		return
	}
	out := make([]kbNamespaceResponse, 0, len(items))
	for _, namespace := range items {
		out = append(out, kbNamespaceToResponse(namespace))
	}
	writeJSON(w, http.StatusOK, map[string]any{"namespaces": out})
}

func (s *Server) handleKBCreateNamespace(w http.ResponseWriter, r *http.Request) {
	if !s.requireKBService(w) {
		return
	}
	scope, ok := kbManagementScope(w, r)
	if !ok {
		return
	}
	var req kb.CreateNamespaceInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "无效的请求体", Code: errs.CodeBadRequest})
		return
	}
	if strings.TrimSpace(req.DomainID) == "" {
		req.DomainID = scope.DomainID
	}
	namespace, err := s.kbService.CreateNamespace(r.Context(), scope, req)
	if err != nil {
		s.writeKBError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, kbNamespaceToResponse(*namespace))
}

func (s *Server) handleKBListDocuments(w http.ResponseWriter, r *http.Request) {
	if !s.requireKBService(w) {
		return
	}
	namespaceID := strings.TrimSpace(r.PathValue("id"))
	if namespaceID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "需要 namespace ID", Code: errs.CodeBadRequest})
		return
	}
	scope, ok := kbManagementScope(w, r)
	if !ok {
		return
	}
	docs, err := s.kbService.ListDocumentsForManagement(r.Context(), scope, kb.ListDocumentsInput{
		NamespaceID: namespaceID,
		Query:       strings.TrimSpace(r.URL.Query().Get("query")),
		Status:      kb.DocumentStatus(strings.TrimSpace(r.URL.Query().Get("status"))),
		Limit:       parsePositiveInt(r.URL.Query().Get("limit"), 100),
	})
	if err != nil {
		s.writeKBError(w, err)
		return
	}
	items := make([]kbDocumentResponse, 0, len(docs))
	for _, doc := range docs {
		items = append(items, kbDocumentToResponse(doc))
	}
	writeJSON(w, http.StatusOK, map[string]any{"documents": items})
}

func (s *Server) handleKBIngestMarkdown(w http.ResponseWriter, r *http.Request) {
	if !s.requireKBService(w) {
		return
	}
	namespaceID := strings.TrimSpace(r.PathValue("id"))
	if namespaceID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "需要 namespace ID", Code: errs.CodeBadRequest})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxKBIngestRequestBytes)
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type"))), "multipart/form-data") {
		writeJSON(w, http.StatusUnsupportedMediaType, ErrorResponse{Error: "KB 文档导入只接受 multipart/form-data", Code: errs.CodeBadRequest})
		return
	}
	s.handleKBIngestMarkdownMultipart(w, r, namespaceID)
}

func (s *Server) handleKBPreviewMarkdown(w http.ResponseWriter, r *http.Request) {
	if !s.requireKBService(w) {
		return
	}
	namespaceID := strings.TrimSpace(r.PathValue("id"))
	if namespaceID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "需要 namespace ID", Code: errs.CodeBadRequest})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxKBIngestRequestBytes)
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type"))), "multipart/form-data") {
		writeJSON(w, http.StatusUnsupportedMediaType, ErrorResponse{Error: "KB 文档预览只接受 multipart/form-data", Code: errs.CodeBadRequest})
		return
	}
	if err := r.ParseMultipartForm(maxKBIngestRequestBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "无效的 multipart 请求: " + err.Error(), Code: errs.CodeBadRequest})
		return
	}
	form := r.MultipartForm
	if form == nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "缺少 multipart 表单", Code: errs.CodeBadRequest})
		return
	}
	defer func() { _ = form.RemoveAll() }()
	files := form.File["file"]
	if len(files) != 1 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "预览需要且只能上传一个文档文件", Code: errs.CodeBadRequest})
		return
	}
	fileData, err := readMultipartFile(files[0])
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "读取文档文件失败: " + err.Error(), Code: errs.CodeBadRequest})
		return
	}
	converted, err := s.convertMultipartFileToMarkdown(r.Context(), files[0], fileData)
	if err != nil {
		if errors.Is(err, fileconv.ErrMarkdownProviderUnavailable) {
			writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: err.Error(), Code: errs.CodeUnavailable})
			return
		}
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error(), Code: errs.CodeBadRequest})
		return
	}
	if len([]byte(converted.Content)) > maxKBMarkdownBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, ErrorResponse{Error: "转换后的 markdown 超过 2MB", Code: errs.CodeBadRequest})
		return
	}
	writeJSON(w, http.StatusOK, kbMarkdownPreviewToResponse(converted))
}

func (s *Server) handleKBIngestMarkdownMultipart(w http.ResponseWriter, r *http.Request, namespaceID string) {
	tracker := newKBIngestTracker(s.logger, namespaceID)
	report := kbIngestReport{
		IngestID:    tracker.id,
		NamespaceID: namespaceID,
	}
	defer func(start time.Time) {
		report.DurationMs = time.Since(start).Milliseconds()
		report.Stages = append([]kbStageEvent{}, tracker.stages...)
		tracker.finish(report)
	}(tracker.startedAt)

	parseStarted := time.Now()
	if err := r.ParseMultipartForm(maxKBIngestRequestBytes); err != nil {
		tracker.stage("parse_multipart", "fail", parseStarted, map[string]any{"error": err.Error()})
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "无效的 multipart 请求: " + err.Error(), Code: errs.CodeBadRequest})
		return
	}
	tracker.stage("parse_multipart", "ok", parseStarted, nil)
	form := r.MultipartForm
	if form == nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "缺少 multipart 表单", Code: errs.CodeBadRequest})
		return
	}
	defer func() { _ = form.RemoveAll() }()
	content := firstMultipartValue(form.Value, "markdown")
	if strings.TrimSpace(content) == "" {
		content = firstMultipartValue(form.Value, "content")
	}
	var ingestAssets map[string]kb.MarkdownAsset
	var warnings []string
	var provider string
	var quality string
	if files := form.File["file"]; len(files) > 0 {
		if len(files) > 1 {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "文档文件只能上传一个", Code: errs.CodeBadRequest})
			return
		}
		fileData, err := readMultipartFile(files[0])
		if err != nil {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "读取文档文件失败: " + err.Error(), Code: errs.CodeBadRequest})
			return
		}
		report.SourceFilename = files[0].Filename
		convertStarted := time.Now()
		converted, err := s.convertMultipartFileToMarkdown(r.Context(), files[0], fileData)
		if err != nil {
			tracker.stage("convert_file", "fail", convertStarted, map[string]any{"filename": files[0].Filename, "error": err.Error()})
			if errors.Is(err, fileconv.ErrMarkdownProviderUnavailable) {
				writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: err.Error(), Code: errs.CodeUnavailable})
				return
			}
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error(), Code: errs.CodeBadRequest})
			return
		}
		tracker.stage("convert_file", "ok", convertStarted, map[string]any{"filename": files[0].Filename, "assets": len(converted.Assets), "provider": converted.Provider, "quality": string(converted.Quality)})
		report.Converted = true
		report.ConvertedAssets = len(converted.Assets)
		provider = converted.Provider
		quality = string(converted.Quality)
		if strings.TrimSpace(content) == "" {
			content = converted.Content
		}
		warnings = converted.Warnings
		if strings.TrimSpace(firstMultipartValue(form.Value, "title")) == "" {
			form.Value["title"] = []string{converted.Title}
		}
		ingestAssets = kbAssetsFromConverted(converted)
	}
	extraAssets, err := kbAssetsFromMultipart(form)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error(), Code: errs.CodeBadRequest})
		return
	}
	report.UploadedAssets = len(extraAssets)
	if len(extraAssets) > 0 {
		if ingestAssets == nil {
			ingestAssets = make(map[string]kb.MarkdownAsset, len(extraAssets))
		}
		for path, asset := range extraAssets {
			ingestAssets[path] = asset
		}
	}
	if len([]byte(content)) > maxKBMarkdownBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, ErrorResponse{Error: "markdown 超过 2MB", Code: errs.CodeBadRequest})
		return
	}
	report.ContentBytes = len([]byte(content))
	report.MarkdownLines = markdownLineCount(content)
	report.ImageRefs = countMarkdownImageRefs(content)
	report.Provider = provider
	report.Quality = quality
	report.Warnings = append([]string{}, warnings...)
	mgmtScope, ok := kbManagementScope(w, r)
	if !ok {
		return
	}
	scope := kb.Scope{
		DomainID:     mgmtScope.DomainID,
		OwnerScope:   mgmtScope.OwnerScope,
		OwnerID:      mgmtScope.OwnerID,
		NamespaceIDs: []string{namespaceID},
		Now:          mgmtScope.Now,
	}
	version := strings.TrimSpace(firstMultipartValue(form.Value, "version"))
	if version == "" {
		version = "v1"
	}
	effectiveAt, err := parseOptionalKBTime(firstMultipartValue(form.Value, "effective_at"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "effective_at 必须是 RFC3339 时间", Code: errs.CodeBadRequest})
		return
	}
	expiresAt, err := parseOptionalKBTimePtr(firstMultipartValue(form.Value, "expires_at"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "expires_at 必须是 RFC3339 时间", Code: errs.CodeBadRequest})
		return
	}
	input := kb.IngestMarkdownInput{
		NamespaceID: namespaceID,
		Title:       strings.TrimSpace(firstMultipartValue(form.Value, "title")),
		SourceURI:   strings.TrimSpace(firstMultipartValue(form.Value, "source_uri")),
		Version:     version,
		Content:     content,
		EffectiveAt: effectiveAt,
		ExpiresAt:   expiresAt,
	}
	report.Title = input.Title
	report.Version = input.Version
	var doc *kb.Document
	ingestStarted := time.Now()
	if len(ingestAssets) > 0 || containsIngestibleMarkdownAsset(content) {
		doc, err = s.kbService.IngestMarkdownWithAssets(r.Context(), scope, kb.IngestMarkdownWithAssetsInput{
			IngestMarkdownInput: input,
			Assets:              ingestAssets,
		})
	} else {
		doc, err = s.kbService.IngestMarkdown(r.Context(), scope, input)
	}
	if err != nil {
		tracker.stage("save_document", "fail", ingestStarted, map[string]any{"title": input.Title, "error": err.Error()})
		s.writeKBError(w, err)
		return
	}
	tracker.stage("save_document", "ok", ingestStarted, map[string]any{"doc_id": doc.ID, "title": doc.Title})
	report.DocumentID = doc.ID
	report.Title = doc.Title
	report.Version = doc.Version
	stats := s.buildKBIngestResultStats(r.Context(), mgmtScope, *doc)
	report.TreeNodes = stats.TreeNodes
	report.BoundAssets = stats.BoundAssets
	report.UnboundAssets = stats.UnboundAssets
	report.AssetBindings = stats.AssetBindings
	report.DurationMs = time.Since(tracker.startedAt).Milliseconds()
	report.Stages = append([]kbStageEvent{}, tracker.stages...)
	response := map[string]any{"document": kbDocumentToResponse(*doc), "report": report}
	if len(warnings) > 0 {
		response["warnings"] = warnings
	}
	writeJSON(w, http.StatusCreated, response)
}

func (s *Server) handleKBGetDocumentTree(w http.ResponseWriter, r *http.Request) {
	if !s.requireKBService(w) {
		return
	}
	docID := strings.TrimSpace(r.PathValue("id"))
	if docID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "需要 document ID", Code: errs.CodeBadRequest})
		return
	}
	scope, ok := kbManagementScope(w, r)
	if !ok {
		return
	}
	nodes, doc, err := s.kbService.DocumentTreeForManagement(r.Context(), scope, docID, false)
	if err != nil {
		s.writeKBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, kb.DocStructureResult{
		DocumentID:  doc.ID,
		NamespaceID: doc.NamespaceID,
		Nodes:       buildKBStructureForAPI(nodes),
	})
}

func (s *Server) handleKBGetDocumentNode(w http.ResponseWriter, r *http.Request) {
	if !s.requireKBService(w) {
		return
	}
	docID := strings.TrimSpace(r.PathValue("id"))
	nodeID := strings.TrimSpace(r.PathValue("node_id"))
	if docID == "" || nodeID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "需要 document ID 和 node ID", Code: errs.CodeBadRequest})
		return
	}
	scope, ok := kbManagementScope(w, r)
	if !ok {
		return
	}
	nodes, doc, err := s.kbService.DocumentTreeForManagement(r.Context(), scope, docID, true)
	if err != nil {
		s.writeKBError(w, err)
		return
	}
	for _, node := range nodes {
		if node.ID == nodeID {
			writeJSON(w, http.StatusOK, kbNodeResponse{
				DocumentID:  doc.ID,
				NamespaceID: doc.NamespaceID,
				Node:        node,
			})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "KB 节点不存在", Code: errs.CodeNotFound})
}

func (s *Server) handleKBArchiveDocument(w http.ResponseWriter, r *http.Request) {
	if !s.requireKBService(w) {
		return
	}
	docID := strings.TrimSpace(r.PathValue("id"))
	if !strings.HasSuffix(docID, ":archive") {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "KB 文档操作不存在", Code: errs.CodeNotFound})
		return
	}
	docID = strings.TrimSuffix(docID, ":archive")
	if docID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "需要 document ID", Code: errs.CodeBadRequest})
		return
	}
	scope, ok := kbManagementScope(w, r)
	if !ok {
		return
	}
	if err := s.kbService.ArchiveDocument(r.Context(), scope, docID); err != nil {
		s.writeKBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "archived"})
}

func (s *Server) handleKBListEvidence(w http.ResponseWriter, r *http.Request) {
	if !s.requireKBService(w) {
		return
	}
	_, evidence, ok := kbUserAndEvidenceScope(w, r)
	if !ok {
		return
	}
	refs, err := s.kbService.CurrentTurnEvidence(r.Context(), evidence)
	if err != nil {
		s.writeKBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"evidence": refs})
}

func (s *Server) requireKBService(w http.ResponseWriter) bool {
	if s == nil || s.kbService == nil {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "KB service 未初始化", Code: errs.CodeInternal})
		return false
	}
	return true
}

func kbUserScope(w http.ResponseWriter, r *http.Request) (kb.Scope, bool) {
	user := auth.UserFrom(r.Context())
	if user == nil || strings.TrimSpace(user.ID) == "" {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "未授权", Code: errs.CodePermissionDenied})
		return kb.Scope{}, false
	}
	domainID := strings.TrimSpace(r.URL.Query().Get("domain_id"))
	if domainID == "" {
		domainID = "generic"
	}
	namespaceIDs := strings.Split(strings.TrimSpace(r.URL.Query().Get("namespace_ids")), ",")
	if namespaceID := strings.TrimSpace(r.PathValue("id")); namespaceID != "" {
		namespaceIDs = append(namespaceIDs, namespaceID)
	}
	if namespaceID := strings.TrimSpace(r.URL.Query().Get("namespace_id")); namespaceID != "" {
		namespaceIDs = append(namespaceIDs, namespaceID)
	}
	return kb.Scope{
		DomainID:     domainID,
		OwnerScope:   kb.OwnerScopeUser,
		OwnerID:      strings.TrimSpace(user.ID),
		NamespaceIDs: uniqueKBStrings(namespaceIDs),
		Now:          time.Now(),
	}, true
}

func kbManagementScope(w http.ResponseWriter, r *http.Request) (kb.ManagementScope, bool) {
	user := auth.UserFrom(r.Context())
	if user == nil || strings.TrimSpace(user.ID) == "" {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "未授权", Code: errs.CodePermissionDenied})
		return kb.ManagementScope{}, false
	}
	domainID := strings.TrimSpace(r.URL.Query().Get("domain_id"))
	if domainID == "" {
		domainID = "generic"
	}
	return kb.ManagementScope{
		DomainID:   domainID,
		OwnerScope: kb.OwnerScopeUser,
		OwnerID:    strings.TrimSpace(user.ID),
		Now:        time.Now(),
	}, true
}

func kbUserAndEvidenceScope(w http.ResponseWriter, r *http.Request) (kb.Scope, kb.EvidenceScope, bool) {
	scope, ok := kbUserScope(w, r)
	if !ok {
		return kb.Scope{}, kb.EvidenceScope{}, false
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	turnID := strings.TrimSpace(r.URL.Query().Get("turn_id"))
	traceID := strings.TrimSpace(r.URL.Query().Get("trace_id"))
	if sessionID == "" || turnID == "" || traceID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "需要 session_id、turn_id、trace_id", Code: errs.CodeBadRequest})
		return kb.Scope{}, kb.EvidenceScope{}, false
	}
	return scope, kb.EvidenceScope{
		SessionID:  sessionID,
		TurnID:     turnID,
		TraceID:    traceID,
		ToolCallID: strings.TrimSpace(r.URL.Query().Get("tool_call_id")),
		DomainID:   scope.DomainID,
		OwnerScope: scope.OwnerScope,
		OwnerID:    scope.OwnerID,
		Now:        scope.Now,
	}, true
}

func (s *Server) writeKBError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	code := errs.CodeInternal
	message := err.Error()
	switch {
	case errors.Is(err, kb.ErrUnsupportedAsset):
		status = http.StatusUnprocessableEntity
		code = errs.CodeBadRequest
	case errors.Is(err, kb.ErrInvalidInput), errors.Is(err, kb.ErrInvalidScope), errors.Is(err, kb.ErrEmptyDocument), errors.Is(err, kb.ErrNoHeading), errors.Is(err, kb.ErrEmptyHeading), errors.Is(err, kb.ErrNoKBBinding), errors.Is(err, kb.ErrNamespaceNotBound):
		status = http.StatusBadRequest
		code = errs.CodeBadRequest
	case errors.Is(err, kb.ErrNotFound):
		status = http.StatusNotFound
		code = errs.CodeNotFound
	case errors.Is(err, kb.ErrOutputTooLarge):
		status = http.StatusRequestEntityTooLarge
		code = errs.CodeBadRequest
	}
	writeJSON(w, status, ErrorResponse{Error: message, Code: code})
}

func kbNamespaceToResponse(namespace kb.Namespace) kbNamespaceResponse {
	return kbNamespaceResponse{
		ID:                     namespace.ID,
		Name:                   namespace.Name,
		DomainID:               namespace.DomainID,
		OwnerScope:             namespace.OwnerScope,
		OwnerID:                namespace.OwnerID,
		IndexStrategy:          namespace.IndexStrategy,
		ThinningEnabled:        namespace.ThinningEnabled,
		ThinningTokenThreshold: namespace.ThinningTokenThreshold,
		SummaryTokenThreshold:  namespace.SummaryTokenThreshold,
		SummaryModel:           namespace.SummaryModel,
		CreatedAt:              namespace.CreatedAt,
		UpdatedAt:              namespace.UpdatedAt,
	}
}

func kbDocumentToResponse(doc kb.Document) kbDocumentResponse {
	return kbDocumentResponse{
		ID:          doc.ID,
		NamespaceID: doc.NamespaceID,
		Title:       doc.Title,
		Version:     doc.Version,
		Status:      doc.Status,
		Description: doc.Description,
		SourceURI:   doc.SourceURI,
		EffectiveAt: doc.EffectiveAt,
		ExpiresAt:   doc.ExpiresAt,
	}
}

func kbMarkdownPreviewToResponse(doc *fileconv.MarkdownDocument) kbMarkdownPreviewResponse {
	if doc == nil {
		return kbMarkdownPreviewResponse{}
	}
	assets := make([]kbMarkdownPreviewAssetResponse, 0, len(doc.Assets))
	for _, asset := range doc.Assets {
		mimeType := strings.TrimSpace(asset.MimeType)
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		assets = append(assets, kbMarkdownPreviewAssetResponse{
			Path:     strings.TrimSpace(asset.Path),
			Filename: strings.TrimSpace(asset.Filename),
			MimeType: mimeType,
			AltText:  strings.TrimSpace(asset.AltText),
			Caption:  strings.TrimSpace(asset.Caption),
			Size:     len(asset.Data),
			DataURL:  "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(asset.Data),
		})
	}
	return kbMarkdownPreviewResponse{
		Title:    strings.TrimSpace(doc.Title),
		Markdown: doc.Content,
		Assets:   assets,
		Quality:  string(doc.Quality),
		Provider: strings.TrimSpace(doc.Provider),
		Warnings: doc.Warnings,
	}
}

type kbIngestResultStats struct {
	TreeNodes     int
	BoundAssets   int
	UnboundAssets int
	AssetBindings []kbAssetBindingReport
}

func (s *Server) buildKBIngestResultStats(ctx context.Context, scope kb.ManagementScope, doc kb.Document) kbIngestResultStats {
	stats := kbIngestResultStats{}
	if s == nil || s.kbService == nil {
		return stats
	}
	nodes, _, err := s.kbService.DocumentTreeForManagement(ctx, scope, doc.ID, false)
	if err == nil {
		stats.TreeNodes = len(nodes)
	}
	assets, err := s.kbService.ListNodeAssets(ctx, scope, doc.ID, nil)
	if err != nil {
		return stats
	}
	nodeByID := make(map[string]kb.TreeNode, len(nodes))
	for _, node := range nodes {
		nodeByID[node.ID] = node
	}
	stats.AssetBindings = make([]kbAssetBindingReport, 0, len(assets))
	for _, asset := range assets {
		item := kbAssetBindingReport{
			AssetURI:    strings.TrimSpace(asset.AssetURI),
			NodeID:      strings.TrimSpace(asset.NodeID),
			Line:        asset.Line,
			Page:        asset.Page,
			AltText:     strings.TrimSpace(asset.AltText),
			Caption:     strings.TrimSpace(asset.Caption),
			ContentHash: strings.TrimSpace(asset.ContentHash),
			MimeType:    strings.TrimSpace(asset.MimeType),
			Bound:       strings.TrimSpace(asset.NodeID) != "",
		}
		if node, ok := nodeByID[item.NodeID]; ok {
			item.NodePath = strings.TrimSpace(node.NodePath)
			item.NodeTitle = strings.TrimSpace(node.Title)
		}
		stats.AssetBindings = append(stats.AssetBindings, item)
		if strings.TrimSpace(asset.NodeID) == "" {
			stats.UnboundAssets++
			continue
		}
		stats.BoundAssets++
	}
	return stats
}

func newKBIngestTracker(logger *zap.Logger, namespaceID string) *kbIngestTracker {
	if logger == nil {
		logger = zap.NewNop()
	}
	id := newShortID("kb_ing_")
	tracker := &kbIngestTracker{
		id:        id,
		startedAt: time.Now(),
		logger:    logger,
	}
	logger.Info("KB 文档导入开始",
		zap.String("ingest_id", id),
		zap.String("namespace_id", namespaceID))
	return tracker
}

func (t *kbIngestTracker) stage(name, status string, startedAt time.Time, attrs map[string]any) {
	if t == nil {
		return
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	event := kbStageEvent{
		Name:       name,
		Status:     status,
		DurationMs: time.Since(startedAt).Milliseconds(),
		Attributes: attrs,
	}
	t.stages = append(t.stages, event)
	fields := []zap.Field{
		zap.String("ingest_id", t.id),
		zap.String("stage", name),
		zap.String("status", status),
		zap.Int64("duration_ms", event.DurationMs),
	}
	for key, value := range attrs {
		fields = append(fields, zap.Any(key, value))
	}
	if status == "fail" {
		t.logger.Warn("KB 文档导入阶段失败", fields...)
		return
	}
	t.logger.Info("KB 文档导入阶段完成", fields...)
}

func (t *kbIngestTracker) finish(report kbIngestReport) {
	if t == nil {
		return
	}
	t.logger.Info("KB 文档导入结束",
		zap.String("ingest_id", report.IngestID),
		zap.String("namespace_id", report.NamespaceID),
		zap.String("doc_id", report.DocumentID),
		zap.Int("tree_nodes", report.TreeNodes),
		zap.Int("image_refs", report.ImageRefs),
		zap.Int("bound_assets", report.BoundAssets),
		zap.Int("unbound_assets", report.UnboundAssets),
		zap.Int64("duration_ms", report.DurationMs))
}

func newShortID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return prefix + fmt.Sprintf("%x", b[:])
}

func markdownLineCount(content string) int {
	if content == "" {
		return 0
	}
	return strings.Count(content, "\n") + 1
}

func countMarkdownImageRefs(content string) int {
	count := 0
	inFence := false
	fenceMarker := ""
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[:3]
			if !inFence {
				inFence = true
				fenceMarker = marker
			} else if marker == fenceMarker {
				inFence = false
				fenceMarker = ""
			}
			continue
		}
		if inFence {
			continue
		}
		count += countMarkdownImagesInLine(line)
	}
	return count
}

func countMarkdownImagesInLine(line string) int {
	count := 0
	for offset := 0; offset < len(line); {
		start := strings.Index(line[offset:], "![")
		if start < 0 {
			break
		}
		start += offset
		closingAlt := strings.Index(line[start+2:], "](")
		if closingAlt < 0 {
			break
		}
		uriStart := start + 2 + closingAlt + len("](")
		uriEnd := strings.Index(line[uriStart:], ")")
		if uriEnd < 0 {
			break
		}
		target := strings.TrimSpace(line[uriStart : uriStart+uriEnd])
		if target != "" {
			count++
		}
		offset = uriStart + uriEnd + 1
	}
	return count
}

func buildKBStructureForAPI(nodes []kb.TreeNode) []kb.StructureNode {
	itemsByID := make(map[string]kb.StructureNode, len(nodes))
	childrenByID := make(map[string][]string, len(nodes))
	rootIDs := make([]string, 0)
	for _, node := range nodes {
		itemsByID[node.ID] = kb.StructureNode{
			ID:            node.ID,
			ParentNodeID:  node.ParentNodeID,
			NodePath:      node.NodePath,
			Title:         node.Title,
			Level:         node.Level,
			TokenCount:    node.TokenCount,
			Summary:       node.Summary,
			PrefixSummary: node.PrefixSummary,
			StartLine:     node.StartLine,
			EndLine:       node.EndLine,
			StartPage:     node.StartPage,
			EndPage:       node.EndPage,
		}
		if node.ParentNodeID == nil {
			rootIDs = append(rootIDs, node.ID)
			continue
		}
		childrenByID[*node.ParentNodeID] = append(childrenByID[*node.ParentNodeID], node.ID)
	}
	var build func(id string) kb.StructureNode
	build = func(id string) kb.StructureNode {
		item := itemsByID[id]
		for _, childID := range childrenByID[id] {
			child := build(childID)
			item.Children = append(item.Children, child)
			if child.StartPage > 0 && (item.StartPage == 0 || child.StartPage < item.StartPage) {
				item.StartPage = child.StartPage
			}
			if child.EndPage > item.EndPage {
				item.EndPage = child.EndPage
			}
		}
		return item
	}
	roots := make([]kb.StructureNode, 0, len(rootIDs))
	for _, id := range rootIDs {
		roots = append(roots, build(id))
	}
	return roots
}

func parsePositiveInt(raw string, fallback int) int {
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(raw), "%d", &n); err != nil || n <= 0 {
		return fallback
	}
	return n
}

func uniqueKBStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func containsIngestibleMarkdownAsset(content string) bool {
	inFence := false
	fenceMarker := ""
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[:3]
			if !inFence {
				inFence = true
				fenceMarker = marker
			} else if marker == fenceMarker {
				inFence = false
				fenceMarker = ""
			}
			continue
		}
		if inFence {
			continue
		}
		for offset := 0; offset < len(line); {
			start := strings.Index(line[offset:], "![")
			if start < 0 {
				break
			}
			start += offset
			closingAlt := strings.Index(line[start+2:], "](")
			if closingAlt < 0 {
				break
			}
			uriStart := start + 2 + closingAlt + len("](")
			uriEnd := strings.Index(line[uriStart:], ")")
			if uriEnd < 0 {
				break
			}
			target := strings.TrimSpace(line[uriStart : uriStart+uriEnd])
			if target != "" && !strings.HasPrefix(target, "asset://") {
				return true
			}
			offset = uriStart + uriEnd + 1
		}
	}
	return false
}

func kbAssetsFromConverted(doc *fileconv.MarkdownDocument) map[string]kb.MarkdownAsset {
	if doc == nil || len(doc.Assets) == 0 {
		return nil
	}
	out := make(map[string]kb.MarkdownAsset, len(doc.Assets))
	for _, asset := range doc.Assets {
		path := strings.TrimSpace(asset.Path)
		if path == "" {
			path = strings.TrimSpace(asset.Filename)
		}
		if path == "" {
			continue
		}
		out[path] = kb.MarkdownAsset{
			Path:     path,
			Filename: strings.TrimSpace(asset.Filename),
			MimeType: strings.TrimSpace(asset.MimeType),
			Data:     asset.Data,
			AltText:  strings.TrimSpace(asset.AltText),
			Caption:  strings.TrimSpace(asset.Caption),
		}
	}
	return out
}

func kbAssetsFromMultipart(form *multipart.Form) (map[string]kb.MarkdownAsset, error) {
	if form == nil || len(form.File["assets"]) == 0 {
		return nil, nil
	}
	out := make(map[string]kb.MarkdownAsset, len(form.File["assets"]))
	paths := form.Value["asset_path"]
	altTexts := form.Value["asset_alt_text"]
	captions := form.Value["asset_caption"]
	for i, header := range form.File["assets"] {
		data, err := readMultipartFile(header)
		if err != nil {
			return nil, fmt.Errorf("asset #%d 读取失败: %w", i+1, err)
		}
		path := strings.TrimSpace(valueAt(paths, i))
		if path == "" {
			path = strings.TrimSpace(header.Filename)
		}
		if path == "" {
			return nil, fmt.Errorf("asset #%d 缺少文件名", i+1)
		}
		out[path] = kb.MarkdownAsset{
			Path:     path,
			Filename: strings.TrimSpace(header.Filename),
			MimeType: multipartFileMime(header, "application/octet-stream"),
			Data:     data,
			AltText:  strings.TrimSpace(valueAt(altTexts, i)),
			Caption:  strings.TrimSpace(valueAt(captions, i)),
		}
	}
	return out, nil
}

func firstMultipartValue(values map[string][]string, key string) string {
	return valueAt(values[key], 0)
}

func valueAt(values []string, index int) string {
	if index < 0 || index >= len(values) {
		return ""
	}
	return values[index]
}

func (s *Server) convertMultipartFileToMarkdown(ctx context.Context, header *multipart.FileHeader, data []byte) (*fileconv.MarkdownDocument, error) {
	if header == nil {
		return nil, errs.New(errs.CodeInvalidInput, "文件为空")
	}
	filename := strings.TrimSpace(header.Filename)
	if filename == "" {
		return nil, errs.New(errs.CodeInvalidInput, "文件名不能为空")
	}
	if len(data) == 0 {
		return nil, errs.New(errs.CodeInvalidInput, "文件数据不能为空")
	}
	registry := s.kbMarkdownRegistry
	if registry == nil {
		registry = fileconv.DefaultMarkdownRegistry()
	}
	return registry.Convert(ctx, fileconv.MarkdownInput{
		Filename: filename,
		MimeType: multipartFileMime(header, "application/octet-stream"),
		Data:     data,
	})
}

func readMultipartFile(header *multipart.FileHeader) ([]byte, error) {
	if header == nil {
		return nil, fmt.Errorf("文件为空")
	}
	file, err := header.Open()
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

func multipartFileMime(header *multipart.FileHeader, fallback string) string {
	if header == nil {
		return fallback
	}
	if value := strings.TrimSpace(header.Header.Get("Content-Type")); value != "" {
		if !strings.EqualFold(value, "application/octet-stream") {
			return value
		}
	}
	if value := mimeTypeFromFilename(header.Filename); value != "" {
		return value
	}
	return fallback
}

func mimeTypeFromFilename(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".md", ".markdown":
		return "text/markdown"
	case ".txt":
		return "text/plain"
	case ".pdf":
		return "application/pdf"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	}
	return mime.TypeByExtension(filepath.Ext(filename))
}
