package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/fileconv"
	"github.com/chef-guo/agents-hive/internal/kb"
	"github.com/chef-guo/agents-hive/internal/master"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/subagent"
)

type fakeKBManagementService struct {
	createNamespaceFn func(context.Context, kb.ManagementScope, kb.CreateNamespaceInput) (*kb.Namespace, error)
	listNamespacesFn  func(context.Context, kb.ManagementScope, kb.ListNamespacesInput) ([]kb.Namespace, error)
	listDocsFn        func(context.Context, kb.ManagementScope, kb.ListDocumentsInput) ([]kb.Document, error)
	treeFn            func(context.Context, kb.ManagementScope, string, bool) ([]kb.TreeNode, *kb.Document, error)
	archiveFn         func(context.Context, kb.ManagementScope, string) error
	createBindingFn   func(context.Context, kb.ManagementScope, kb.CreateBindingInput) (*kb.Binding, error)
	listBindingsFn    func(context.Context, kb.ManagementScope, kb.BindingQuery) ([]kb.Binding, error)
	updateBindingFn   func(context.Context, kb.ManagementScope, string, kb.UpdateBindingInput) (*kb.Binding, error)
	disableBindingFn  func(context.Context, kb.ManagementScope, string) (*kb.Binding, error)
	effectiveFn       func(context.Context, kb.BindingResolveInput) ([]kb.EffectiveBinding, error)
	docMetaFn         func(context.Context, kb.Scope, kb.DocMetaInput) (*kb.DocMetaResult, error)
	docStructureFn    func(context.Context, kb.Scope, kb.DocStructureInput) (*kb.DocStructureResult, error)
	sectionTextFn     func(context.Context, kb.Scope, kb.EvidenceScope, kb.SectionTextInput) (*kb.SectionTextResult, error)
	ingestFn          func(context.Context, kb.Scope, kb.IngestMarkdownInput) (*kb.Document, error)
	ingestAssetsFn    func(context.Context, kb.Scope, kb.IngestMarkdownWithAssetsInput) (*kb.Document, error)
	listNodeAssetsFn  func(context.Context, kb.ManagementScope, string, []string) ([]kb.NodeAsset, error)
	evidenceFn        func(context.Context, kb.EvidenceScope) ([]kb.EvidenceRef, error)
	lastMgmtScope     kb.ManagementScope
	lastScope         kb.Scope
	lastEvidence      kb.EvidenceScope
	lastIngestInput   kb.IngestMarkdownInput
	lastAssetsInput   kb.IngestMarkdownWithAssetsInput
}

type fakeMarkdownProvider struct {
	name     string
	supports func(filename, mimeType string) bool
	doc      *fileconv.MarkdownDocument
	err      error
}

func (f fakeMarkdownProvider) Name() string {
	if f.name != "" {
		return f.name
	}
	return "fake-markdown"
}

func (f fakeMarkdownProvider) Supports(filename, mimeType string) bool {
	if f.supports != nil {
		return f.supports(filename, mimeType)
	}
	return true
}

func (f fakeMarkdownProvider) ConvertToMarkdown(context.Context, fileconv.MarkdownInput) (*fileconv.MarkdownDocument, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.doc, nil
}

func (f *fakeKBManagementService) CreateNamespace(ctx context.Context, scope kb.ManagementScope, input kb.CreateNamespaceInput) (*kb.Namespace, error) {
	f.lastMgmtScope = scope
	if f.createNamespaceFn != nil {
		return f.createNamespaceFn(ctx, scope, input)
	}
	return &kb.Namespace{
		ID:            "ns-1",
		Name:          input.Name,
		DomainID:      scope.DomainID,
		OwnerScope:    scope.OwnerScope,
		OwnerID:       scope.OwnerID,
		IndexStrategy: "markdown_tree",
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}, nil
}

func (f *fakeKBManagementService) ListNamespaces(ctx context.Context, scope kb.ManagementScope, input kb.ListNamespacesInput) ([]kb.Namespace, error) {
	f.lastMgmtScope = scope
	if f.listNamespacesFn != nil {
		return f.listNamespacesFn(ctx, scope, input)
	}
	return []kb.Namespace{{ID: "ns-1", Name: "FAQ", DomainID: scope.DomainID, OwnerScope: scope.OwnerScope, OwnerID: scope.OwnerID}}, nil
}

func (f *fakeKBManagementService) ListDocumentsForManagement(ctx context.Context, scope kb.ManagementScope, input kb.ListDocumentsInput) ([]kb.Document, error) {
	f.lastMgmtScope = scope
	if f.listDocsFn != nil {
		return f.listDocsFn(ctx, scope, input)
	}
	return []kb.Document{{ID: "doc-1", NamespaceID: input.NamespaceID, DomainID: scope.DomainID, OwnerScope: scope.OwnerScope, OwnerID: scope.OwnerID, Title: "Doc", Status: kb.DocumentActive}}, nil
}

