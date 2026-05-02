package api

import (
	"context"
	"errors"
	"net/http"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/sessiontodo"
	"github.com/chef-guo/agents-hive/internal/store"
)

type sessionTodoSnapshotStore interface {
	Snapshot(ctx context.Context, sessionID string) (sessiontodo.Snapshot, error)
}

func (s *Server) SetSessionTodoStore(store sessionTodoSnapshotStore) {
	s.sessionTodoStore = store
}

func (s *Server) handleGetSessionTodos(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "需要会话 ID", Code: errs.CodeBadRequest})
		return
	}

	if s.config == nil || !s.config.Agent.PlanRuntime.Enabled {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "session todos feature disabled", Code: errs.CodeNotFound})
		return
	}
	if s.sessionTodoStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "session todo store 未初始化", Code: errs.CodeUnavailable})
		return
	}
	if s.master != nil {
		sess, err := s.master.GetSessionByID(r.Context(), sessionID)
		if err != nil {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "会话不存在或无权访问", Code: errs.CodeNotFound})
			return
		}
		if !s.checkSessionOwnership(w, r, sess) {
			return
		}
	}

	snapshot, err := s.sessionTodoStore.Snapshot(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) || errs.IsCode(err, errs.CodeNotFound) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "session todo snapshot 未找到", Code: errs.CodeNotFound})
			return
		}
		if s.logger != nil {
			s.logger.Error("查询 session todo snapshot 失败", zap.String("session_id", sessionID), zap.Error(err))
		}
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "查询 session todo snapshot 失败", Code: errs.CodeStoreReadFailed})
		return
	}
	if snapshot.Todos == nil {
		snapshot.Todos = []sessiontodo.Todo{}
	}
	writeJSON(w, http.StatusOK, snapshot)
}
