package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/sessiontodo"
)

func (s *Server) handleAdminSessionTodoOpsSnapshot(w http.ResponseWriter, r *http.Request) {
	if s.config == nil || !s.config.Agent.PlanRuntime.Enabled {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "sessiontodo observability disabled", Code: errs.CodeNotFound})
		return
	}
	if s.sessionTodoOpsReader == nil {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "sessiontodo observability reader 未初始化", Code: errs.CodeUnavailable})
		return
	}
	now := time.Now()
	window := parseOpsWindow(r, 24*time.Hour)
	until := now
	if raw := r.URL.Query().Get("until"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "until 必须是 RFC3339 时间", Code: errs.CodeBadRequest})
			return
		}
		until = parsed
	}
	since := until.Add(-window)
	if raw := r.URL.Query().Get("since"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "since 必须是 RFC3339 时间", Code: errs.CodeBadRequest})
			return
		}
		since = parsed
		window = until.Sub(since)
		if window <= 0 {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "since 必须早于 until", Code: errs.CodeBadRequest})
			return
		}
	}

	input, err := s.sessionTodoOpsReader.LoadOps(r.Context(), since, until)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "读取 sessiontodo observability 失败", Code: errs.CodeStoreReadFailed})
		return
	}
	input.Now = until
	input.Window = window
	writeJSON(w, http.StatusOK, sessiontodo.BuildOpsSnapshot(input, sessiontodo.DefaultOpsAlertThresholds()))
}

func parseOpsWindow(r *http.Request, fallback time.Duration) time.Duration {
	if raw := r.URL.Query().Get("window_minutes"); raw != "" {
		minutes, err := strconv.Atoi(raw)
		if err == nil && minutes > 0 && minutes <= 7*24*60 {
			return time.Duration(minutes) * time.Minute
		}
	}
	return fallback
}