func (f *fakeKBManagementService) DocumentTreeForManagement(ctx context.Context, scope kb.ManagementScope, documentID string, includeText bool) ([]kb.TreeNode, *kb.Document, error) {
	f.lastMgmtScope = scope
	if f.treeFn != nil {
		return f.treeFn(ctx, scope, documentID, includeText)
	}
	return []kb.TreeNode{{ID: "0000", DocumentID: documentID, NamespaceID: "ns-1", DomainID: scope.DomainID, OwnerScope: scope.OwnerScope, OwnerID: scope.OwnerID, Title: "Node", Text: "text"}}, &kb.Document{ID: documentID, NamespaceID: "ns-1", DomainID: scope.DomainID, OwnerScope: scope.OwnerScope, OwnerID: scope.OwnerID}, nil
}

func (f *fakeKBManagementService) ArchiveDocument(ctx context.Context, scope kb.ManagementScope, documentID string) error {
	f.lastMgmtScope = scope
	if f.archiveFn != nil {
		return f.archiveFn(ctx, scope, documentID)
	}
	return nil
}

func (f *fakeKBManagementService) CreateBinding(ctx context.Context, scope kb.ManagementScope, input kb.CreateBindingInput) (*kb.Binding, error) {
	f.lastMgmtScope = scope
	if f.createBindingFn != nil {
		return f.createBindingFn(ctx, scope, input)
	}
	return &kb.Binding{ID: "bind-1", NamespaceID: input.NamespaceID, DomainID: scope.DomainID, OwnerScope: scope.OwnerScope, OwnerID: scope.OwnerID, BindingType: input.BindingType, BindingTarget: input.BindingTarget, Enabled: true, EffectiveAt: time.Now()}, nil
}

func (f *fakeKBManagementService) ListBindingsForManagement(ctx context.Context, scope kb.ManagementScope, query kb.BindingQuery) ([]kb.Binding, error) {
	f.lastMgmtScope = scope
	if f.listBindingsFn != nil {
		return f.listBindingsFn(ctx, scope, query)
	}
	bindingType := query.BindingType
	if bindingType == "" {
		bindingType = kb.BindingTypeAgent
	}
	bindingTarget := query.BindingTarget
	if bindingTarget == "" {
		bindingTarget = "master"
	}
	return []kb.Binding{{ID: "bind-1", NamespaceID: "ns-1", DomainID: scope.DomainID, OwnerScope: scope.OwnerScope, OwnerID: scope.OwnerID, BindingType: bindingType, BindingTarget: bindingTarget, Enabled: true, EffectiveAt: time.Now()}}, nil
}

