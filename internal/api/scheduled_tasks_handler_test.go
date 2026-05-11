package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/master"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/subagent"
)

type blockingDispatchPushService struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type scheduledTaskResolverFunc func(context.Context, string) (*auth.User, error)

func (f scheduledTaskResolverFunc) GetUserByID(ctx context.Context, userID string) (*auth.User, error) {
	return f(ctx, userID)
}

type nilScheduledTaskStore struct {
	store.Store
}

func (nilScheduledTaskStore) SaveScheduledTask(context.Context, *store.ScheduledTask) error {
	return nil
}

func (nilScheduledTaskStore) GetScheduledTask(_ context.Context, id string) (*store.ScheduledTask, error) {
	return &store.ScheduledTask{ID: id, CreatedBy: ""}, nil
}

func (nilScheduledTaskStore) DeleteScheduledTask(context.Context, string) error {
	return nil
}

func (nilScheduledTaskStore) ListScheduledTasksByUser(context.Context, string) ([]*store.ScheduledTask, error) {
	return nil, nil
}

func (nilScheduledTaskStore) ListAllScheduledTasks(context.Context) ([]*store.ScheduledTask, error) {
	return nil, nil
}

func (nilScheduledTaskStore) ListScheduledTaskRuns(context.Context, string, int) ([]*store.ScheduledTaskRun, error) {
	return nil, nil
}

func (nilScheduledTaskStore) ClaimManualScheduledTaskRun(context.Context, string, time.Time, string, time.Time, string) (*store.ScheduledTaskRun, error) {
	return nil, store.ErrNotFound
}

func (nilScheduledTaskStore) FinishScheduledTaskRun(context.Context, *store.ScheduledTaskRun) error {
	return nil
}

func newBlockingDispatchPushService() *blockingDispatchPushService {
	return &blockingDispatchPushService{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (s *blockingDispatchPushService) DispatchScheduledPrompt(context.Context, string) error {
	s.once.Do(func() {
		close(s.started)
	})
	<-s.release
	return nil
}

func (s *blockingDispatchPushService) DispatchScheduledConfig(context.Context, string, map[string]any, string) error {
	return s.DispatchScheduledPrompt(context.Background(), "")
}

func newScheduledTaskTestServer(t *testing.T) (*Server, *store.MemoryStore) {
	t.Helper()
	logger := zap.NewNop()
	appStore := store.NewMemoryStore()
	m := master.NewMaster(master.Config{}, config.HITLConfig{}, subagent.NewRegistry(logger), skills.NewRegistry(logger), appStore, logger)
	cfg := config.Default()
	return NewServer(config.ServerConfig{Port: 0}, config.HITLConfig{}, config.WebUIConfig{}, m, nil, cfg, "", nil, appStore, nil, logger), appStore
}

func newScheduledTaskAuthTestServer(t *testing.T) (*Server, *store.MemoryStore) {
	t.Helper()
	logger := zap.NewNop()
	appStore := store.NewMemoryStore()
	m := master.NewMaster(master.Config{}, config.HITLConfig{}, subagent.NewRegistry(logger), skills.NewRegistry(logger), appStore, logger)
	cfg := config.Default()
	jwt := auth.NewJWTManager("test-secret", time.Hour, 24*time.Hour)
	authEngine := auth.NewEngine(nil, jwt, logger)
	return NewServer(config.ServerConfig{Port: 0}, config.HITLConfig{}, config.WebUIConfig{}, m, nil, cfg, "", nil, appStore, authEngine, logger), appStore
}

func scheduledTaskUserCtx(req *http.Request, userID string) *http.Request {
	return req.WithContext(auth.WithUser(auth.WithAuthEnabled(req.Context()), &auth.User{ID: userID, Role: "user", Status: "active"}))
}

func scheduledTaskAdminCtx(req *http.Request, userID string) *http.Request {
	return req.WithContext(auth.WithUser(auth.WithAuthEnabled(req.Context()), &auth.User{ID: userID, Role: "admin", Status: "active"}))
}

func TestScheduledTasksCRUDAndOwnership(t *testing.T) {
	srv, _ := newScheduledTaskTestServer(t)
	body := `{"name":"daily-quality","target_type":"session","prompt":"检查质量","interval_sec":60,"timezone":"UTC","enabled":true}`
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/scheduled-tasks", bytes.NewBufferString(body))
	createReq = scheduledTaskUserCtx(createReq, "u1")
	createRec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", createRec.Code, createRec.Body.String())
	}
	var created store.ScheduledTask
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.CreatedBy != "u1" || created.TargetType != "session" || created.NextRunAt == nil {
		t.Fatalf("unexpected created task: %+v", created)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/scheduled-tasks", nil)
	listReq = scheduledTaskUserCtx(listReq, "u1")
	listRec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", listRec.Code, listRec.Body.String())
	}
	var listed []store.ScheduledTask
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("unexpected list: %+v", listed)
	}

	otherReq := httptest.NewRequest(http.MethodGet, "/api/v1/scheduled-tasks/"+created.ID, nil)
	otherReq.SetPathValue("id", created.ID)
	otherReq = scheduledTaskUserCtx(otherReq, "u2")
	otherRec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(otherRec, otherReq)
	if otherRec.Code != http.StatusNotFound {
		t.Fatalf("cross-user status = %d, body=%s", otherRec.Code, otherRec.Body.String())
	}
}

