package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/kb"
)

type sessionKBBindingsRequest struct {
	NamespaceIDs []string `json:"namespace_ids"`
}

func (s *Server) handleGetSessionKBBindings(w http.ResponseWriter, r *http.Request) {
	sessionID, scope, ok := s.sessionKBBindingScope(w, r)
	if !ok {
		return
	}
	bindings, err := s.kbService.ListBindingsForManagement(r.Context(), scope, kb.BindingQuery{
		BindingType:   kb.BindingTypeSession,
		BindingTarget: sessionID,
		Enabled:       boolPtr(true),
	})
	if err != nil {
		s.writeKBError(w, err)
		return
	}
	out := make([]kbBindingResponse, 0)
	for _, binding := range bindings {
		if binding.BindingType == kb.BindingTypeSession && binding.BindingTarget == sessionID {
			out = append(out, kbBindingToResponse(binding))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"bindings": out})
}

func (s *Server) handlePutSessionKBBindings(w http.ResponseWriter, r *http.Request) {
	sessionID, scope, ok := s.sessionKBBindingScope(w, r)
	if !ok {
		return
	}
	var req sessionKBBindingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "无效的请求体", Code: errs.CodeBadRequest})
		return
	}
	namespaceIDs := uniqueKBStrings(req.NamespaceIDs)
	existing, err := s.kbService.ListBindingsForManagement(r.Context(), scope, kb.BindingQuery{
		BindingType:   kb.BindingTypeSession,
		BindingTarget: sessionID,
		Enabled:       boolPtr(true),
	})
	if err != nil {
		s.writeKBError(w, err)
		return
	}
	wanted := make(map[string]struct{}, len(namespaceIDs))
	for _, namespaceID := range namespaceIDs {
		wanted[namespaceID] = struct{}{}
	}
	current := make(map[string]kb.Binding)
	for _, binding := range existing {
		if binding.BindingType != kb.BindingTypeSession || binding.BindingTarget != sessionID {
			continue
		}
		if _, ok := wanted[binding.NamespaceID]; ok {
			current[binding.NamespaceID] = binding
			continue
		}
		if _, err := s.kbService.DisableBinding(r.Context(), scope, binding.ID); err != nil {
			s.writeKBError(w, err)
			return
		}
	}
	for _, namespaceID := range namespaceIDs {
		if _, ok := current[namespaceID]; ok {
			continue
		}
		binding, err := s.kbService.CreateBinding(r.Context(), scope, kb.CreateBindingInput{
			NamespaceID:   namespaceID,
			DomainID:      scope.DomainID,
			BindingType:   kb.BindingTypeSession,
			BindingTarget: sessionID,
			CreatedBy:     auth.UserIDFrom(r.Context()),
		})
		if err != nil {
			s.writeKBError(w, err)
			return
		}
		current[namespaceID] = *binding
	}
	if err := s.updateSessionKBDomain(r.Context(), sessionID, scope.DomainID, len(namespaceIDs) > 0); err != nil {
		s.writeKBError(w, err)
		return
	}
	bindings, err := s.listActiveSessionKBBindings(r.Context(), scope, sessionID)
	if err != nil {
		s.writeKBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bindings": bindings})
}

func (s *Server) handleDeleteSessionKBBinding(w http.ResponseWriter, r *http.Request) {
	sessionID, scope, ok := s.sessionKBBindingScope(w, r)
	if !ok {
		return
	}
	namespaceID := strings.TrimSpace(r.PathValue("namespace_id"))
	if namespaceID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "需要 namespace ID", Code: errs.CodeBadRequest})
		return
	}
	bindings, err := s.kbService.ListBindingsForManagement(r.Context(), scope, kb.BindingQuery{
		NamespaceID:   namespaceID,
		BindingType:   kb.BindingTypeSession,
		BindingTarget: sessionID,
		Enabled:       boolPtr(true),
	})
	if err != nil {
		s.writeKBError(w, err)
		return
	}
	for _, binding := range bindings {
		if binding.BindingType != kb.BindingTypeSession || binding.BindingTarget != sessionID {
			continue
		}
		if _, err := s.kbService.DisableBinding(r.Context(), scope, binding.ID); err != nil {
			s.writeKBError(w, err)
			return
		}
	}
	remaining, err := s.listActiveSessionKBBindings(r.Context(), scope, sessionID)
	if err != nil {
		s.writeKBError(w, err)
		return
	}
	if len(remaining) == 0 {
		if err := s.updateSessionKBDomain(r.Context(), sessionID, scope.DomainID, false); err != nil {
			s.writeKBError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"bindings": remaining})
}

func (s *Server) listActiveSessionKBBindings(ctx context.Context, scope kb.ManagementScope, sessionID string) ([]kbBindingResponse, error) {
	bindings, err := s.kbService.ListBindingsForManagement(ctx, scope, kb.BindingQuery{
		BindingType:   kb.BindingTypeSession,
		BindingTarget: sessionID,
		Enabled:       boolPtr(true),
	})
	if err != nil {
		return nil, err
	}
	out := make([]kbBindingResponse, 0, len(bindings))
	for _, binding := range bindings {
		if binding.BindingType == kb.BindingTypeSession && binding.BindingTarget == sessionID {
			out = append(out, kbBindingToResponse(binding))
		}
	}
	return out, nil
}

func (s *Server) updateSessionKBDomain(ctx context.Context, sessionID, domainID string, enabled bool) error {
	if s == nil || s.master == nil {
		return nil
	}
	return s.master.SetSessionKBDomain(ctx, sessionID, domainID, enabled)
}

func (s *Server) sessionKBBindingScope(w http.ResponseWriter, r *http.Request) (string, kb.ManagementScope, bool) {
	if !s.requireKBService(w) {
		return "", kb.ManagementScope{}, false
	}
	sessionID := strings.TrimSpace(r.PathValue("id"))
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "需要会话 ID", Code: errs.CodeBadRequest})
		return "", kb.ManagementScope{}, false
	}
	session, err := s.master.GetSessionByID(r.Context(), sessionID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "会话不存在或无权访问", Code: errs.CodeNotFound})
		return "", kb.ManagementScope{}, false
	}
	if !s.checkSessionOwnership(w, r, session) {
		return "", kb.ManagementScope{}, false
	}
	scope, ok := kbManagementScope(w, r)
	if !ok {
		return "", kb.ManagementScope{}, false
	}
	return sessionID, scope, true
}

func boolPtr(v bool) *bool {
	return &v
}
