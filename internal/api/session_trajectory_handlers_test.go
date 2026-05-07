package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/master"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/subagent"
	"github.com/chef-guo/agents-hive/internal/trajectory"
)

func TestSessionTrajectoryGetReturnsSnapshot(t *testing.T) {
	handler, _, trajStore, cleanup := newTestServerForSessionTrajectory(t)
	defer cleanup()

	sessionID := createTrajectoryTestSession(t, handler, "trajectory-source")
	messages := json.RawMessage(`[{"role":"user","content":"hello"},{"role":"assistant","content":"world"}]`)
	if err := trajStore.Save(t.Context(), trajectory.Snapshot{
		SessionID:    sessionID,
		SnapshotSeq:  7,
		TraceID:      "trace-1",
		SpanID:       "span-1",
		Iteration:    2,
		MessageCount: 2,
		Messages:     messages,
		SessionTodo:  json.RawMessage(`{"status":"running"}`),
		MemoryRefs:   json.RawMessage(`["mem-1"]`),
	}); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/sessions/"+sessionID+"/trajectory/7", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET trajectory status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got trajectory.Snapshot
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.SessionID != sessionID || got.SnapshotSeq != 7 || got.MessageCount != 2 {
		t.Fatalf("snapshot identity = (%q,%d,%d), want (%q,7,2)", got.SessionID, got.SnapshotSeq, got.MessageCount, sessionID)
	}
	if string(got.Messages) != string(messages) {
		t.Fatalf("messages = %s, want %s", got.Messages, messages)
	}
}

func TestSessionForkFromStepCreatesForkWithSnapshotMessagesAndPrompt(t *testing.T) {
	handler, testMaster, trajStore, cleanup := newTestServerForSessionTrajectory(t)
	defer cleanup()

	sessionID := createTrajectoryTestSession(t, handler, "trajectory-source")
	messages := json.RawMessage(`[{"role":"user","content":"first"},{"role":"assistant","content":"second"}]`)
	if err := trajStore.Save(t.Context(), trajectory.Snapshot{
		SessionID:    sessionID,
		SnapshotSeq:  3,
		MessageCount: 2,
		Messages:     messages,
	}); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	body := bytes.NewReader([]byte(`{"snapshot_seq":3,"fork_name":"debug-branch","prompt":"continue from here"}`))
	req := httptest.NewRequest("POST", "/api/v1/sessions/"+sessionID+"/fork-from-step", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("fork-from-step status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got ForkFromStepResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ForkID == "" || got.ForkName != "debug-branch" || got.SnapshotSeq != 3 || got.MessageCount != 3 {
		t.Fatalf("unexpected fork response: %+v", got)
	}

	forkMessages, err := testMaster.GetSessionMessages(t.Context(), got.ForkID, 0)
	if err != nil {
		t.Fatalf("get fork messages: %v", err)
	}
	if len(forkMessages) != 3 {
		t.Fatalf("fork message count = %d, want 3", len(forkMessages))
	}
	if forkMessages[0].Role != "user" || forkMessages[0].Content != "first" {
		t.Fatalf("first fork message = %#v", forkMessages[0])
	}
	if forkMessages[1].Role != "assistant" || forkMessages[1].Content != "second" {
		t.Fatalf("second fork message = %#v", forkMessages[1])
	}
	if forkMessages[2].Role != "user" || forkMessages[2].Content != "continue from here" {
		t.Fatalf("prompt fork message = %#v", forkMessages[2])
	}
}

func newTestServerForSessionTrajectory(t *testing.T) (http.Handler, *master.Master, trajectory.Store, func()) {
	t.Helper()
	logger, _ := zap.NewDevelopment()
	skillReg := skills.NewOverlayRegistry(logger)
	agentReg := subagent.NewRegistry(logger)
	st := store.NewMemoryStore()
	testMaster := master.NewMaster(
		master.Config{Model: "test"},
		config.HITLConfig{},
		agentReg,
		skillReg.Registry,
		st,
		logger,
	)

	ctx, cancel := context.WithCancel(context.Background())
	testMaster.Start(ctx)

	sessionDone := make(chan struct{})
	go func() {
		defer close(sessionDone)
		if err := testMaster.SessionLoop(ctx); err != nil && err != context.Canceled {
			logger.Error("session loop error", zap.Error(err))
		}
	}()
	time.Sleep(50 * time.Millisecond)

	trajStore := trajectory.NewMemoryStore()
	srv := NewServer(
		config.ServerConfig{Port: 0},
		config.HITLConfig{},
		config.WebUIConfig{},
		testMaster,
		skillReg,
		config.Default(),
		"",
		nil,
		nil,
		nil,
		logger,
	)
	srv.SetTrajectoryStore(trajStore)

	return srv.Mux(), testMaster, trajStore, func() {
		cancel()
		select {
		case <-sessionDone:
		case <-time.After(5 * time.Second):
		}
		testMaster.Stop()
	}
}

func createTrajectoryTestSession(t *testing.T, handler http.Handler, name string) string {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/sessions", bytes.NewReader([]byte(`{"name":"`+name+`"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create session status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var createResp CreateSessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return createResp.SessionID
}
