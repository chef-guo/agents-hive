package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/channel"
	"github.com/chef-guo/agents-hive/internal/channel/push"
	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/master"
	"github.com/chef-guo/agents-hive/internal/store"
)

const scheduledTaskQuotaPerUser = 100

type scheduledTaskStore interface {
	SaveScheduledTask(ctx context.Context, rec *store.ScheduledTask) error
	GetScheduledTask(ctx context.Context, id string) (*store.ScheduledTask, error)
	DeleteScheduledTask(ctx context.Context, id string) error
	ListScheduledTasksByUser(ctx context.Context, createdBy string) ([]*store.ScheduledTask, error)
	ListAllScheduledTasks(ctx context.Context) ([]*store.ScheduledTask, error)
	ListScheduledTaskRuns(ctx context.Context, taskID string, limit int) ([]*store.ScheduledTaskRun, error)
	ClaimManualScheduledTaskRun(ctx context.Context, taskID string, now time.Time, runID string, leaseUntil time.Time, claimedBy string) (*store.ScheduledTaskRun, error)
	FinishScheduledTaskRun(ctx context.Context, run *store.ScheduledTaskRun) error
}

type scheduledTaskRequest struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	TargetType   string         `json:"target_type"`
	TargetConfig map[string]any `json:"target_config"`
	Platform     string         `json:"platform"`
	Prompt       string         `json:"prompt"`
	CronExpr     string         `json:"cron_expr"`
	IntervalSec  int            `json:"interval_sec"`
	Timezone     string         `json:"timezone"`
	Enabled      bool           `json:"enabled"`
}

type scheduledTaskToggleRequest struct {
	Enabled bool `json:"enabled"`
}

type scheduledTaskQuotaResponse struct {
	Error   string `json:"error"`
	Code    int    `json:"code"`
	Limit   int    `json:"limit"`
	Current int    `json:"current"`
	Message string `json:"message"`
}

func (s *Server) handleCreateScheduledTask(w http.ResponseWriter, r *http.Request) {
	taskStore, ok := s.requireScheduledTaskStore(w, r)
	if !ok {
		return
	}
	user, ok := requireScheduledTaskUser(w, r)
	if !ok {
		return
	}
	existing, err := taskStore.ListScheduledTasksByUser(r.Context(), user.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	if user.Role != "admin" && len(existing) >= scheduledTaskQuotaPerUser {
		writeJSON(w, http.StatusTooManyRequests, scheduledTaskQuotaResponse{
			Error:   "scheduled_tasks_quota_exceeded",
			Code:    http.StatusTooManyRequests,
			Limit:   scheduledTaskQuotaPerUser,
			Current: len(existing),
			Message: "已达到每用户 100 个定时任务上限,请删除旧任务或联系管理员",
		})
		return
	}
	var req scheduledTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "无效的请求体", Code: errs.CodeBadRequest})
		return
	}
	task, err := s.buildScheduledTaskFromRequest(req, nil, user)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error(), Code: errs.CodeBadRequest})
		return
	}
	if err := taskStore.SaveScheduledTask(r.Context(), task); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	writeJSON(w, http.StatusCreated, task)
}

