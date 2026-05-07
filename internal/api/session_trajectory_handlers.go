package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"

	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/trajectory"
)

var trajectoryStores sync.Map // map[*Server]trajectory.Store

type ForkFromStepRequest struct {
	SnapshotSeq int    `json:"snapshot_seq"`
	ForkName    string `json:"fork_name,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
}

type ForkFromStepResponse struct {
	ForkID       string `json:"fork_id"`
	ForkName     string `json:"fork_name"`
	SnapshotSeq  int    `json:"snapshot_seq"`
	MessageCount int    `json:"message_count"`
	Message      string `json:"message"`
}

func (s *Server) handleGetSessionTrajectory(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "需要会话 ID", Code: errs.CodeBadRequest})
		return
	}
	trajStore := s.getTrajectoryStore()
	if _, ok := s.loadAuthorizedSession(w, r, sessionID); !ok {
		return
	}
	snapshotSeq, err := strconv.Atoi(r.PathValue("step"))
	if err != nil || snapshotSeq <= 0 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "step 必须为正整数 snapshot_seq", Code: errs.CodeBadRequest})
		return
	}
	snapshot, err := trajStore.Get(r.Context(), sessionID, snapshotSeq)
	if err != nil {
		if errors.Is(err, trajectory.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "trajectory snapshot 不存在", Code: errs.CodeNotFound})
			return
		}
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "读取 trajectory snapshot 失败", Code: errs.CodeInternal})
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) handleForkFromStep(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "需要会话 ID", Code: errs.CodeBadRequest})
		return
	}
	trajStore := s.getTrajectoryStore()
	sourceSession, ok := s.loadAuthorizedSession(w, r, sessionID)
	if !ok {
		return
	}
	var req ForkFromStepRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "无效的请求体", Code: errs.CodeBadRequest})
		return
	}
	if req.SnapshotSeq <= 0 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "snapshot_seq 必须为正整数", Code: errs.CodeBadRequest})
		return
	}
	snapshot, err := trajStore.Get(r.Context(), sessionID, req.SnapshotSeq)
	if err != nil {
		if errors.Is(err, trajectory.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "trajectory snapshot 不存在", Code: errs.CodeNotFound})
			return
		}
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "读取 trajectory snapshot 失败", Code: errs.CodeInternal})
		return
	}
	forkID, forkName, messageCount, err := s.master.ForkSessionFromSnapshotMessages(
		r.Context(),
		sourceSession,
		req.ForkName,
		req.SnapshotSeq,
		snapshot.Messages,
		req.Prompt,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	writeJSON(w, http.StatusCreated, ForkFromStepResponse{
		ForkID:       forkID,
		ForkName:     forkName,
		SnapshotSeq:  req.SnapshotSeq,
		MessageCount: messageCount,
		Message:      "已从 trajectory snapshot 创建分支",
	})
}

func (s *Server) SetTrajectoryStore(store trajectory.Store) {
	if store == nil {
		trajectoryStores.Delete(s)
		if s != nil && s.master != nil {
			s.master.SetTrajectoryStore(nil)
		}
		return
	}
	trajectoryStores.Store(s, store)
	if s != nil && s.master != nil {
		s.master.SetTrajectoryStore(store)
	}
}

func (s *Server) getTrajectoryStore() trajectory.Store {
	if s == nil {
		return trajectory.NoopStore{}
	}
	if value, ok := trajectoryStores.Load(s); ok {
		if store, ok := value.(trajectory.Store); ok && store != nil {
			return store
		}
	}
	if pgStore, ok := s.store.(*store.PostgresStore); ok && pgStore.Pool() != nil {
		store := trajectory.NewPGStore(pgStore.Pool())
		trajectoryStores.Store(s, store)
		if s.master != nil {
			s.master.SetTrajectoryStore(store)
		}
		return store
	}
	store := trajectory.NewMemoryStore()
	trajectoryStores.Store(s, store)
	if s.master != nil {
		s.master.SetTrajectoryStore(store)
	}
	return store
}
