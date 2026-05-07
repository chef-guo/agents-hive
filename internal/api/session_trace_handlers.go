package api

import (
	"net/http"
	"strconv"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/observability"
	"github.com/chef-guo/agents-hive/internal/store"
)

func (s *Server) SetTraceReader(reader observability.TraceReader) {
	s.traceReader = reader
}

func (s *Server) loadAuthorizedSession(w http.ResponseWriter, r *http.Request, sessionID string) (*store.SessionRecord, bool) {
	session, err := s.master.GetSessionByID(r.Context(), sessionID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "会话不存在或无权访问", Code: errs.CodeNotFound})
		return nil, false
	}
	if !s.checkSessionOwnership(w, r, session) {
		return nil, false
	}
	return session, true
}

func (s *Server) handleGetSessionTrace(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "需要 session ID", Code: errs.CodeBadRequest})
		return
	}
	if s.traceReader == nil {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "session trace reader 未初始化", Code: errs.CodeUnavailable})
		return
	}
	if _, ok := s.loadAuthorizedSession(w, r, sessionID); !ok {
		return
	}
	timeline, err := s.traceReader.GetSessionTimeline(r.Context(), sessionID, parseSessionTraceLimit(r.URL.Query().Get("limit")))
	if err != nil {
		s.logger.Error("读取 session trace 失败", zap.String("session_id", sessionID), zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "读取 session trace 失败", Code: errs.CodeStoreReadFailed})
		return
	}
	if timeline.Items == nil {
		timeline.Items = []observability.TraceTimelineItem{}
	}
	writeJSON(w, http.StatusOK, timeline)
}

func parseSessionTraceLimit(raw string) int {
	const defaultLimit = 2000
	const maxLimit = 2000
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultLimit
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}