func (s *Server) handleListScheduledTasks(w http.ResponseWriter, r *http.Request) {
	taskStore, ok := s.requireScheduledTaskStore(w, r)
	if !ok {
		return
	}
	user, ok := requireScheduledTaskUser(w, r)
	if !ok {
		return
	}
	records, err := taskStore.ListScheduledTasksByUser(r.Context(), user.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	if records == nil {
		records = []*store.ScheduledTask{}
	}
	writeJSON(w, http.StatusOK, records)
}

func (s *Server) handleAdminListScheduledTasks(w http.ResponseWriter, r *http.Request) {
	taskStore, ok := s.requireScheduledTaskStore(w, r)
	if !ok {
		return
	}
	records, err := taskStore.ListAllScheduledTasks(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	if records == nil {
		records = []*store.ScheduledTask{}
	}
	writeJSON(w, http.StatusOK, records)
}

func (s *Server) handleGetScheduledTask(w http.ResponseWriter, r *http.Request) {
	task, ok := s.loadOwnedScheduledTask(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleUpdateScheduledTask(w http.ResponseWriter, r *http.Request) {
	taskStore, ok := s.requireScheduledTaskStore(w, r)
	if !ok {
		return
	}
	user, ok := requireScheduledTaskUser(w, r)
	if !ok {
		return
	}
	current, ok := s.loadOwnedScheduledTask(w, r)
	if !ok {
		return
	}
	var req scheduledTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "无效的请求体", Code: errs.CodeBadRequest})
		return
	}
	next, err := s.buildScheduledTaskFromRequest(req, current, user)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error(), Code: errs.CodeBadRequest})
		return
	}
	if err := taskStore.SaveScheduledTask(r.Context(), next); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	writeJSON(w, http.StatusOK, next)
}

func (s *Server) handleDeleteScheduledTask(w http.ResponseWriter, r *http.Request) {
	taskStore, ok := s.requireScheduledTaskStore(w, r)
	if !ok {
		return
	}
	current, ok := s.loadOwnedScheduledTask(w, r)
	if !ok {
		return
	}
	if err := taskStore.DeleteScheduledTask(r.Context(), current.ID); err != nil {
		writeStoreError(w, err, "定时任务未找到")
		return
	}
	if s.master != nil {
		s.master.StopCron("scheduled-task:" + current.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleToggleScheduledTask(w http.ResponseWriter, r *http.Request) {
	taskStore, ok := s.requireScheduledTaskStore(w, r)
	if !ok {
		return
	}
	current, ok := s.loadOwnedScheduledTask(w, r)
	if !ok {
		return
	}
	var req scheduledTaskToggleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "无效的请求体", Code: errs.CodeBadRequest})
		return
	}
	current.Enabled = req.Enabled
	if current.Enabled && current.NextRunAt == nil {
		next, err := scheduledTaskNextRunAt(current)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error(), Code: errs.CodeBadRequest})
			return
		}
		current.NextRunAt = &next
	}
	if err := taskStore.SaveScheduledTask(r.Context(), current); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	writeJSON(w, http.StatusOK, current)
}

func (s *Server) handleRunScheduledTaskNow(w http.ResponseWriter, r *http.Request) {
	taskStore, ok := s.requireScheduledTaskStore(w, r)
	if !ok {
		return
	}
	current, ok := s.loadOwnedScheduledTask(w, r)
	if !ok {
		return
	}
	if s.master == nil {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "master 未初始化", Code: errs.CodeInternal})
		return
	}
	now := time.Now().UTC()
	run, err := taskStore.ClaimManualScheduledTaskRun(r.Context(), current.ID, now, newScheduleRunID(), now.Add(35*time.Minute), "api")
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.master.RecordScheduledTaskMetric("scheduled_task.claim_total", map[string]any{"result": "skipped", "target_type": current.TargetType})
			writeJSON(w, http.StatusConflict, ErrorResponse{Error: "任务未启用或已有运行中的实例", Code: http.StatusConflict})
			return
		}
		s.master.RecordScheduledTaskMetric("scheduled_task.claim_total", map[string]any{"result": "error", "target_type": current.TargetType})
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	s.master.RecordScheduledTaskMetric("scheduled_task.claim_total", map[string]any{"result": "claimed", "target_type": current.TargetType})
	go s.executeScheduledTaskRun(context.Background(), taskStore, *current, run)
	writeJSON(w, http.StatusAccepted, run)
}