func TestScheduledTaskListHandlersReturnEmptyArraysForNilSlices(t *testing.T) {
	srv := &Server{store: nilScheduledTaskStore{}, logger: zap.NewNop()}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/scheduled-tasks", nil)
	rec := httptest.NewRecorder()
	srv.handleListScheduledTasks(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("list body = %q, want []", rec.Body.String())
	}

	adminReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/scheduled-tasks", nil)
	adminRec := httptest.NewRecorder()
	srv.handleAdminListScheduledTasks(adminRec, adminReq)
	if adminRec.Code != http.StatusOK {
		t.Fatalf("admin list status = %d, body=%s", adminRec.Code, adminRec.Body.String())
	}
	if strings.TrimSpace(adminRec.Body.String()) != "[]" {
		t.Fatalf("admin list body = %q, want []", adminRec.Body.String())
	}

	runsReq := httptest.NewRequest(http.MethodGet, "/api/v1/scheduled-tasks/task-empty/runs", nil)
	runsReq.SetPathValue("id", "task-empty")
	runsRec := httptest.NewRecorder()
	srv.handleListScheduledTaskRuns(runsRec, runsReq)
	if runsRec.Code != http.StatusOK {
		t.Fatalf("runs status = %d, body=%s", runsRec.Code, runsRec.Body.String())
	}
	if strings.TrimSpace(runsRec.Body.String()) != "[]" {
		t.Fatalf("runs body = %q, want []", runsRec.Body.String())
	}
}

