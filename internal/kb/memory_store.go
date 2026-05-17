package kb

import (
	"context"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

type MemoryStore struct {
	mu         sync.RWMutex
	namespaces map[string]Namespace
	documents  map[string]Document
	nodes      map[string][]TreeNode
	bindings   map[string]Binding
	evidence   map[string]EvidenceEvent
	nodeAssets map[string][]NodeAsset
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		namespaces: make(map[string]Namespace),
		documents:  make(map[string]Document),
		nodes:      make(map[string][]TreeNode),
		bindings:   make(map[string]Binding),
		evidence:   make(map[string]EvidenceEvent),
		nodeAssets: make(map[string][]NodeAsset),
	}
}

func (s *MemoryStore) SaveNamespace(ctx context.Context, namespace Namespace) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if namespace.ID == "" || namespace.DomainID == "" || namespace.OwnerScope == "" || namespace.OwnerID == "" {
		return ErrInvalidInput
	}
	now := time.Now()
	if namespace.CreatedAt.IsZero() {
		namespace.CreatedAt = now
	}
	if namespace.UpdatedAt.IsZero() {
		namespace.UpdatedAt = now
	}
	if namespace.IndexStrategy == "" {
		namespace.IndexStrategy = "tree"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.namespaces[namespace.ID] = namespace
	return nil
}

func (s *MemoryStore) GetNamespace(ctx context.Context, namespaceID string) (*Namespace, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	namespace, ok := s.namespaces[namespaceID]
	if !ok {
		return nil, ErrNotFound
	}
	return &namespace, nil
}