func (s *Server) handleListScheduledTaskRuns(w http.ResponseWriter, r *http.Request) {
	taskStore, ok := s.requireScheduledTaskStore(w, r)
	if !ok {
		return
	}
	current, ok := s.loadOwnedScheduledTask(w, r)
	if !ok {
		return
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	records, err := taskStore.ListScheduledTaskRuns(r.Context(), current.ID, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	if records == nil {
		records = []*store.ScheduledTaskRun{}
	}
	writeJSON(w, http.StatusOK, records)
}

func (s *Server) requireScheduledTaskStore(w http.ResponseWriter, _ *http.Request) (scheduledTaskStore, bool) {
	taskStore, ok := s.store.(scheduledTaskStore)
	if !ok || taskStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "scheduled task store 未初始化", Code: errs.CodeInternal})
		return nil, false
	}
	return taskStore, true
}

func requireScheduledTaskUser(w http.ResponseWriter, r *http.Request) (*auth.User, bool) {
	if auth.IsAuthEnabled(r.Context()) {
		user := auth.UserFrom(r.Context())
		if user == nil {
			writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "未授权", Code: http.StatusUnauthorized})
			return nil, false
		}
		return user, true
	}
	user := auth.UserFrom(r.Context())
	if user == nil {
		user = &auth.User{ID: "", Role: "admin", Status: "active"}
	}
	return user, true
}

func (s *Server) loadOwnedScheduledTask(w http.ResponseWriter, r *http.Request) (*store.ScheduledTask, bool) {
	taskStore, ok := s.requireScheduledTaskStore(w, r)
	if !ok {
		return nil, false
	}
	user, ok := requireScheduledTaskUser(w, r)
	if !ok {
		return nil, false
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "定时任务 id 不能为空", Code: errs.CodeBadRequest})
		return nil, false
	}
	task, err := taskStore.GetScheduledTask(r.Context(), id)
	if err != nil {
		writeStoreError(w, err, "定时任务未找到")
		return nil, false
	}
	if auth.IsAuthEnabled(r.Context()) && task.CreatedBy != user.ID {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "定时任务未找到", Code: errs.CodeNotFound})
		return nil, false
	}
	return task, true
}

func (s *Server) buildScheduledTaskFromRequest(req scheduledTaskRequest, current *store.ScheduledTask, user *auth.User) (*store.ScheduledTask, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, errors.New("name 不能为空")
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, errors.New("prompt 不能为空")
	}
	if len(prompt) > 8000 {
		return nil, errors.New("prompt 不能超过 8000 字符")
	}
	targetType := strings.TrimSpace(req.TargetType)
	if targetType == "" {
		targetType = "session"
	}
	if targetType != "im_push" && targetType != "session" {
		return nil, errors.New("target_type 只能是 im_push 或 session")
	}
	timezone := strings.TrimSpace(req.Timezone)
	if timezone == "" {
		timezone = "UTC"
	}
	interval := req.IntervalSec
	cronExpr := strings.TrimSpace(req.CronExpr)
	minInterval := master.ScheduledTaskDefaultMinInterval
	if user != nil && user.Role == "admin" {
		minInterval = master.ScheduledTaskAdminMinInterval
	}
	if err := master.ValidateScheduleSpec(master.ScheduleSpec{
		Interval: time.Duration(interval) * time.Second,
		CronExpr: cronExpr,
		Timezone: timezone,
	}, minInterval); err != nil {
		return nil, err
	}
	if targetType == "im_push" {
		if err := validateScheduledTaskPushInput(req); err != nil {
			return nil, err
		}
	}
	nextRunAt, err := master.NextScheduledRun(master.ScheduleSpec{
		Interval: time.Duration(interval) * time.Second,
		CronExpr: cronExpr,
		Timezone: timezone,
	}, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	createdBy := ""
	if user != nil {
		createdBy = user.ID
	}
	task := &store.ScheduledTask{
		ID:           newScheduleID(),
		Name:         name,
		Description:  strings.TrimSpace(req.Description),
		TargetType:   targetType,
		TargetConfig: req.TargetConfig,
		Platform:     strings.TrimSpace(req.Platform),
		Prompt:       prompt,
		CronExpr:     cronExpr,
		IntervalSec:  interval,
		Timezone:     timezone,
		Enabled:      req.Enabled,
		CreatedBy:    createdBy,
	}
	if task.TargetConfig == nil {
		task.TargetConfig = map[string]any{}
	}
	if current != nil {
		task.ID = current.ID
		task.CreatedBy = current.CreatedBy
		task.LastRunAt = current.LastRunAt
		task.LastError = current.LastError
		task.ActiveRunID = current.ActiveRunID
		task.LeaseExpiresAt = current.LeaseExpiresAt
		task.CreatedAt = current.CreatedAt
	}
	if task.Platform == "" && targetType == "im_push" {
		task.Platform = string(channel.PlatformFeishu)
	}
	if task.Enabled {
		task.NextRunAt = &nextRunAt
	}
	return task, nil
}