func TestScheduledTasksValidationRejectsHighFrequencyUserInterval(t *testing.T) {
	srv, _ := newScheduledTaskTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/scheduled-tasks", bytes.NewBufferString(`{"name":"fast","target_type":"session","prompt":"run","interval_sec":10,"timezone":"UTC","enabled":true}`))
	req = scheduledTaskUserCtx(req, "u1")
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestScheduledTaskRunsRequireOwnership(t *testing.T) {
	srv, appStore := newScheduledTaskTestServer(t)
	if err := appStore.SaveScheduledTask(httptest.NewRequest(http.MethodGet, "/", nil).Context(), &store.ScheduledTask{
		ID:          "task-1",
		Name:        "owned",
		TargetType:  "session",
		Prompt:      "run",
		IntervalSec: 60,
		Timezone:    "UTC",
		Enabled:     true,
		CreatedBy:   "u1",
	}); err != nil {
		t.Fatalf("SaveScheduledTask: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/scheduled-tasks/task-1/runs", nil)
	req.SetPathValue("id", "task-1")
	req = scheduledTaskUserCtx(req, "u2")
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 body=%s", rec.Code, rec.Body.String())
	}
}

func TestScheduledTaskToggleDisableKeepsRunningLease(t *testing.T) {
	srv, appStore := newScheduledTaskTestServer(t)
	leaseUntil := time.Now().UTC().Add(time.Hour)
	if err := appStore.SaveScheduledTask(httptest.NewRequest(http.MethodGet, "/", nil).Context(), &store.ScheduledTask{
		ID:             "task-running",
		Name:           "running",
		TargetType:     "session",
		Prompt:         "run",
		IntervalSec:    60,
		Timezone:       "UTC",
		Enabled:        true,
		CreatedBy:      "u1",
		ActiveRunID:    "run-1",
		LeaseExpiresAt: &leaseUntil,
	}); err != nil {
		t.Fatalf("SaveScheduledTask: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/scheduled-tasks/task-running/toggle", bytes.NewBufferString(`{"enabled":false}`))
	req.SetPathValue("id", "task-running")
	req = scheduledTaskUserCtx(req, "u1")
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("toggle status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got, err := appStore.GetScheduledTask(req.Context(), "task-running")
	if err != nil {
		t.Fatalf("GetScheduledTask: %v", err)
	}
	if got.Enabled || got.ActiveRunID != "run-1" || got.LeaseExpiresAt == nil {
		t.Fatalf("toggle disable must preserve running lease: %+v", got)
	}
}

func TestScheduledTaskUpdateKeepsRunningLease(t *testing.T) {
	srv, appStore := newScheduledTaskTestServer(t)
	leaseUntil := time.Now().UTC().Add(time.Hour)
	nextRun := time.Now().UTC().Add(time.Hour)
	if err := appStore.SaveScheduledTask(context.Background(), &store.ScheduledTask{
		ID:             "task-update-running",
		Name:           "running",
		TargetType:     "session",
		Prompt:         "run",
		IntervalSec:    60,
		Timezone:       "UTC",
		Enabled:        true,
		CreatedBy:      "u1",
		NextRunAt:      &nextRun,
		ActiveRunID:    "run-1",
		LeaseExpiresAt: &leaseUntil,
	}); err != nil {
		t.Fatalf("SaveScheduledTask: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/scheduled-tasks/task-update-running", bytes.NewBufferString(`{"name":"renamed","target_type":"session","prompt":"run updated","interval_sec":60,"timezone":"UTC","enabled":true}`))
	req.SetPathValue("id", "task-update-running")
	req = scheduledTaskUserCtx(req, "u1")
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got, err := appStore.GetScheduledTask(req.Context(), "task-update-running")
	if err != nil {
		t.Fatalf("GetScheduledTask: %v", err)
	}
	if got.ActiveRunID != "run-1" || got.LeaseExpiresAt == nil {
		t.Fatalf("update must preserve running lease: %+v", got)
	}
}

func TestScheduledTaskDeleteAllowsSessionTarget(t *testing.T) {
	srv, appStore := newScheduledTaskTestServer(t)
	if err := appStore.SaveScheduledTask(context.Background(), &store.ScheduledTask{
		ID:          "task-delete-session",
		Name:        "delete-session",
		TargetType:  "session",
		Prompt:      "run",
		IntervalSec: 60,
		Timezone:    "UTC",
		Enabled:     true,
		CreatedBy:   "u1",
	}); err != nil {
		t.Fatalf("SaveScheduledTask: %v", err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/scheduled-tasks/task-delete-session", nil)
	req.SetPathValue("id", "task-delete-session")
	req = scheduledTaskUserCtx(req, "u1")
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if _, err := appStore.GetScheduledTask(req.Context(), "task-delete-session"); err != store.ErrNotFound {
		t.Fatalf("GetScheduledTask after delete error = %v, want ErrNotFound", err)
	}
}

func TestScheduledTaskRunNowConflictDoesNotChangeNextRunAt(t *testing.T) {
	srv, appStore := newScheduledTaskTestServer(t)
	nextRun := time.Now().UTC().Add(time.Hour)
	leaseUntil := time.Now().UTC().Add(time.Hour)
	if err := appStore.SaveScheduledTask(context.Background(), &store.ScheduledTask{
		ID:             "task-run-now-conflict",
		Name:           "conflict",
		TargetType:     "session",
		Prompt:         "run",
		IntervalSec:    60,
		Timezone:       "UTC",
		Enabled:        true,
		CreatedBy:      "u1",
		NextRunAt:      &nextRun,
		ActiveRunID:    "run-active",
		LeaseExpiresAt: &leaseUntil,
	}); err != nil {
		t.Fatalf("SaveScheduledTask: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/scheduled-tasks/task-run-now-conflict/run-now", nil)
	req.SetPathValue("id", "task-run-now-conflict")
	req = scheduledTaskUserCtx(req, "u1")
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("run-now status = %d, want 409 body=%s", rec.Code, rec.Body.String())
	}
	got, err := appStore.GetScheduledTask(req.Context(), "task-run-now-conflict")
	if err != nil {
		t.Fatalf("GetScheduledTask: %v", err)
	}
	if got.NextRunAt == nil || !got.NextRunAt.Equal(nextRun) {
		t.Fatalf("run-now conflict changed next_run_at: got %v want %v", got.NextRunAt, nextRun)
	}
}

func TestScheduledTaskRunNowExecutesBeforeNextRunAndKeepsNextRunAt(t *testing.T) {
	srv, appStore := newScheduledTaskTestServer(t)
	pushSvc := newBlockingDispatchPushService()
	srv.master.SetScheduledTaskPushService(pushSvc)
	srv.master.SetScheduledTaskUserResolver(scheduledTaskResolverFunc(func(context.Context, string) (*auth.User, error) {
		return &auth.User{ID: "u1", Role: "user", Status: "active"}, nil
	}))
	nextRun := time.Now().UTC().Add(time.Hour)
	if err := appStore.SaveScheduledTask(context.Background(), &store.ScheduledTask{
		ID:          "task-run-now",
		Name:        "run-now",
		TargetType:  "im_push",
		Platform:    "feishu",
		Prompt:      "scheduled_push:task_done:chat_id=oc_1:title=ok:summary=done",
		IntervalSec: 60,
		Timezone:    "UTC",
		Enabled:     true,
		CreatedBy:   "u1",
		NextRunAt:   &nextRun,
	}); err != nil {
		t.Fatalf("SaveScheduledTask: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/scheduled-tasks/task-run-now/run-now", nil)
	req.SetPathValue("id", "task-run-now")
	req = scheduledTaskUserCtx(req, "u1")
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("run-now status = %d, body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-pushSvc.started:
	case <-time.After(time.Second):
		t.Fatal("run-now did not dispatch task")
	}
	got, err := appStore.GetScheduledTask(req.Context(), "task-run-now")
	if err != nil {
		t.Fatalf("GetScheduledTask after claim: %v", err)
	}
	if got.NextRunAt == nil || !got.NextRunAt.Equal(nextRun) {
		close(pushSvc.release)
		t.Fatalf("manual run changed next_run_at: got %v want %v", got.NextRunAt, nextRun)
	}
	close(pushSvc.release)
}

func TestScheduledTasksQuotaReturnsStandard429(t *testing.T) {
	srv, appStore := newScheduledTaskTestServer(t)
	for i := 0; i < scheduledTaskQuotaPerUser; i++ {
		if err := appStore.SaveScheduledTask(context.Background(), &store.ScheduledTask{
			ID:          "task-quota-" + strconv.Itoa(i),
			Name:        "quota-" + strconv.Itoa(i),
			TargetType:  "session",
			Prompt:      "run",
			IntervalSec: 60,
			Timezone:    "UTC",
			Enabled:     true,
			CreatedBy:   "u1",
		}); err != nil {
			t.Fatalf("SaveScheduledTask %d: %v", i, err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/scheduled-tasks", bytes.NewBufferString(`{"name":"overflow","target_type":"session","prompt":"run","interval_sec":60,"timezone":"UTC","enabled":true}`))
	req = scheduledTaskUserCtx(req, "u1")
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("quota status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"scheduled_tasks_quota_exceeded"`) || !strings.Contains(rec.Body.String(), `"limit":100`) {
		t.Fatalf("quota response body mismatch: %s", rec.Body.String())
	}
}

func TestScheduledTasksAdminListShowsAllUsers(t *testing.T) {
	srv, appStore := newScheduledTaskAuthTestServer(t)
	for _, id := range []string{"task-admin-u1", "task-admin-u2"} {
		owner := "u1"
		if strings.HasSuffix(id, "u2") {
			owner = "u2"
		}
		if err := appStore.SaveScheduledTask(context.Background(), &store.ScheduledTask{
			ID:          id,
			Name:        id,
			TargetType:  "session",
			Prompt:      "run",
			IntervalSec: 60,
			Timezone:    "UTC",
			Enabled:     true,
			CreatedBy:   owner,
		}); err != nil {
			t.Fatalf("SaveScheduledTask %s: %v", id, err)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/scheduled-tasks", nil)
	req = scheduledTaskAdminCtx(req, "admin-1")
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin list status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var listed []store.ScheduledTask
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode admin list: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("admin list count = %d, want 2 body=%s", len(listed), rec.Body.String())
	}
}

func TestScheduledTasksCronValidationRejectsInvalidCron(t *testing.T) {
	srv, _ := newScheduledTaskTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/scheduled-tasks", bytes.NewBufferString(`{"name":"bad-cron","target_type":"session","prompt":"run","cron_expr":"bad cron","timezone":"UTC","enabled":true}`))
	req = scheduledTaskUserCtx(req, "u1")
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("cron validation status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
}