func (s *MemoryStore) ListNamespaces(ctx context.Context, query NamespaceQuery) ([]Namespace, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if query.DomainID == "" || query.OwnerScope == "" || query.OwnerID == "" {
		return nil, ErrInvalidScope
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	namespaces := make([]Namespace, 0)
	for _, namespace := range s.namespaces {
		if namespace.DomainID != query.DomainID || namespace.OwnerScope != query.OwnerScope || namespace.OwnerID != query.OwnerID {
			continue
		}
		if query.Query != "" && !strings.Contains(strings.ToLower(namespace.Name), strings.ToLower(query.Query)) {
			continue
		}
		namespaces = append(namespaces, namespace)
	}
	sort.Slice(namespaces, func(i, j int) bool {
		if namespaces[i].UpdatedAt.Equal(namespaces[j].UpdatedAt) {
			return namespaces[i].ID < namespaces[j].ID
		}
		return namespaces[i].UpdatedAt.After(namespaces[j].UpdatedAt)
	})
	if query.Limit > 0 && len(namespaces) > query.Limit {
		namespaces = namespaces[:query.Limit]
	}
	return namespaces, nil
}

func (s *MemoryStore) SaveDocument(ctx context.Context, document Document, nodes []TreeNode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if document.ID == "" || document.NamespaceID == "" || document.DomainID == "" || document.OwnerScope == "" || document.OwnerID == "" {
		return ErrInvalidInput
	}
	now := time.Now()
	if document.CreatedAt.IsZero() {
		document.CreatedAt = now
	}
	if document.UpdatedAt.IsZero() {
		document.UpdatedAt = now
	}
	if document.EffectiveAt.IsZero() {
		document.EffectiveAt = now
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.documents {
		if existing.NamespaceID == document.NamespaceID && existing.ContentHash == document.ContentHash && existing.Version == document.Version {
			return ErrDuplicateDocument
		}
	}
	copied := make([]TreeNode, len(nodes))
	for i, node := range nodes {
		if node.DocumentID == "" {
			node.DocumentID = document.ID
		}
		if node.NamespaceID == "" {
			node.NamespaceID = document.NamespaceID
		}
		if node.DomainID == "" {
			node.DomainID = document.DomainID
		}
		if node.OwnerScope == "" {
			node.OwnerScope = document.OwnerScope
		}
		if node.OwnerID == "" {
			node.OwnerID = document.OwnerID
		}
		if node.CreatedAt.IsZero() {
			node.CreatedAt = now
		}
		copied[i] = node
	}
	s.documents[document.ID] = document
	s.nodes[document.ID] = copied
	return nil
}

func (s *MemoryStore) UpdateDocumentStatus(ctx context.Context, scope ManagementScope, documentID string, status DocumentStatus) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateManagementScope(scope); err != nil {
		return err
	}
	if status == "" {
		return ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, ok := s.documents[documentID]
	if !ok || !documentVisibleForManagement(scope, doc) {
		return ErrNotFound
	}
	doc.Status = status
	doc.UpdatedAt = time.Now()
	s.documents[documentID] = doc
	return nil
}

func (s *MemoryStore) GetDocument(ctx context.Context, scope Scope, documentID string) (*Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateScope(scope); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	doc, ok := s.documents[documentID]
	if !ok || !documentVisible(scope, doc) {
		return nil, ErrNotFound
	}
	return &doc, nil
}

func (s *MemoryStore) GetDocumentForManagement(ctx context.Context, scope ManagementScope, documentID string) (*Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateManagementScope(scope); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	doc, ok := s.documents[documentID]
	if !ok || !documentVisibleForManagement(scope, doc) {
		return nil, ErrNotFound
	}
	return &doc, nil
}

func (s *MemoryStore) ListDocuments(ctx context.Context, scope Scope, query DocumentQuery) ([]Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateScope(scope); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var docs []Document
	for _, doc := range s.documents {
		if !documentVisible(scope, doc) {
			continue
		}
		if query.Query != "" && !strings.Contains(strings.ToLower(doc.Title+" "+doc.Description), strings.ToLower(query.Query)) {
			continue
		}
		docs = append(docs, doc)
	}
	sort.Slice(docs, func(i, j int) bool {
		if docs[i].UpdatedAt.Equal(docs[j].UpdatedAt) {
			return docs[i].ID < docs[j].ID
		}
		return docs[i].UpdatedAt.After(docs[j].UpdatedAt)
	})
	if query.Limit > 0 && len(docs) > query.Limit {
		docs = docs[:query.Limit]
	}
	return docs, nil
}

func (s *MemoryStore) ListDocumentsForManagement(ctx context.Context, scope ManagementScope, namespaceID string, query DocumentQuery) ([]Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateManagementScope(scope); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var docs []Document
	for _, doc := range s.documents {
		if !documentVisibleForManagement(scope, doc) {
			continue
		}
		if namespaceID != "" && doc.NamespaceID != namespaceID {
			continue
		}
		if query.Status != "" && doc.Status != query.Status {
			continue
		}
		if query.Query != "" && !strings.Contains(strings.ToLower(doc.Title+" "+doc.Description), strings.ToLower(query.Query)) {
			continue
		}
		docs = append(docs, doc)
	}
	sort.Slice(docs, func(i, j int) bool {
		if docs[i].UpdatedAt.Equal(docs[j].UpdatedAt) {
			return docs[i].ID < docs[j].ID
		}
		return docs[i].UpdatedAt.After(docs[j].UpdatedAt)
	})
	if query.Limit > 0 && len(docs) > query.Limit {
		docs = docs[:query.Limit]
	}
	return docs, nil
}

func (s *MemoryStore) GetStructure(ctx context.Context, scope Scope, documentID string) ([]TreeNode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := s.GetDocument(ctx, scope, documentID); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	nodes := append([]TreeNode(nil), s.nodes[documentID]...)
	return stripNodeText(nodes), nil
}

func (s *MemoryStore) GetStructureForManagement(ctx context.Context, scope ManagementScope, documentID string, includeText bool) ([]TreeNode, *Document, error) {
	doc, err := s.GetDocumentForManagement(ctx, scope, documentID)
	if err != nil {
		return nil, nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	nodes := append([]TreeNode(nil), s.nodes[documentID]...)
	if !includeText {
		nodes = stripNodeText(nodes)
	}
	return nodes, doc, nil
}

func (s *MemoryStore) GetSectionText(ctx context.Context, scope Scope, documentID string, nodeIDs []string) ([]TreeNode, *Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if len(nodeIDs) == 0 {
		return nil, nil, ErrInvalidInput
	}
	doc, err := s.GetDocument(ctx, scope, documentID)
	if err != nil {
		return nil, nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	requested := make(map[string]int, len(nodeIDs))
	for i, id := range nodeIDs {
		if id == "" {
			return nil, nil, ErrInvalidInput
		}
		requested[id] = i
	}
	matches := make([]TreeNode, 0, len(nodeIDs))
	for _, node := range s.nodes[documentID] {
		if _, ok := requested[node.ID]; !ok {
			continue
		}
		if node.OwnerScope != scope.OwnerScope || node.OwnerID != scope.OwnerID || node.DomainID != scope.DomainID {
			return nil, nil, ErrNotFound
		}
		if !slices.Contains(effectiveNamespaceIDs(scope), node.NamespaceID) {
			return nil, nil, ErrNotFound
		}
		matches = append(matches, node)
	}
	if len(matches) != len(requested) {
		return nil, nil, ErrNotFound
	}
	sort.Slice(matches, func(i, j int) bool {
		return requested[matches[i].ID] < requested[matches[j].ID]
	})
	return matches, doc, nil
}

func (s *MemoryStore) GetSectionTextByPageRanges(ctx context.Context, scope Scope, documentID string, ranges []PageRange) ([]TreeNode, *Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if len(ranges) == 0 {
		return nil, nil, ErrInvalidInput
	}
	doc, err := s.GetDocument(ctx, scope, documentID)
	if err != nil {
		return nil, nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	matches := make([]TreeNode, 0)
	seen := make(map[string]bool)
	for _, node := range s.nodes[documentID] {
		if node.OwnerScope != scope.OwnerScope || node.OwnerID != scope.OwnerID || node.DomainID != scope.DomainID {
			continue
		}
		if !slices.Contains(effectiveNamespaceIDs(scope), node.NamespaceID) {
			continue
		}
		if !nodeOverlapsAnyPageRange(node, ranges) {
			continue
		}
		if seen[node.ID] {
			continue
		}
		seen[node.ID] = true
		matches = append(matches, node)
	}
	if len(matches) == 0 {
		return nil, nil, ErrNotFound
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].StartPage == matches[j].StartPage {
			return matches[i].ID < matches[j].ID
		}
		return matches[i].StartPage < matches[j].StartPage
	})
	return matches, doc, nil
}

func (s *MemoryStore) SaveBinding(ctx context.Context, binding Binding) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if binding.ID == "" || binding.NamespaceID == "" || binding.DomainID == "" || binding.OwnerScope == "" || binding.OwnerID == "" || binding.BindingType == "" || binding.BindingTarget == "" {
		return ErrInvalidInput
	}
	now := time.Now()
	if binding.CreatedAt.IsZero() {
		binding.CreatedAt = now
	}
	if binding.UpdatedAt.IsZero() {
		binding.UpdatedAt = now
	}
	if binding.EffectiveAt.IsZero() {
		binding.EffectiveAt = now
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindings[binding.ID] = binding
	return nil
}

func (s *MemoryStore) ListBindings(ctx context.Context, input BindingResolveInput) ([]Binding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if input.DomainID == "" || input.OwnerScope == "" || input.OwnerID == "" {
		return nil, ErrInvalidScope
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}
	targets := bindingTargets(input)
	s.mu.RLock()
	defer s.mu.RUnlock()
	bindings := make([]Binding, 0)
	for _, binding := range s.bindings {
		if binding.DomainID != input.DomainID || binding.OwnerScope != input.OwnerScope || binding.OwnerID != input.OwnerID {
			continue
		}
		if !binding.Enabled || binding.EffectiveAt.After(now) || (binding.ExpiresAt != nil && !binding.ExpiresAt.After(now)) {
			continue
		}
		if target, ok := targets[binding.BindingType]; !ok || target != binding.BindingTarget {
			continue
		}
		bindings = append(bindings, binding)
	}
	sort.Slice(bindings, func(i, j int) bool {
		return bindings[i].ID < bindings[j].ID
	})
	return bindings, nil
}

func (s *MemoryStore) ListBindingsForManagement(ctx context.Context, query BindingQuery) ([]Binding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if query.DomainID == "" || query.OwnerScope == "" || query.OwnerID == "" {
		return nil, ErrInvalidScope
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	bindings := make([]Binding, 0)
	for _, binding := range s.bindings {
		if binding.DomainID != query.DomainID || binding.OwnerScope != query.OwnerScope || binding.OwnerID != query.OwnerID {
			continue
		}
		if query.NamespaceID != "" && binding.NamespaceID != query.NamespaceID {
			continue
		}
		if query.BindingType != "" && binding.BindingType != query.BindingType {
			continue
		}
		if query.BindingTarget != "" && binding.BindingTarget != query.BindingTarget {
			continue
		}
		if query.Enabled != nil && binding.Enabled != *query.Enabled {
			continue
		}
		bindings = append(bindings, binding)
	}
	sort.Slice(bindings, func(i, j int) bool {
		if bindings[i].UpdatedAt.Equal(bindings[j].UpdatedAt) {
			return bindings[i].ID < bindings[j].ID
		}
		return bindings[i].UpdatedAt.After(bindings[j].UpdatedAt)
	})
	return bindings, nil
}

func (s *MemoryStore) FindActiveBindingDomains(ctx context.Context, query BindingQuery, now time.Time) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if query.OwnerScope == "" || query.OwnerID == "" || query.BindingType == "" || strings.TrimSpace(query.BindingTarget) == "" {
		return nil, ErrInvalidScope
	}
	if now.IsZero() {
		now = time.Now()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]struct{}{}
	var domains []string
	for _, binding := range s.bindings {
		if binding.OwnerScope != query.OwnerScope || binding.OwnerID != query.OwnerID {
			continue
		}
		if query.DomainID != "" && binding.DomainID != query.DomainID {
			continue
		}
		if binding.BindingType != query.BindingType || binding.BindingTarget != strings.TrimSpace(query.BindingTarget) {
			continue
		}
		if !binding.Active(now) {
			continue
		}
		if _, ok := seen[binding.DomainID]; ok {
			continue
		}
		seen[binding.DomainID] = struct{}{}
		domains = append(domains, binding.DomainID)
	}
	sort.Strings(domains)
	return domains, nil
}

func (s *MemoryStore) SaveEvidenceEvent(ctx context.Context, event EvidenceEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if event.ID == "" || event.EvidenceToken == "" || event.SessionID == "" || event.TurnID == "" || event.TraceID == "" ||
		event.DomainID == "" || event.NamespaceID == "" || event.DocumentID == "" || event.DocumentVersion == "" ||
		event.NodeID == "" || event.OwnerScope == "" || event.OwnerID == "" {
		return ErrInvalidInput
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evidence[event.ID] = event
	return nil
}

func (s *MemoryStore) ListEvidenceEvents(ctx context.Context, scope EvidenceScope) ([]EvidenceEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateEvidenceScope(scope); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	events := make([]EvidenceEvent, 0)
	for _, event := range s.evidence {
		if event.SessionID == scope.SessionID && event.TurnID == scope.TurnID && event.TraceID == scope.TraceID &&
			event.DomainID == scope.DomainID && event.OwnerScope == scope.OwnerScope && event.OwnerID == scope.OwnerID {
			events = append(events, event)
		}
	}
	sort.Slice(events, func(i, j int) bool {
		return events[i].CreatedAt.Before(events[j].CreatedAt)
	})
	return events, nil
}

func (s *MemoryStore) SaveNodeAssets(ctx context.Context, assets []NodeAsset) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, asset := range assets {
		if asset.ID == "" || asset.DocumentID == "" || asset.NamespaceID == "" || asset.DomainID == "" || asset.OwnerScope == "" || asset.OwnerID == "" || asset.AssetURI == "" {
			return ErrInvalidInput
		}
		if asset.CreatedAt.IsZero() {
			asset.CreatedAt = time.Now()
		}
		current := s.nodeAssets[asset.DocumentID]
		replaced := false
		for i := range current {
			if current[i].ID == asset.ID {
				current[i] = asset
				replaced = true
				break
			}
		}
		if !replaced {
			current = append(current, asset)
		}
		s.nodeAssets[asset.DocumentID] = current
	}
	return nil
}

func (s *MemoryStore) DeleteNodeAssetsForDocument(ctx context.Context, scope ManagementScope, documentID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateManagementScope(scope); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.nodeAssets[documentID]
	if len(current) == 0 {
		return nil
	}
	next := current[:0]
	for _, item := range current {
		if item.DomainID == scope.DomainID && item.OwnerScope == scope.OwnerScope && item.OwnerID == scope.OwnerID {
			continue
		}
		next = append(next, item)
	}
	if len(next) == 0 {
		delete(s.nodeAssets, documentID)
		return nil
	}
	s.nodeAssets[documentID] = next
	return nil
}

func (s *MemoryStore) ListNodeAssets(ctx context.Context, scope ManagementScope, documentID string, nodeIDs []string) ([]NodeAsset, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateManagementScope(scope); err != nil {
		return nil, err
	}
	requested := make(map[string]struct{}, len(nodeIDs))
	for _, id := range nodeIDs {
		if id != "" {
			requested[id] = struct{}{}
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]NodeAsset, 0)
	for _, asset := range s.nodeAssets[documentID] {
		if asset.DomainID != scope.DomainID || asset.OwnerScope != scope.OwnerScope || asset.OwnerID != scope.OwnerID {
			continue
		}
		if len(requested) > 0 {
			if _, ok := requested[asset.NodeID]; !ok {
				continue
			}
		}
		out = append(out, asset)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].NodeID == out[j].NodeID {
			return out[i].AssetURI < out[j].AssetURI
		}
		return out[i].NodeID < out[j].NodeID
	})
	return out, nil
}
