package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/master"
	"github.com/chef-guo/agents-hive/internal/observability"
	"github.com/chef-guo/agents-hive/internal/sessiontodo"
	"github.com/chef-guo/agents-hive/internal/store"
)

type sessionTodoSnapshotStore interface {
	Snapshot(ctx context.Context, sessionID string) (sessiontodo.Snapshot, error)
	ClaimResume(ctx context.Context, sessionID string, expectedPlanVersion int64, expectedRuntimeEpoch, runtimeEpoch, turnID string) (sessiontodo.Snapshot, error)
}

func (s *Server) SetSessionTodoStore(store sessionTodoSnapshotStore) {
	s.sessionTodoStore = store
}

func (s *Server) SetSessionTodoOpsReader(reader sessionTodoOpsReader) {
	s.sessionTodoOpsReader = reader
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

func (s *Server) handlePlanSessionTodoResume(w http.ResponseWriter, r *http.Request) {
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
	var body struct {
		Mode                 string `json:"mode"`
		Execute              bool   `json:"execute"`
		ExpectedPlanVersion  int64  `json:"expected_plan_version"`
		ExpectedRuntimeEpoch string `json:"expected_runtime_epoch"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Mode == "" {
		body.Mode = string(sessiontodo.ResumeModeManual)
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
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "查询 session todo snapshot 失败", Code: errs.CodeStoreReadFailed})
		return
	}
	action := sessiontodo.PlanResumeAction(snapshot, sessiontodo.ResumeOptions{
		Mode:                 sessiontodo.ResumeMode(body.Mode),
		BudgetOK:             true,
		RuntimeEpoch:         masterRuntimeEpoch(s.master),
		ExpectedRuntimeEpoch: body.ExpectedRuntimeEpoch,
		Execute:              body.Execute,
	})
	if !action.Allowed {
		writeJSON(w, http.StatusConflict, action)
		return
	}
	if body.Execute {
		if s.master == nil {
			writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "master 未初始化", Code: errs.CodeUnavailable})
			return
		}
		if body.ExpectedPlanVersion != snapshot.PlanVersion {
			writeJSON(w, http.StatusConflict, ErrorResponse{Error: "expected_plan_version 已过期", Code: errs.CodeFailedPrecondition})
			return
		}
		turnID := observability.NewTraceID()
		claimed, err := s.sessionTodoStore.ClaimResume(r.Context(), sessionID, body.ExpectedPlanVersion, body.ExpectedRuntimeEpoch, masterRuntimeEpoch(s.master), turnID)
		if err != nil {
			writeJSON(w, http.StatusConflict, sessiontodo.ResumeAction{
				Allowed:      false,
				Mode:         action.Mode,
				Reason:       err.Error(),
				PlanVersion:  snapshot.PlanVersion,
				RuntimeEpoch: snapshot.RuntimeEpoch,
			})
			return
		}
		if err := s.master.BroadcastTodoSnapshot(r.Context(), claimed); err != nil {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "resume snapshot 广播失败: " + err.Error(), Code: errs.CodeInternal})
			return
		}
		bgCtx := context.Background()
		if user := auth.UserFrom(r.Context()); user != nil {
			bgCtx = auth.WithUser(bgCtx, user)
		}
		if auth.IsAuthEnabled(r.Context()) {
			bgCtx = auth.WithAuthEnabled(bgCtx)
		}
		go func() {
			if _, err := s.master.ProcessMessageWithOptions(bgCtx, sessionID, action.Prompt, master.WithTurnID(turnID)); err != nil {
				s.master.RestorePausedAfterResumeFailure(context.Background(), sessionID, claimed.PlanVersion, claimed.RuntimeEpoch, claimed.TurnID, "manual resume process failed", err)
				if s.logger != nil {
					s.logger.Error("sessiontodo resume enqueue failed", zap.String("session_id", sessionID), zap.String("turn_id", turnID), zap.Error(err))
				}
			}
		}()
		writeJSON(w, http.StatusOK, map[string]any{"action": action, "snapshot": claimed})
		return
	}
	writeJSON(w, http.StatusOK, action)
}

func masterRuntimeEpoch(m *master.Master) string {
	if m == nil {
		return ""
	}
	return m.RuntimeEpoch()
}
