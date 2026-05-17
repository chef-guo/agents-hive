package kb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var _ Store = (*PGStore)(nil)

// PGStore 是 KB P0 的 PostgreSQL 持久化实现。
type PGStore struct {
	pool *pgxpool.Pool
}

func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

func (s *PGStore) SaveNamespace(ctx context.Context, namespace Namespace) error {
	if s == nil || s.pool == nil {
		return ErrInvalidInput
	}
	if namespace.ID == "" || namespace.DomainID == "" || namespace.OwnerScope == "" || namespace.OwnerID == "" {
		return ErrInvalidInput
	}
	now := time.Now().UTC()
	if namespace.CreatedAt.IsZero() {
		namespace.CreatedAt = now
	}
	if namespace.UpdatedAt.IsZero() {
		namespace.UpdatedAt = now
	}
	if namespace.IndexStrategy == "" {
		namespace.IndexStrategy = "markdown_tree"
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO kb_namespaces (
			id, name, domain_id, owner_scope, owner_id, index_strategy,
			thinning_enabled, thinning_token_threshold, summary_token_threshold,
			summary_model, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			domain_id = EXCLUDED.domain_id,
			owner_scope = EXCLUDED.owner_scope,
			owner_id = EXCLUDED.owner_id,
			index_strategy = EXCLUDED.index_strategy,
			thinning_enabled = EXCLUDED.thinning_enabled,
			thinning_token_threshold = EXCLUDED.thinning_token_threshold,
			summary_token_threshold = EXCLUDED.summary_token_threshold,
			summary_model = EXCLUDED.summary_model,
			updated_at = EXCLUDED.updated_at`,
		namespace.ID, namespace.Name, namespace.DomainID, string(namespace.OwnerScope), namespace.OwnerID, namespace.IndexStrategy,
		namespace.ThinningEnabled, namespace.ThinningTokenThreshold, namespace.SummaryTokenThreshold,
		namespace.SummaryModel, namespace.CreatedAt, namespace.UpdatedAt,
	)
	return err
}

func (s *PGStore) GetNamespace(ctx context.Context, namespaceID string) (*Namespace, error) {
	if s == nil || s.pool == nil {
		return nil, ErrInvalidInput
	}
	row := s.pool.QueryRow(ctx, `
		SELECT id, name, domain_id, owner_scope, owner_id, index_strategy,
		       thinning_enabled, thinning_token_threshold, summary_token_threshold,
		       summary_model, created_at, updated_at
		FROM kb_namespaces
		WHERE id = $1`, strings.TrimSpace(namespaceID))
	return scanNamespace(row)
}

func (s *PGStore) ListNamespaces(ctx context.Context, query NamespaceQuery) ([]Namespace, error) {
	if s == nil || s.pool == nil {
		return nil, ErrInvalidInput
	}
	if query.DomainID == "" || query.OwnerScope == "" || query.OwnerID == "" {
		return nil, ErrInvalidScope
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, domain_id, owner_scope, owner_id, index_strategy,
		       thinning_enabled, thinning_token_threshold, summary_token_threshold,
		       summary_model, created_at, updated_at
		FROM kb_namespaces
		WHERE domain_id = $1
		  AND owner_scope = $2
		  AND owner_id = $3
		  AND ($4 = '' OR lower(name) LIKE '%' || lower($4) || '%')
		ORDER BY updated_at DESC, id
		LIMIT $5`,
		query.DomainID, string(query.OwnerScope), query.OwnerID, strings.TrimSpace(query.Query), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Namespace
	for rows.Next() {
		namespace, err := scanNamespace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *namespace)
	}
	return out, rows.Err()
}

func (s *PGStore) SaveDocument(ctx context.Context, document Document, nodes []TreeNode) error {
	if s == nil || s.pool == nil {
		return ErrInvalidInput
	}
	if document.ID == "" || document.NamespaceID == "" || document.DomainID == "" || document.OwnerScope == "" || document.OwnerID == "" {
		return ErrInvalidInput
	}
	now := time.Now().UTC()
	if document.CreatedAt.IsZero() {
		document.CreatedAt = now
	}
	if document.UpdatedAt.IsZero() {
		document.UpdatedAt = now
	}
	if document.EffectiveAt.IsZero() {
		document.EffectiveAt = now
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	var existingID string
	err = tx.QueryRow(ctx, `
		SELECT id
		FROM kb_documents
		WHERE namespace_id = $1 AND content_hash = $2 AND version = $3
		LIMIT 1`, document.NamespaceID, document.ContentHash, document.Version).Scan(&existingID)
	if err == nil && existingID != document.ID {
		return ErrDuplicateDocument
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO kb_documents (
			id, namespace_id, domain_id, owner_scope, owner_id, source_uri, title,
			description, content_hash, version, status, effective_at, expires_at,
			created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (id) DO UPDATE SET
			namespace_id = EXCLUDED.namespace_id,
			domain_id = EXCLUDED.domain_id,
			owner_scope = EXCLUDED.owner_scope,
			owner_id = EXCLUDED.owner_id,
			source_uri = EXCLUDED.source_uri,
			title = EXCLUDED.title,
			description = EXCLUDED.description,
			content_hash = EXCLUDED.content_hash,
			version = EXCLUDED.version,
			status = EXCLUDED.status,
			effective_at = EXCLUDED.effective_at,
			expires_at = EXCLUDED.expires_at,
			updated_at = EXCLUDED.updated_at`,
		document.ID, document.NamespaceID, document.DomainID, string(document.OwnerScope), document.OwnerID,
		document.SourceURI, document.Title, document.Description, document.ContentHash, document.Version,
		string(document.Status), document.EffectiveAt, document.ExpiresAt, document.CreatedAt, document.UpdatedAt,
	); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `DELETE FROM kb_tree_nodes WHERE document_id = $1`, document.ID); err != nil {
		return err
	}
	for _, node := range nodes {
		node = normalizeNodeForDocument(node, document, now)
		if _, err := tx.Exec(ctx, `
			INSERT INTO kb_tree_nodes (
				id, document_id, namespace_id, domain_id, owner_scope, owner_id,
				parent_node_id, node_path, title, level, text, token_count,
				summary, prefix_summary, start_line, end_line, start_page, end_page, content_hash, created_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
			node.ID, node.DocumentID, node.NamespaceID, node.DomainID, string(node.OwnerScope), node.OwnerID,
			node.ParentNodeID, node.NodePath, node.Title, node.Level, node.Text, node.TokenCount,
			node.Summary, node.PrefixSummary, node.StartLine, node.EndLine, node.StartPage, node.EndPage, node.ContentHash, node.CreatedAt,
		); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *PGStore) UpdateDocumentStatus(ctx context.Context, scope ManagementScope, documentID string, status DocumentStatus) error {
	if s == nil || s.pool == nil {
		return ErrInvalidInput
	}
	if err := ValidateManagementScope(scope); err != nil {
		return err
	}
	if status == "" {
		return ErrInvalidInput
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE kb_documents
		SET status = $1, updated_at = NOW()
		WHERE id = $2
		  AND domain_id = $3
		  AND owner_scope = $4
		  AND owner_id = $5`,
		string(status), strings.TrimSpace(documentID), scope.DomainID, string(scope.OwnerScope), scope.OwnerID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) GetDocument(ctx context.Context, scope Scope, documentID string) (*Document, error) {
	if err := ValidateScope(scope); err != nil {
		return nil, err
	}
	scope = normalizeScope(scope)
	doc, err := s.getDocumentByID(ctx, strings.TrimSpace(documentID))
	if err != nil {
		return nil, err
	}
	if !documentVisible(scope, *doc) {
		return nil, ErrNotFound
	}
	return doc, nil
}

func (s *PGStore) GetDocumentForManagement(ctx context.Context, scope ManagementScope, documentID string) (*Document, error) {
	if err := ValidateManagementScope(scope); err != nil {
		return nil, err
	}
	doc, err := s.getDocumentByID(ctx, strings.TrimSpace(documentID))
	if err != nil {
		return nil, err
	}
	if !documentVisibleForManagement(scope, *doc) {
		return nil, ErrNotFound
	}
	return doc, nil
}

func (s *PGStore) ListDocuments(ctx context.Context, scope Scope, query DocumentQuery) ([]Document, error) {
	if s == nil || s.pool == nil {
		return nil, ErrInvalidInput
	}
	if err := ValidateScope(scope); err != nil {
		return nil, err
	}
	scope = normalizeScope(scope)
	namespaces := effectiveNamespaceIDs(scope)
	limit := query.Limit
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, namespace_id, domain_id, owner_scope, owner_id, source_uri, title,
		       description, content_hash, version, status, effective_at, expires_at,
		       created_at, updated_at
		FROM kb_documents
		WHERE domain_id = $1
		  AND owner_scope = $2
		  AND owner_id = $3
		  AND namespace_id = ANY($4)
		  AND status = $5
		  AND effective_at <= $6
		  AND (expires_at IS NULL OR expires_at > $6)
		  AND ($7 = '' OR lower(title || ' ' || description) LIKE '%' || lower($7) || '%')
		ORDER BY updated_at DESC, id
		LIMIT $8`,
		scope.DomainID, string(scope.OwnerScope), scope.OwnerID, namespaces, string(DocumentActive), scope.Now, strings.TrimSpace(query.Query), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDocumentRows(rows)
}

func (s *PGStore) ListDocumentsForManagement(ctx context.Context, scope ManagementScope, namespaceID string, query DocumentQuery) ([]Document, error) {
	if s == nil || s.pool == nil {
		return nil, ErrInvalidInput
	}
	if err := ValidateManagementScope(scope); err != nil {
		return nil, err
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}
	status := string(query.Status)
	rows, err := s.pool.Query(ctx, `
		SELECT id, namespace_id, domain_id, owner_scope, owner_id, source_uri, title,
		       description, content_hash, version, status, effective_at, expires_at,
		       created_at, updated_at
		FROM kb_documents
		WHERE domain_id = $1
		  AND owner_scope = $2
		  AND owner_id = $3
		  AND ($4 = '' OR namespace_id = $4)
		  AND ($5 = '' OR status = $5)
		  AND ($6 = '' OR lower(title || ' ' || description) LIKE '%' || lower($6) || '%')
		ORDER BY updated_at DESC, id
		LIMIT $7`,
		scope.DomainID, string(scope.OwnerScope), scope.OwnerID, strings.TrimSpace(namespaceID), status, strings.TrimSpace(query.Query), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDocumentRows(rows)
}

func (s *PGStore) GetStructure(ctx context.Context, scope Scope, documentID string) ([]TreeNode, error) {
	if _, err := s.GetDocument(ctx, scope, documentID); err != nil {
		return nil, err
	}
	nodes, err := s.listNodes(ctx, documentID)
	if err != nil {
		return nil, err
	}
	return stripNodeText(nodes), nil
}

func (s *PGStore) GetStructureForManagement(ctx context.Context, scope ManagementScope, documentID string, includeText bool) ([]TreeNode, *Document, error) {
	doc, err := s.GetDocumentForManagement(ctx, scope, documentID)
	if err != nil {
		return nil, nil, err
	}
	nodes, err := s.listNodes(ctx, doc.ID)
	if err != nil {
		return nil, nil, err
	}
	if !includeText {
		nodes = stripNodeText(nodes)
	}
	return nodes, doc, nil
}

func (s *PGStore) GetSectionText(ctx context.Context, scope Scope, documentID string, nodeIDs []string) ([]TreeNode, *Document, error) {
	if len(nodeIDs) == 0 {
		return nil, nil, ErrInvalidInput
	}
	doc, err := s.GetDocument(ctx, scope, documentID)
	if err != nil {
		return nil, nil, err
	}
	requested := make(map[string]int, len(nodeIDs))
	ids := make([]string, 0, len(nodeIDs))
	for i, id := range nodeIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, nil, ErrInvalidInput
		}
		if _, ok := requested[id]; !ok {
			ids = append(ids, id)
		}
		requested[id] = i
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, document_id, namespace_id, domain_id, owner_scope, owner_id,
		       parent_node_id, node_path, title, level, text, token_count,
		       summary, prefix_summary, start_line, end_line, start_page, end_page, content_hash, created_at
		FROM kb_tree_nodes
		WHERE document_id = $1
		  AND id = ANY($2)
		  AND domain_id = $3
		  AND owner_scope = $4
		  AND owner_id = $5
		  AND namespace_id = ANY($6)`,
		doc.ID, ids, scope.DomainID, string(scope.OwnerScope), scope.OwnerID, effectiveNamespaceIDs(normalizeScope(scope)),
	)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	nodes, err := scanNodeRows(rows)
	if err != nil {
		return nil, nil, err
	}
	if len(nodes) != len(ids) {
		return nil, nil, ErrNotFound
	}
	sortNodesByRequest(nodes, requested)
	return nodes, doc, nil
}

func (s *PGStore) GetSectionTextByPageRanges(ctx context.Context, scope Scope, documentID string, ranges []PageRange) ([]TreeNode, *Document, error) {
	if len(ranges) == 0 {
		return nil, nil, ErrInvalidInput
	}
	doc, err := s.GetDocument(ctx, scope, documentID)
	if err != nil {
		return nil, nil, err
	}
	conditions := make([]string, 0, len(ranges))
	args := []any{doc.ID, scope.DomainID, string(scope.OwnerScope), scope.OwnerID, effectiveNamespaceIDs(normalizeScope(scope))}
	for _, r := range ranges {
		if r.Start <= 0 || r.End <= 0 || r.Start > r.End {
			return nil, nil, ErrInvalidInput
		}
		args = append(args, r.Start, r.End)
		startParam := len(args) - 1
		endParam := len(args)
		conditions = append(conditions, fmt.Sprintf("(start_page > 0 AND end_page > 0 AND start_page <= $%d AND end_page >= $%d)", endParam, startParam))
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT id, document_id, namespace_id, domain_id, owner_scope, owner_id,
		       parent_node_id, node_path, title, level, text, token_count,
		       summary, prefix_summary, start_line, end_line, start_page, end_page, content_hash, created_at
		FROM kb_tree_nodes
		WHERE document_id = $1
		  AND domain_id = $2
		  AND owner_scope = $3
		  AND owner_id = $4
		  AND namespace_id = ANY($5)
		  AND (%s)
		ORDER BY start_page ASC, id ASC`, strings.Join(conditions, " OR ")), args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	nodes, err := scanNodeRows(rows)
	if err != nil {
		return nil, nil, err
	}
	if len(nodes) == 0 {
		return nil, nil, ErrNotFound
	}
	return nodes, doc, nil
}

func (s *PGStore) SaveBinding(ctx context.Context, binding Binding) error {
	if s == nil || s.pool == nil {
		return ErrInvalidInput
	}
	if binding.ID == "" || binding.NamespaceID == "" || binding.DomainID == "" || binding.OwnerScope == "" || binding.OwnerID == "" || binding.BindingType == "" || binding.BindingTarget == "" {
		return ErrInvalidInput
	}
	now := time.Now().UTC()
	if binding.CreatedAt.IsZero() {
		binding.CreatedAt = now
	}
	if binding.UpdatedAt.IsZero() {
		binding.UpdatedAt = now
	}
	if binding.EffectiveAt.IsZero() {
		binding.EffectiveAt = now
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO kb_bindings (
			id, domain_id, owner_scope, owner_id, namespace_id, binding_type,
			binding_target, enabled, effective_at, expires_at, created_by,
			created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (id) DO UPDATE SET
			domain_id = EXCLUDED.domain_id,
			owner_scope = EXCLUDED.owner_scope,
			owner_id = EXCLUDED.owner_id,
			namespace_id = EXCLUDED.namespace_id,
			binding_type = EXCLUDED.binding_type,
			binding_target = EXCLUDED.binding_target,
			enabled = EXCLUDED.enabled,
			effective_at = EXCLUDED.effective_at,
			expires_at = EXCLUDED.expires_at,
			created_by = EXCLUDED.created_by,
			updated_at = EXCLUDED.updated_at`,
		binding.ID, binding.DomainID, string(binding.OwnerScope), binding.OwnerID, binding.NamespaceID,
		string(binding.BindingType), binding.BindingTarget, binding.Enabled, binding.EffectiveAt,
		binding.ExpiresAt, binding.CreatedBy, binding.CreatedAt, binding.UpdatedAt,
	)
	return err
}

func (s *PGStore) ListBindings(ctx context.Context, input BindingResolveInput) ([]Binding, error) {
	if s == nil || s.pool == nil {
		return nil, ErrInvalidInput
	}
	if input.DomainID == "" || input.OwnerScope == "" || input.OwnerID == "" {
		return nil, ErrInvalidScope
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	targets := bindingTargets(input)
	if len(targets) == 0 {
		return nil, nil
	}
	types := make([]string, 0, len(targets))
	values := make([]string, 0, len(targets))
	for typ, target := range targets {
		types = append(types, string(typ))
		values = append(values, target)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, domain_id, owner_scope, owner_id, namespace_id, binding_type,
		       binding_target, enabled, effective_at, expires_at, created_by,
		       created_at, updated_at
		FROM kb_bindings
		WHERE domain_id = $1
		  AND owner_scope = $2
		  AND owner_id = $3
		  AND binding_type = ANY($4)
		  AND binding_target = ANY($5)
		  AND enabled = TRUE
		  AND effective_at <= $6
		  AND (expires_at IS NULL OR expires_at > $6)
		ORDER BY id`,
		input.DomainID, string(input.OwnerScope), input.OwnerID, types, values, now,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Binding
	for rows.Next() {
		binding, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		if binding.BindingTarget == targets[binding.BindingType] {
			out = append(out, binding)
		}
	}
	return out, rows.Err()
}

func (s *PGStore) ListBindingsForManagement(ctx context.Context, query BindingQuery) ([]Binding, error) {
	if s == nil || s.pool == nil {
		return nil, ErrInvalidInput
	}
	if query.DomainID == "" || query.OwnerScope == "" || query.OwnerID == "" {
		return nil, ErrInvalidScope
	}
	bindingTypeFilter := ""
	bindingTargetFilter := ""
	enabledFilter := ""
	argPos := 5
	args := []any{query.DomainID, string(query.OwnerScope), query.OwnerID, strings.TrimSpace(query.NamespaceID)}
	if query.BindingType != "" {
		bindingTypeFilter = fmt.Sprintf("AND binding_type = $%d", argPos)
		args = append(args, string(query.BindingType))
		argPos++
	}
	if strings.TrimSpace(query.BindingTarget) != "" {
		bindingTargetFilter = fmt.Sprintf("AND binding_target = $%d", argPos)
		args = append(args, strings.TrimSpace(query.BindingTarget))
		argPos++
	}
	if query.Enabled != nil {
		enabledFilter = fmt.Sprintf("AND enabled = $%d", argPos)
		args = append(args, *query.Enabled)
	}
	sql := fmt.Sprintf(`
		SELECT id, domain_id, owner_scope, owner_id, namespace_id, binding_type,
		       binding_target, enabled, effective_at, expires_at, created_by,
		       created_at, updated_at
		FROM kb_bindings
		WHERE domain_id = $1
		  AND owner_scope = $2
		  AND owner_id = $3
		  AND ($4 = '' OR namespace_id = $4)
		  %s
		  %s
		  %s
		ORDER BY updated_at DESC, id`, bindingTypeFilter, bindingTargetFilter, enabledFilter)
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Binding, 0)
	for rows.Next() {
		binding, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, binding)
	}
	return out, rows.Err()
}

func (s *PGStore) FindActiveBindingDomains(ctx context.Context, query BindingQuery, now time.Time) ([]string, error) {
	if s == nil || s.pool == nil {
		return nil, ErrInvalidInput
	}
	if query.OwnerScope == "" || query.OwnerID == "" || query.BindingType == "" || strings.TrimSpace(query.BindingTarget) == "" {
		return nil, ErrInvalidScope
	}
	if now.IsZero() {
		now = time.Now()
	}
	args := []any{string(query.OwnerScope), query.OwnerID, string(query.BindingType), strings.TrimSpace(query.BindingTarget), now}
	domainFilter := ""
	if strings.TrimSpace(query.DomainID) != "" {
		domainFilter = "AND domain_id = $6"
		args = append(args, strings.TrimSpace(query.DomainID))
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT DISTINCT domain_id
		FROM kb_bindings
		WHERE owner_scope = $1
		  AND owner_id = $2
		  AND binding_type = $3
		  AND binding_target = $4
		  AND enabled = TRUE
		  AND effective_at <= $5
		  AND (expires_at IS NULL OR expires_at > $5)
		  %s
		ORDER BY domain_id`, domainFilter), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var domains []string
	for rows.Next() {
		var domain string
		if err := rows.Scan(&domain); err != nil {
			return nil, err
		}
		if strings.TrimSpace(domain) != "" {
			domains = append(domains, domain)
		}
	}
	return domains, rows.Err()
}

func (s *PGStore) SaveEvidenceEvent(ctx context.Context, event EvidenceEvent) error {
	if s == nil || s.pool == nil {
		return ErrInvalidInput
	}
	if event.ID == "" || event.EvidenceToken == "" || event.SessionID == "" || event.TurnID == "" || event.TraceID == "" ||
		event.DomainID == "" || event.NamespaceID == "" || event.DocumentID == "" || event.DocumentVersion == "" ||
		event.NodeID == "" || event.OwnerScope == "" || event.OwnerID == "" {
		return ErrInvalidInput
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx, `
			INSERT INTO kb_evidence_events (
				id, session_id, turn_id, trace_id, domain_id, namespace_id,
				document_id, document_version, node_id, node_path, start_page, end_page,
				owner_scope, owner_id, evidence_token, citation_text, verified, created_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
			ON CONFLICT (id) DO NOTHING`,
		event.ID, event.SessionID, event.TurnID, event.TraceID, event.DomainID, event.NamespaceID,
		event.DocumentID, event.DocumentVersion, event.NodeID, event.NodePath, event.StartPage, event.EndPage,
		string(event.OwnerScope), event.OwnerID, event.EvidenceToken, event.CitationText, event.Verified, event.CreatedAt,
	)
	return err
}

func (s *PGStore) ListEvidenceEvents(ctx context.Context, scope EvidenceScope) ([]EvidenceEvent, error) {
	if s == nil || s.pool == nil {
		return nil, ErrInvalidInput
	}
	if err := ValidateEvidenceScope(scope); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
			SELECT id, session_id, turn_id, trace_id, domain_id, namespace_id,
			       document_id, document_version, node_id, node_path, start_page, end_page,
			       owner_scope, owner_id, evidence_token, citation_text, verified, created_at
			FROM kb_evidence_events
		WHERE session_id = $1
		  AND turn_id = $2
		  AND trace_id = $3
		  AND domain_id = $4
		  AND owner_scope = $5
		  AND owner_id = $6
		ORDER BY created_at ASC, id`,
		scope.SessionID, scope.TurnID, scope.TraceID, scope.DomainID, string(scope.OwnerScope), scope.OwnerID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []EvidenceEvent
	for rows.Next() {
		event, err := scanEvidence(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s *PGStore) SaveNodeAssets(ctx context.Context, assets []NodeAsset) error {
	if s == nil || s.pool == nil {
		return ErrInvalidInput
	}
	if len(assets) == 0 {
		return nil
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	for _, asset := range assets {
		if asset.ID == "" || asset.DocumentID == "" || asset.NamespaceID == "" || asset.DomainID == "" || asset.OwnerScope == "" || asset.OwnerID == "" || asset.AssetURI == "" {
			return ErrInvalidInput
		}
		if asset.CreatedAt.IsZero() {
			asset.CreatedAt = time.Now().UTC()
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO kb_node_assets (
				id, owner_scope, owner_id, domain_id, namespace_id, document_id,
				node_id, line, page, asset_uri, content_hash, mime_type, alt_text, caption, created_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
			ON CONFLICT (id) DO UPDATE SET
				node_id = EXCLUDED.node_id,
				line = EXCLUDED.line,
				page = EXCLUDED.page,
				asset_uri = EXCLUDED.asset_uri,
				content_hash = EXCLUDED.content_hash,
				mime_type = EXCLUDED.mime_type,
				alt_text = EXCLUDED.alt_text,
				caption = EXCLUDED.caption`,
			asset.ID, string(asset.OwnerScope), asset.OwnerID, asset.DomainID, asset.NamespaceID, asset.DocumentID,
			asset.NodeID, asset.Line, asset.Page, asset.AssetURI, asset.ContentHash, asset.MimeType, asset.AltText, asset.Caption, asset.CreatedAt,
		); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *PGStore) DeleteNodeAssetsForDocument(ctx context.Context, scope ManagementScope, documentID string) error {
	if s == nil || s.pool == nil {
		return ErrInvalidInput
	}
	if err := ValidateManagementScope(scope); err != nil {
		return err
	}
	if strings.TrimSpace(documentID) == "" {
		return ErrInvalidInput
	}
	_, err := s.pool.Exec(ctx, `
		DELETE FROM kb_node_assets
		WHERE document_id = $1
		  AND domain_id = $2
		  AND owner_scope = $3
		  AND owner_id = $4`,
		strings.TrimSpace(documentID), scope.DomainID, string(scope.OwnerScope), scope.OwnerID,
	)
	return err
}

func (s *PGStore) ListNodeAssets(ctx context.Context, scope ManagementScope, documentID string, nodeIDs []string) ([]NodeAsset, error) {
	if s == nil || s.pool == nil {
		return nil, ErrInvalidInput
	}
	if err := ValidateManagementScope(scope); err != nil {
		return nil, err
	}
	if strings.TrimSpace(documentID) == "" {
		return nil, ErrInvalidInput
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, owner_scope, owner_id, domain_id, namespace_id, document_id,
		       node_id, line, page, asset_uri, content_hash, mime_type, alt_text, caption, created_at
		FROM kb_node_assets
		WHERE document_id = $1
		  AND domain_id = $2
		  AND owner_scope = $3
		  AND owner_id = $4
		  AND (cardinality($5::text[]) = 0 OR node_id = ANY($5::text[]))
		ORDER BY node_id, page, line, asset_uri`,
		strings.TrimSpace(documentID), scope.DomainID, string(scope.OwnerScope), scope.OwnerID, uniqueNonEmpty(nodeIDs),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]NodeAsset, 0)
	for rows.Next() {
		asset, err := scanNodeAsset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, asset)
	}
	return out, rows.Err()
}

func (s *PGStore) getDocumentByID(ctx context.Context, documentID string) (*Document, error) {
	if s == nil || s.pool == nil {
		return nil, ErrInvalidInput
	}
	row := s.pool.QueryRow(ctx, `
		SELECT id, namespace_id, domain_id, owner_scope, owner_id, source_uri, title,
		       description, content_hash, version, status, effective_at, expires_at,
		       created_at, updated_at
		FROM kb_documents
		WHERE id = $1`, documentID)
	return scanDocument(row)
}

func (s *PGStore) listNodes(ctx context.Context, documentID string) ([]TreeNode, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, document_id, namespace_id, domain_id, owner_scope, owner_id,
		       parent_node_id, node_path, title, level, text, token_count,
		       summary, prefix_summary, start_line, end_line, start_page, end_page, content_hash, created_at
		FROM kb_tree_nodes
		WHERE document_id = $1
		ORDER BY id`, documentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodeRows(rows)
}

type pgScanner interface {
	Scan(dest ...any) error
}

func scanNamespace(row pgScanner) (*Namespace, error) {
	var ns Namespace
	if err := row.Scan(
		&ns.ID, &ns.Name, &ns.DomainID, &ns.OwnerScope, &ns.OwnerID, &ns.IndexStrategy,
		&ns.ThinningEnabled, &ns.ThinningTokenThreshold, &ns.SummaryTokenThreshold,
		&ns.SummaryModel, &ns.CreatedAt, &ns.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &ns, nil
}

func scanDocument(row pgScanner) (*Document, error) {
	var doc Document
	if err := row.Scan(
		&doc.ID, &doc.NamespaceID, &doc.DomainID, &doc.OwnerScope, &doc.OwnerID,
		&doc.SourceURI, &doc.Title, &doc.Description, &doc.ContentHash, &doc.Version,
		&doc.Status, &doc.EffectiveAt, &doc.ExpiresAt, &doc.CreatedAt, &doc.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &doc, nil
}

func scanDocumentRows(rows pgx.Rows) ([]Document, error) {
	var docs []Document
	for rows.Next() {
		doc, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		docs = append(docs, *doc)
	}
	return docs, rows.Err()
}

func scanNodeRows(rows pgx.Rows) ([]TreeNode, error) {
	var nodes []TreeNode
	for rows.Next() {
		var node TreeNode
		if err := rows.Scan(
			&node.ID, &node.DocumentID, &node.NamespaceID, &node.DomainID, &node.OwnerScope, &node.OwnerID,
			&node.ParentNodeID, &node.NodePath, &node.Title, &node.Level, &node.Text, &node.TokenCount,
			&node.Summary, &node.PrefixSummary, &node.StartLine, &node.EndLine, &node.StartPage, &node.EndPage, &node.ContentHash, &node.CreatedAt,
		); err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}

func scanBinding(row pgScanner) (Binding, error) {
	var binding Binding
	if err := row.Scan(
		&binding.ID, &binding.DomainID, &binding.OwnerScope, &binding.OwnerID,
		&binding.NamespaceID, &binding.BindingType, &binding.BindingTarget,
		&binding.Enabled, &binding.EffectiveAt, &binding.ExpiresAt, &binding.CreatedBy,
		&binding.CreatedAt, &binding.UpdatedAt,
	); err != nil {
		return Binding{}, err
	}
	return binding, nil
}

func scanEvidence(row pgScanner) (EvidenceEvent, error) {
	var event EvidenceEvent
	if err := row.Scan(
		&event.ID, &event.SessionID, &event.TurnID, &event.TraceID, &event.DomainID,
		&event.NamespaceID, &event.DocumentID, &event.DocumentVersion, &event.NodeID,
		&event.NodePath, &event.StartPage, &event.EndPage, &event.OwnerScope, &event.OwnerID,
		&event.EvidenceToken, &event.CitationText, &event.Verified, &event.CreatedAt,
	); err != nil {
		return EvidenceEvent{}, err
	}
	return event, nil
}

func scanNodeAsset(row pgScanner) (NodeAsset, error) {
	var asset NodeAsset
	if err := row.Scan(
		&asset.ID, &asset.OwnerScope, &asset.OwnerID, &asset.DomainID,
		&asset.NamespaceID, &asset.DocumentID, &asset.NodeID, &asset.Line, &asset.Page, &asset.AssetURI,
		&asset.ContentHash, &asset.MimeType, &asset.AltText, &asset.Caption,
		&asset.CreatedAt,
	); err != nil {
		return NodeAsset{}, err
	}
	return asset, nil
}

func normalizeNodeForDocument(node TreeNode, doc Document, now time.Time) TreeNode {
	if node.DocumentID == "" {
		node.DocumentID = doc.ID
	}
	if node.NamespaceID == "" {
		node.NamespaceID = doc.NamespaceID
	}
	if node.DomainID == "" {
		node.DomainID = doc.DomainID
	}
	if node.OwnerScope == "" {
		node.OwnerScope = doc.OwnerScope
	}
	if node.OwnerID == "" {
		node.OwnerID = doc.OwnerID
	}
	if node.CreatedAt.IsZero() {
		node.CreatedAt = now
	}
	return node
}

func sortNodesByRequest(nodes []TreeNode, requested map[string]int) {
	for i := 0; i < len(nodes)-1; i++ {
		for j := i + 1; j < len(nodes); j++ {
			if requested[nodes[j].ID] < requested[nodes[i].ID] {
				nodes[i], nodes[j] = nodes[j], nodes[i]
			}
		}
	}
}

func StableDocumentID(namespaceID, contentHash string) string {
	payload := strings.TrimSpace(namespaceID) + "\x00" + strings.TrimSpace(contentHash)
	return fmt.Sprintf("doc_%s", hashText(payload)[:16])
}
