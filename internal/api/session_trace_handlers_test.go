package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/master"
	"github.com/chef-guo/agents-hive/internal/observability"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/subagent"
)

type fakeTraceReader struct {
	called     bool
	gotSession string
	gotLimit   int
	timeline   observability.TraceTimeline
	err        error
}

func (f *fakeTraceReader) GetSessionTimeline(_ context.Context, sessionID string, limit int) (observability.TraceTimeline, error) {
	f.called = true
	f.gotSession = sessionID
	f.gotLimit = limit
	return f.timeline, f.err
}

func newSessionTraceTestServer(t *testing.T) (*Server, *master.Master) {
	t.Helper()
	logger := zap.NewNop()
	skillReg := skills.NewOverlayRegistry(logger)
	agentReg := subagent.NewRegistry(logger)
	st := store.NewMemoryStore()
	m := master.NewMaster(
		master.Config{Model: "test"},
		config.HITLConfig{},
		agentReg,
		skillReg.Registry,
		st,
		logger,
	)
	s := &Server{
		master: m,
		config: config.Default(),
		logger: logger,
	}
	return s, m
}

func createTraceTestSession(t *testing.T, m *master.Master, name string) string {
	t.Helper()
	ctx := auth.WithUser(auth.WithAuthEnabled(context.Background()), &auth.User{ID: "owner", Role: "user"})
	id, err := m.CreateSession(ctx, name, "direct")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	return id
}

func TestHandleGetSessionTraceOK(t *testing.T) {
	s, m := newSessionTraceTestServer(t)
	sessionID := createTraceTestSession(t, m, "trace-ok")
	reader := &fakeTraceReader{
		timeline: observability.TraceTimeline{
			SessionID: sessionID,
			Items: []observability.TraceTimelineItem{{
				Kind:      "span",
				TraceID:   "trace-1",
				SpanID:    "span-1",
				Operation: "llm.call",
				Timestamp: time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC),
			}},
		},
	}
	s.SetTraceReader(reader)
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sessionID+"/trace?limit=100", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if !reader.called || reader.gotSession != sessionID || reader.gotLimit != 100 {
		t.Fatalf("reader call = called:%v session:%q limit:%d", reader.called, reader.gotSession, reader.gotLimit)
	}
	var got observability.TraceTimeline
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.SessionID != sessionID || len(got.Items) != 1 || got.Items[0].Kind != "span" {
		t.Fatalf("timeline mismatch: %+v", got)
	}
}

func TestHandleGetSessionTraceMissingSession(t *testing.T) {
	s, _ := newSessionTraceTestServer(t)
	reader := &fakeTraceReader{}
	s.SetTraceReader(reader)
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/missing/trace", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if reader.called {
		t.Fatal("trace reader should not be called for missing session")
	}
}

func TestHandleGetSessionTraceUnauthorizedDoesNotCallReader(t *testing.T) {
	s, m := newSessionTraceTestServer(t)
	ownerCtx := auth.WithUser(auth.WithAuthEnabled(context.Background()), &auth.User{ID: "owner", Role: "user"})
	sessionID, err := m.CreateSession(ownerCtx, "trace-forbidden", "direct")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	reader := &fakeTraceReader{}
	s.SetTraceReader(reader)
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sessionID+"/trace", nil)
	req = req.WithContext(auth.WithUser(auth.WithAuthEnabled(req.Context()), &auth.User{ID: "other", Role: "user"}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if reader.called {
		t.Fatal("trace reader should not be called before ownership passes")
	}
}

func TestHandleGetSessionTraceClampsLimit(t *testing.T) {
	s, m := newSessionTraceTestServer(t)
	sessionID := createTraceTestSession(t, m, "trace-limit")
	reader := &fakeTraceReader{timeline: observability.TraceTimeline{SessionID: sessionID}}
	s.SetTraceReader(reader)
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sessionID+"/trace?limit=5000", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if reader.gotLimit != 2000 {
		t.Fatalf("limit = %d, want 2000", reader.gotLimit)
	}
}

func TestHandleGetSessionTraceReaderNil(t *testing.T) {
	s, m := newSessionTraceTestServer(t)
	sessionID := createTraceTestSession(t, m, "trace-nil")
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sessionID+"/trace", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleGetSessionTraceReaderError(t *testing.T) {
	s, m := newSessionTraceTestServer(t)
	sessionID := createTraceTestSession(t, m, "trace-error")
	s.SetTraceReader(&fakeTraceReader{err: errors.New("db down")})
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sessionID+"/trace", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
}

func TestParseSessionTraceLimit(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"", 2000},
		{"abc", 2000},
		{"0", 2000},
		{"100", 100},
		{"5000", 2000},
	}
	for _, tc := range cases {
		if got := parseSessionTraceLimit(tc.raw); got != tc.want {
			t.Fatalf("parseSessionTraceLimit(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}