func validateScheduledTaskPushInput(req scheduledTaskRequest) error {
	if strings.HasPrefix(strings.TrimSpace(req.Prompt), "scheduled_push:") && len(req.TargetConfig) == 0 {
		parsed, matched, err := push.ParseScheduledPrompt(req.Prompt)
		if err != nil {
			return err
		}
		if !matched {
			return errors.New("scheduled_push prompt 无效")
		}
		platform := strings.TrimSpace(req.Platform)
		if platform == "" {
			platform = string(channel.PlatformFeishu)
		}
		if parsed.Platform != "" && string(parsed.Platform) != platform {
			return errors.New("schedule prompt platform 与请求 platform 不一致")
		}
		return nil
	}
	cfg := req.TargetConfig
	if len(cfg) == 0 {
		return errors.New("im_push target_config 不能为空")
	}
	if stringValue(cfg["chat_id"]) == "" && stringValue(cfg["open_id"]) == "" {
		return errors.New("im_push target_config 需要 chat_id 或 open_id")
	}
	if stringValue(cfg["content"]) == "" && stringValue(cfg["template"]) == "" && strings.TrimSpace(req.Prompt) == "" {
		return errors.New("im_push 需要 content、template 或 prompt")
	}
	return nil
}

func scheduledTaskNextRunAt(task *store.ScheduledTask) (time.Time, error) {
	return master.NextScheduledRun(master.ScheduleSpec{
		Interval: time.Duration(task.IntervalSec) * time.Second,
		CronExpr: task.CronExpr,
		Timezone: task.Timezone,
	}, time.Now().UTC())
}

func stringValue(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func writeStoreError(w http.ResponseWriter, err error, notFoundMsg string) {
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: notFoundMsg, Code: errs.CodeNotFound})
		return
	}
	writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
}

func newScheduleRunID() string {
	return "run-" + uuid.NewString()
}

func (s *Server) executeScheduledTaskRun(ctx context.Context, taskStore scheduledTaskStore, task store.ScheduledTask, run *store.ScheduledTaskRun) {
	if run == nil {
		return
	}
	execCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	sessionID, output, attempts, err := s.master.DispatchScheduledTaskWithRetry(execCtx, task, run.ID)
	finishedAt := time.Now().UTC()
	run.FinishedAt = &finishedAt
	run.SessionID = sessionID
	run.Output = output
	run.AttemptCount += attempts
	if err != nil {
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			run.Status = "timeout"
			run.Error = "scheduled task timed out after 30m"
		} else {
			run.Status = "failed"
			run.Error = err.Error()
		}
	} else {
		run.Status = "succeeded"
		run.Error = ""
	}
	if finishErr := taskStore.FinishScheduledTaskRun(context.Background(), run); finishErr != nil && s.logger != nil {
		s.logger.Warn("完成 Agent 定时任务 run 失败", zap.String("task_id", run.TaskID), zap.String("run_id", run.ID), zap.Error(finishErr))
	}
	s.master.RecordScheduledTaskMetric("scheduled_task.run_total", map[string]any{"status": run.Status, "target_type": task.TargetType})
}