func (f *fakeKBManagementService) UpdateBinding(ctx context.Context, scope kb.ManagementScope, bindingID string, input kb.UpdateBindingInput) (*kb.Binding, error) {
	f.lastMgmtScope = scope
	if f.updateBindingFn != nil {
		return f.updateBindingFn(ctx, scope, bindingID, input)
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	return &kb.Binding{ID: bindingID, NamespaceID: "ns-1", DomainID: scope.DomainID, OwnerScope: scope.OwnerScope, OwnerID: scope.OwnerID, BindingType: kb.BindingTypeAgent, BindingTarget: "master", Enabled: enabled, EffectiveAt: time.Now()}, nil
}

func (f *fakeKBManagementService) DisableBinding(ctx context.Context, scope kb.ManagementScope, bindingID string) (*kb.Binding, error) {
	f.lastMgmtScope = scope
	if f.disableBindingFn != nil {
		return f.disableBindingFn(ctx, scope, bindingID)
	}
	return &kb.Binding{ID: bindingID, NamespaceID: "ns-1", DomainID: scope.DomainID, OwnerScope: scope.OwnerScope, OwnerID: scope.OwnerID, BindingType: kb.BindingTypeAgent, BindingTarget: "master", Enabled: false, EffectiveAt: time.Now()}, nil
}

func (f *fakeKBManagementService) ActiveBindingHint(context.Context, kb.ActiveBindingHintInput) (string, bool, error) {
	return "", false, nil
}

func (f *fakeKBManagementService) EffectiveBindings(ctx context.Context, input kb.BindingResolveInput) ([]kb.EffectiveBinding, error) {
	if f.effectiveFn != nil {
		return f.effectiveFn(ctx, input)
	}
	return []kb.EffectiveBinding{{Binding: kb.Binding{ID: "bind-1", NamespaceID: "ns-1", DomainID: input.DomainID, OwnerScope: input.OwnerScope, OwnerID: input.OwnerID, BindingType: kb.BindingTypeAgent, BindingTarget: input.AgentID, Enabled: true, EffectiveAt: time.Now()}, NamespaceID: "ns-1"}}, nil
}

func (f *fakeKBManagementService) DocMeta(ctx context.Context, scope kb.Scope, input kb.DocMetaInput) (*kb.DocMetaResult, error) {
	f.lastScope = scope
	if f.docMetaFn != nil {
		return f.docMetaFn(ctx, scope, input)
	}
	return &kb.DocMetaResult{}, nil
}

func (f *fakeKBManagementService) DocStructure(ctx context.Context, scope kb.Scope, input kb.DocStructureInput) (*kb.DocStructureResult, error) {
	f.lastScope = scope
	if f.docStructureFn != nil {
		return f.docStructureFn(ctx, scope, input)
	}
	return &kb.DocStructureResult{DocumentID: input.DocumentID, NamespaceID: input.NamespaceID}, nil
}

func (f *fakeKBManagementService) SectionText(ctx context.Context, scope kb.Scope, evidence kb.EvidenceScope, input kb.SectionTextInput) (*kb.SectionTextResult, error) {
	f.lastScope = scope
	f.lastEvidence = evidence
	if f.sectionTextFn != nil {
		return f.sectionTextFn(ctx, scope, evidence, input)
	}
	return &kb.SectionTextResult{
		DocumentID:  input.DocumentID,
		NamespaceID: input.NamespaceID,
		Sections: []kb.SectionText{{
			NodeID:        input.NodeIDs[0],
			NodePath:      "1",
			Title:         "Node",
			Text:          "text",
			EvidenceToken: "kbref-token",
		}},
	}, nil
}

func (f *fakeKBManagementService) IngestMarkdown(ctx context.Context, scope kb.Scope, input kb.IngestMarkdownInput) (*kb.Document, error) {
	f.lastScope = scope
	f.lastIngestInput = input
	if f.ingestFn != nil {
		return f.ingestFn(ctx, scope, input)
	}
	return &kb.Document{
		ID:          "doc-1",
		NamespaceID: input.NamespaceID,
		DomainID:    scope.DomainID,
		OwnerScope:  scope.OwnerScope,
		OwnerID:     scope.OwnerID,
		Title:       input.Title,
		Version:     input.Version,
		Status:      kb.DocumentActive,
		EffectiveAt: time.Now(),
	}, nil
}

func (f *fakeKBManagementService) IngestMarkdownWithAssets(ctx context.Context, scope kb.Scope, input kb.IngestMarkdownWithAssetsInput) (*kb.Document, error) {
	f.lastScope = scope
	f.lastAssetsInput = input
	if f.ingestAssetsFn != nil {
		return f.ingestAssetsFn(ctx, scope, input)
	}
	return &kb.Document{
		ID:          "doc-with-assets",
		NamespaceID: input.NamespaceID,
		DomainID:    scope.DomainID,
		OwnerScope:  scope.OwnerScope,
		OwnerID:     scope.OwnerID,
		Title:       input.Title,
		Version:     input.Version,
		Status:      kb.DocumentActive,
		EffectiveAt: time.Now(),
	}, nil
}

func (f *fakeKBManagementService) ListNodeAssets(ctx context.Context, scope kb.ManagementScope, documentID string, nodeIDs []string) ([]kb.NodeAsset, error) {
	if f.listNodeAssetsFn != nil {
		return f.listNodeAssetsFn(ctx, scope, documentID, nodeIDs)
	}
	return nil, nil
}

func (f *fakeKBManagementService) CurrentTurnEvidence(ctx context.Context, scope kb.EvidenceScope) ([]kb.EvidenceRef, error) {
	f.lastEvidence = scope
	if f.evidenceFn != nil {
		return f.evidenceFn(ctx, scope)
	}
	return []kb.EvidenceRef{{Token: "kbref-token", DocumentID: "doc-1", NodeID: "0000", Verified: true}}, nil
}

func TestKBHandlersNilServiceReturns503(t *testing.T) {
	srv := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/kb/namespaces", nil)

	srv.handleKBListNamespaces(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestKBIngestMarkdownDerivesOwnerFromAuthUser(t *testing.T) {
	service := &fakeKBManagementService{}
	srv := &Server{kbService: service}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("title", "Doc"))
	require.NoError(t, writer.WriteField("version", "v9"))
	require.NoError(t, writer.WriteField("markdown", "# Doc\nbody"))
	require.NoError(t, writer.WriteField("owner_id", "attacker"))
	require.NoError(t, writer.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/kb/namespaces/ns-1/documents:ingest-markdown?domain_id=support", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.SetPathValue("id", "ns-1")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: "user-1", Role: "user"}))
	rec := httptest.NewRecorder()

	srv.handleKBIngestMarkdown(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Equal(t, "support", service.lastScope.DomainID)
	require.Equal(t, kb.OwnerScopeUser, service.lastScope.OwnerScope)
	require.Equal(t, "user-1", service.lastScope.OwnerID)
	require.Equal(t, []string{"ns-1"}, service.lastScope.NamespaceIDs)
	require.Equal(t, "ns-1", service.lastIngestInput.NamespaceID)
}

func TestKBIngestMarkdownRejectsJSONBody(t *testing.T) {
	service := &fakeKBManagementService{}
	srv := &Server{kbService: service}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/kb/namespaces/ns-1/documents:ingest-markdown?domain_id=support", strings.NewReader(`{"title":"Doc","markdown":"# Doc"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "ns-1")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: "user-1", Role: "user"}))
	rec := httptest.NewRecorder()

	srv.handleKBIngestMarkdown(rec, req)

	require.Equal(t, http.StatusUnsupportedMediaType, rec.Code, rec.Body.String())
	require.Empty(t, service.lastIngestInput.NamespaceID)
	require.Empty(t, service.lastAssetsInput.NamespaceID)
}

func TestKBIngestMarkdownDataURIUsesAssetIngest(t *testing.T) {
	service := &fakeKBManagementService{}
	srv := &Server{kbService: service}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("title", "Doc"))
	require.NoError(t, writer.WriteField("version", "v1"))
	require.NoError(t, writer.WriteField("markdown", "# Doc\n![inline](data:image/png;base64,cG5nLWRhdGE=)"))
	require.NoError(t, writer.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/kb/namespaces/ns-1/documents:ingest-markdown?domain_id=support", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.SetPathValue("id", "ns-1")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: "user-1", Role: "user"}))
	rec := httptest.NewRecorder()

	srv.handleKBIngestMarkdown(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Equal(t, "ns-1", service.lastAssetsInput.NamespaceID)
	require.Contains(t, service.lastAssetsInput.Content, "data:image/png;base64")
	require.Empty(t, service.lastIngestInput.NamespaceID)
}

func TestKBIngestMarkdownExistingAssetURIUsesPlainIngest(t *testing.T) {
	service := &fakeKBManagementService{}
	srv := &Server{kbService: service}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("title", "Doc"))
	require.NoError(t, writer.WriteField("version", "v1"))
	require.NoError(t, writer.WriteField("markdown", "# Doc\n![stored](asset://kb/user/user-1/ns-1/doc/hash.png)"))
	require.NoError(t, writer.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/kb/namespaces/ns-1/documents:ingest-markdown?domain_id=support", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.SetPathValue("id", "ns-1")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: "user-1", Role: "user"}))
	rec := httptest.NewRecorder()

	srv.handleKBIngestMarkdown(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Equal(t, "ns-1", service.lastIngestInput.NamespaceID)
	require.Empty(t, service.lastAssetsInput.NamespaceID)
}

func TestKBIngestMarkdownMultipartUsesAssetIngest(t *testing.T) {
	service := &fakeKBManagementService{}
	srv := &Server{kbService: service}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("title", "Doc"))
	require.NoError(t, writer.WriteField("version", "v2"))
	require.NoError(t, writer.WriteField("markdown", "# Doc\n![diagram](./a.png)"))
	assetPart, err := writer.CreateFormFile("assets", "a.png")
	require.NoError(t, err)
	_, err = assetPart.Write([]byte("png-data"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/kb/namespaces/ns-1/documents:ingest-markdown?domain_id=support", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.SetPathValue("id", "ns-1")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: "user-1", Role: "user"}))
	rec := httptest.NewRecorder()

	srv.handleKBIngestMarkdown(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Equal(t, "support", service.lastScope.DomainID)
	require.Equal(t, "ns-1", service.lastAssetsInput.NamespaceID)
	require.Equal(t, "# Doc\n![diagram](./a.png)", service.lastAssetsInput.Content)
	require.Contains(t, service.lastAssetsInput.Assets, "a.png")
	require.Equal(t, []byte("png-data"), service.lastAssetsInput.Assets["a.png"].Data)
	require.Equal(t, "image/png", service.lastAssetsInput.Assets["a.png"].MimeType)
	require.Empty(t, service.lastIngestInput.NamespaceID)
}

func TestKBIngestMarkdownReturnsReportStagesAndAssetBindings(t *testing.T) {
	service := &fakeKBManagementService{
		treeFn: func(ctx context.Context, scope kb.ManagementScope, documentID string, includeText bool) ([]kb.TreeNode, *kb.Document, error) {
			return []kb.TreeNode{{
				ID:          "0001",
				DocumentID:  documentID,
				NamespaceID: "ns-1",
				DomainID:    scope.DomainID,
				OwnerScope:  scope.OwnerScope,
				OwnerID:     scope.OwnerID,
				NodePath:    "1",
				Title:       "章节一",
				StartLine:   1,
				EndLine:     3,
			}}, &kb.Document{ID: documentID, NamespaceID: "ns-1", DomainID: scope.DomainID, OwnerScope: scope.OwnerScope, OwnerID: scope.OwnerID}, nil
		},
		listNodeAssetsFn: func(ctx context.Context, scope kb.ManagementScope, documentID string, nodeIDs []string) ([]kb.NodeAsset, error) {
			return []kb.NodeAsset{{
				AssetURI:    "asset://bound.png",
				DocumentID:  documentID,
				NamespaceID: "ns-1",
				DomainID:    scope.DomainID,
				OwnerScope:  scope.OwnerScope,
				OwnerID:     scope.OwnerID,
				NodeID:      "0001",
				AltText:     "diagram",
				ContentHash: "hash-bound",
				MimeType:    "image/png",
			}, {
				AssetURI:    "asset://unbound.png",
				DocumentID:  documentID,
				NamespaceID: "ns-1",
				DomainID:    scope.DomainID,
				OwnerScope:  scope.OwnerScope,
				OwnerID:     scope.OwnerID,
				ContentHash: "hash-unbound",
				MimeType:    "image/png",
			}}, nil
		},
	}
	srv := &Server{kbService: service}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("title", "Doc"))
	require.NoError(t, writer.WriteField("version", "v2"))
	require.NoError(t, writer.WriteField("markdown", "# Doc\n![diagram](./a.png)\ntext"))
	assetPart, err := writer.CreateFormFile("assets", "a.png")
	require.NoError(t, err)
	_, err = assetPart.Write([]byte("png-data"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/kb/namespaces/ns-1/documents:ingest-markdown?domain_id=support", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.SetPathValue("id", "ns-1")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: "user-1", Role: "user"}))
	rec := httptest.NewRecorder()

	srv.handleKBIngestMarkdown(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var got struct {
		Report kbIngestReport `json:"report"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.NotEmpty(t, got.Report.IngestID)
	require.Equal(t, "ns-1", got.Report.NamespaceID)
	require.Equal(t, 3, got.Report.MarkdownLines)
	require.Equal(t, 1, got.Report.ImageRefs)
	require.Equal(t, 1, got.Report.TreeNodes)
	require.Equal(t, 1, got.Report.BoundAssets)
	require.Equal(t, 1, got.Report.UnboundAssets)
	require.NotEmpty(t, got.Report.Stages)
	require.Len(t, got.Report.AssetBindings, 2)
	require.Equal(t, "0001", got.Report.AssetBindings[0].NodeID)
	require.Equal(t, "章节一", got.Report.AssetBindings[0].NodeTitle)
	require.True(t, got.Report.AssetBindings[0].Bound)
	require.False(t, got.Report.AssetBindings[1].Bound)
}

func TestKBIngestMarkdownMultipartDocumentFileMergesConvertedAndUploadedAssets(t *testing.T) {
	service := &fakeKBManagementService{}
	srv := &Server{
		kbService: service,
		kbMarkdownRegistry: fileconv.NewMarkdownRegistry(fakeMarkdownProvider{
			supports: func(filename, mimeType string) bool {
				return filename == "manual.pdf" && mimeType == "application/pdf"
			},
			doc: &fileconv.MarkdownDocument{
				Title:   "Manual From Provider",
				Content: "# Manual\n![pdf](images/pdf.png)\n![extra](extra.png)",
				Assets: []fileconv.ExtractedAsset{{
					Path:     "images/pdf.png",
					Filename: "pdf.png",
					MimeType: "image/png",
					Data:     []byte("pdf-image"),
				}},
			},
		}),
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("version", "v3"))
	filePart, err := writer.CreateFormFile("file", "manual.pdf")
	require.NoError(t, err)
	_, err = filePart.Write([]byte("%PDF-1.7 fake"))
	require.NoError(t, err)
	assetPart, err := writer.CreateFormFile("assets", "extra.png")
	require.NoError(t, err)
	_, err = assetPart.Write([]byte("extra-image"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/kb/namespaces/ns-1/documents:ingest-markdown?domain_id=support", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.SetPathValue("id", "ns-1")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: "user-1", Role: "user"}))
	rec := httptest.NewRecorder()

	srv.handleKBIngestMarkdown(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Equal(t, "Manual From Provider", service.lastAssetsInput.Title)
	require.Equal(t, "# Manual\n![pdf](images/pdf.png)\n![extra](extra.png)", service.lastAssetsInput.Content)
	require.Contains(t, service.lastAssetsInput.Assets, "images/pdf.png")
	require.Contains(t, service.lastAssetsInput.Assets, "extra.png")
	require.Equal(t, []byte("pdf-image"), service.lastAssetsInput.Assets["images/pdf.png"].Data)
	require.Equal(t, []byte("extra-image"), service.lastAssetsInput.Assets["extra.png"].Data)
	require.Empty(t, service.lastIngestInput.NamespaceID)
}

func TestKBIngestMarkdownDocumentFilePreservesEditedMarkdown(t *testing.T) {
	service := &fakeKBManagementService{}
	srv := &Server{
		kbService: service,
		kbMarkdownRegistry: fileconv.NewMarkdownRegistry(fakeMarkdownProvider{
			supports: func(filename, mimeType string) bool {
				return filename == "manual.docx" && mimeType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
			},
			doc: &fileconv.MarkdownDocument{
				Title:   "Converted Manual",
				Content: "# Converted\n![old](images/original.png)",
				Assets: []fileconv.ExtractedAsset{{
					Path:     "images/original.png",
					Filename: "original.png",
					MimeType: "image/png",
					Data:     []byte("original-image"),
				}},
			},
		}),
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("title", "Edited Manual"))
	require.NoError(t, writer.WriteField("version", "v4"))
	require.NoError(t, writer.WriteField("markdown", "# Edited\n![new](images/original.png)"))
	filePart, err := writer.CreateFormFile("file", "manual.docx")
	require.NoError(t, err)
	_, err = filePart.Write([]byte("docx-data"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/kb/namespaces/ns-1/documents:ingest-markdown?domain_id=support", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.SetPathValue("id", "ns-1")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: "user-1", Role: "user"}))
	rec := httptest.NewRecorder()

	srv.handleKBIngestMarkdown(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Equal(t, "Edited Manual", service.lastAssetsInput.Title)
	require.Equal(t, "# Edited\n![new](images/original.png)", service.lastAssetsInput.Content)
	require.Contains(t, service.lastAssetsInput.Assets, "images/original.png")
	require.Equal(t, []byte("original-image"), service.lastAssetsInput.Assets["images/original.png"].Data)
	require.Empty(t, service.lastIngestInput.NamespaceID)
}

func TestKBPreviewMarkdownReturnsConvertedContentAndAssets(t *testing.T) {
	service := &fakeKBManagementService{}
	srv := &Server{
		kbService: service,
		kbMarkdownRegistry: fileconv.NewMarkdownRegistry(fakeMarkdownProvider{
			supports: func(filename, mimeType string) bool {
				return filename == "manual.pdf" && mimeType == "application/pdf"
			},
			doc: &fileconv.MarkdownDocument{
				Title:    "Manual Preview",
				Content:  "# Manual Preview\n![diagram](images/diagram.png)",
				Quality:  fileconv.ConversionQualityExact,
				Warnings: []string{"converted with mock"},
				Assets: []fileconv.ExtractedAsset{{
					Path:     "images/diagram.png",
					Filename: "diagram.png",
					MimeType: "image/png",
					Data:     []byte("png-data"),
					AltText:  "diagram",
				}},
			},
		}),
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	filePart, err := writer.CreateFormFile("file", "manual.pdf")
	require.NoError(t, err)
	_, err = filePart.Write([]byte("%PDF-1.7 fake"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/kb/namespaces/ns-1/documents:preview-markdown?domain_id=support", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.SetPathValue("id", "ns-1")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: "user-1", Role: "user"}))
	rec := httptest.NewRecorder()

	srv.handleKBPreviewMarkdown(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got kbMarkdownPreviewResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "Manual Preview", got.Title)
	require.Equal(t, "# Manual Preview\n![diagram](images/diagram.png)", got.Markdown)
	require.Equal(t, "exact", got.Quality)
	require.Equal(t, "fake-markdown", got.Provider)
	require.Equal(t, []string{"converted with mock"}, got.Warnings)
	require.Len(t, got.Assets, 1)
	require.Equal(t, "images/diagram.png", got.Assets[0].Path)
	require.Equal(t, "diagram.png", got.Assets[0].Filename)
	require.Equal(t, "image/png", got.Assets[0].MimeType)
	require.Equal(t, 8, got.Assets[0].Size)
}

func TestKBIngestMarkdownMultipartMultipleDocumentFilesRejects(t *testing.T) {
	service := &fakeKBManagementService{}
	srv := &Server{kbService: service}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("title", "Doc"))
	part, err := writer.CreateFormFile("file", "a.md")
	require.NoError(t, err)
	_, err = part.Write([]byte("# A"))
	require.NoError(t, err)
	part, err = writer.CreateFormFile("file", "b.md")
	require.NoError(t, err)
	_, err = part.Write([]byte("# B"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/kb/namespaces/ns-1/documents:ingest-markdown", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.SetPathValue("id", "ns-1")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: "user-1", Role: "user"}))
	rec := httptest.NewRecorder()

	srv.handleKBIngestMarkdown(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Empty(t, service.lastIngestInput.NamespaceID)
	require.Empty(t, service.lastAssetsInput.NamespaceID)
}

func TestKBIngestMarkdownPDFProviderUnavailableReturns503(t *testing.T) {
	service := &fakeKBManagementService{}
	srv := &Server{
		kbService:          service,
		kbMarkdownRegistry: fileconv.NewMarkdownRegistryWithoutPDF(),
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("title", "Doc"))
	require.NoError(t, writer.WriteField("version", "v1"))
	filePart, err := writer.CreateFormFile("file", "manual.pdf")
	require.NoError(t, err)
	_, err = filePart.Write([]byte("%PDF-1.7 mock"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/kb/namespaces/ns-1/documents:ingest-markdown", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.SetPathValue("id", "ns-1")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: "user-1", Role: "user"}))
	rec := httptest.NewRecorder()

	srv.handleKBIngestMarkdown(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Empty(t, service.lastIngestInput.NamespaceID)
	require.Empty(t, service.lastAssetsInput.NamespaceID)
}

func TestKBIngestMarkdownTooLargeReturns413BeforeService(t *testing.T) {
	called := false
	service := &fakeKBManagementService{
		ingestFn: func(context.Context, kb.Scope, kb.IngestMarkdownInput) (*kb.Document, error) {
			called = true
			return nil, nil
		},
	}
	srv := &Server{kbService: service}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("title", "Doc"))
	require.NoError(t, writer.WriteField("markdown", strings.Repeat("a", maxKBMarkdownBytes+1)))
	require.NoError(t, writer.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/kb/namespaces/ns-1/documents:ingest-markdown", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.SetPathValue("id", "ns-1")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: "user-1"}))
	rec := httptest.NewRecorder()

	srv.handleKBIngestMarkdown(rec, req)

	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
	require.False(t, called)
}

func TestKBIngestMarkdownUnsupportedImageReturns422(t *testing.T) {
	service := &fakeKBManagementService{
		ingestAssetsFn: func(context.Context, kb.Scope, kb.IngestMarkdownWithAssetsInput) (*kb.Document, error) {
			return nil, kb.ErrUnsupportedAsset
		},
	}
	srv := &Server{kbService: service}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("title", "Doc"))
	require.NoError(t, writer.WriteField("markdown", "# Doc\n![x](https://example.com/a.png)"))
	require.NoError(t, writer.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/kb/namespaces/ns-1/documents:ingest-markdown", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.SetPathValue("id", "ns-1")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: "user-1"}))
	rec := httptest.NewRecorder()

	srv.handleKBIngestMarkdown(rec, req)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
}

func TestKBGetNodeDerivesEvidenceScope(t *testing.T) {
	service := &fakeKBManagementService{}
	srv := &Server{kbService: service}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/kb/documents/doc-1/nodes/0000?domain_id=support", nil)
	req.SetPathValue("id", "doc-1")
	req.SetPathValue("node_id", "0000")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: "user-1"}))
	rec := httptest.NewRecorder()

	srv.handleKBGetDocumentNode(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "support", service.lastMgmtScope.DomainID)
	require.Equal(t, "user-1", service.lastMgmtScope.OwnerID)
}

func TestKBErrorMappingNotFound(t *testing.T) {
	service := &fakeKBManagementService{
		treeFn: func(context.Context, kb.ManagementScope, string, bool) ([]kb.TreeNode, *kb.Document, error) {
			return nil, nil, kb.ErrNotFound
		},
	}
	srv := &Server{kbService: service}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/kb/documents/missing/tree", nil)
	req.SetPathValue("id", "missing")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: "user-1"}))
	rec := httptest.NewRecorder()

	srv.handleKBGetDocumentTree(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

func TestKBErrorMappingInternal(t *testing.T) {
	service := &fakeKBManagementService{
		treeFn: func(context.Context, kb.ManagementScope, string, bool) ([]kb.TreeNode, *kb.Document, error) {
			return nil, nil, errors.New("boom")
		},
	}
	srv := &Server{kbService: service}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/kb/documents/doc-1/tree", nil)
	req.SetPathValue("id", "doc-1")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: "user-1"}))
	rec := httptest.NewRecorder()

	srv.handleKBGetDocumentTree(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
}

func TestSessionKBBindingsPutReplacesCurrentDomainBindings(t *testing.T) {
	ctx := auth.WithUser(auth.WithAuthEnabled(context.Background()), &auth.User{ID: "user-1", Role: "user"})
	appStore := store.NewMemoryStore()
	m := master.NewMaster(master.Config{Model: "test"}, config.HITLConfig{}, subagent.NewRegistry(nil), skills.NewRegistry(nil), appStore, zap.NewNop())
	sessionID, err := m.CreateSession(ctx, "kb-session", "direct")
	require.NoError(t, err)
	kbStore := kb.NewMemoryStore()
	kbService := kb.NewService(kbStore)
	now := time.Now()
	for _, namespaceID := range []string{"ns-old", "ns-new"} {
		require.NoError(t, kbStore.SaveNamespace(ctx, kb.Namespace{
			ID:            namespaceID,
			Name:          namespaceID,
			DomainID:      "support",
			OwnerScope:    kb.OwnerScopeUser,
			OwnerID:       "user-1",
			IndexStrategy: "markdown_tree",
			CreatedAt:     now,
			UpdatedAt:     now,
		}))
	}
	_, err = kbService.CreateBinding(ctx, kb.ManagementScope{
		DomainID:   "support",
		OwnerScope: kb.OwnerScopeUser,
		OwnerID:    "user-1",
		Now:        now,
	}, kb.CreateBindingInput{
		NamespaceID:   "ns-old",
		DomainID:      "support",
		BindingType:   kb.BindingTypeSession,
		BindingTarget: sessionID,
		CreatedBy:     "user-1",
	})
	require.NoError(t, err)
	srv := &Server{master: m, kbService: kbService}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/sessions/"+sessionID+"/kb-bindings?domain_id=support", strings.NewReader(`{"namespace_ids":["ns-new"]}`))
	req.SetPathValue("id", sessionID)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	srv.handlePutSessionKBBindings(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		Bindings []kbBindingResponse `json:"bindings"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Bindings, 1)
	require.Equal(t, "ns-new", got.Bindings[0].NamespaceID)
	require.Equal(t, "support", m.GetCachedSession(sessionID).KBDomainIDSnapshot())
	record, err := appStore.LoadSession(ctx, sessionID)
	require.NoError(t, err)
	require.Equal(t, "support", record.KBDomainID)
	bindings, err := kbService.ListBindingsForManagement(ctx, kb.ManagementScope{
		DomainID:   "support",
		OwnerScope: kb.OwnerScopeUser,
		OwnerID:    "user-1",
		Now:        now,
	}, kb.BindingQuery{})
	require.NoError(t, err)
	enabledByNS := map[string]bool{}
	for _, binding := range bindings {
		if binding.BindingTarget == sessionID {
			enabledByNS[binding.NamespaceID] = binding.Enabled
		}
	}
	require.False(t, enabledByNS["ns-old"])
	require.True(t, enabledByNS["ns-new"])
}

func TestSessionKBBindingDeleteReturnsRemainingBindings(t *testing.T) {
	ctx := auth.WithUser(auth.WithAuthEnabled(context.Background()), &auth.User{ID: "user-1", Role: "user"})
	appStore := store.NewMemoryStore()
	m := master.NewMaster(master.Config{Model: "test"}, config.HITLConfig{}, subagent.NewRegistry(nil), skills.NewRegistry(nil), appStore, zap.NewNop())
	sessionID, err := m.CreateSession(ctx, "kb-session", "direct")
	require.NoError(t, err)
	kbStore := kb.NewMemoryStore()
	kbService := kb.NewService(kbStore)
	now := time.Now()
	for _, namespaceID := range []string{"ns-old", "ns-keep"} {
		require.NoError(t, kbStore.SaveNamespace(ctx, kb.Namespace{
			ID:            namespaceID,
			Name:          namespaceID,
			DomainID:      "support",
			OwnerScope:    kb.OwnerScopeUser,
			OwnerID:       "user-1",
			IndexStrategy: "markdown_tree",
			CreatedAt:     now,
			UpdatedAt:     now,
		}))
		_, err = kbService.CreateBinding(ctx, kb.ManagementScope{
			DomainID:   "support",
			OwnerScope: kb.OwnerScopeUser,
			OwnerID:    "user-1",
			Now:        now,
		}, kb.CreateBindingInput{
			NamespaceID:   namespaceID,
			DomainID:      "support",
			BindingType:   kb.BindingTypeSession,
			BindingTarget: sessionID,
			CreatedBy:     "user-1",
		})
		require.NoError(t, err)
	}
	require.NoError(t, m.SetSessionKBDomain(ctx, sessionID, "support", true))
	srv := &Server{master: m, kbService: kbService}
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/sessions/"+sessionID+"/kb-bindings/ns-old?domain_id=support", nil)
	req.SetPathValue("id", sessionID)
	req.SetPathValue("namespace_id", "ns-old")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	srv.handleDeleteSessionKBBinding(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		Bindings []kbBindingResponse `json:"bindings"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Bindings, 1)
	require.Equal(t, "ns-keep", got.Bindings[0].NamespaceID)
	require.Equal(t, "support", m.GetCachedSession(sessionID).KBDomainIDSnapshot())
}
