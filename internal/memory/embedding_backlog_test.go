package memory

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEmbeddingBacklogAtomicClaim(t *testing.T) {
	now := time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)
	backlog := NewInMemoryEmbeddingBacklog()
	jobID, err := backlog.Enqueue(context.Background(), EmbeddingBacklogJob{
		MemoryID:  42,
		UserID:    "u1",
		Content:   "prefers go",
		CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	first, err := backlog.ClaimNext(context.Background(), "worker-a", now)
	if err != nil {
		t.Fatalf("ClaimNext(first) error = %v", err)
	}
	if first == nil || first.ID != jobID {
		t.Fatalf("ClaimNext(first) = %+v, want job %d", first, jobID)
	}

	second, err := backlog.ClaimNext(context.Background(), "worker-b", now)
	if err != nil {
		t.Fatalf("ClaimNext(second) error = %v", err)
	}
	if second != nil {
		t.Fatalf("ClaimNext(second) = %+v, want nil because claim is atomic", second)
	}
}

func TestEmbeddingBacklogReclaimsStaleClaim(t *testing.T) {
	now := time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)
	backlog := NewInMemoryEmbeddingBacklog()
	jobID, err := backlog.Enqueue(context.Background(), EmbeddingBacklogJob{
		MemoryID:  42,
		UserID:    "u1",
		Content:   "prefers go",
		CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	first, err := backlog.ClaimNext(context.Background(), "worker-a", now)
	if err != nil {
		t.Fatalf("ClaimNext(first) error = %v", err)
	}
	if first == nil || first.ID != jobID {
		t.Fatalf("ClaimNext(first) = %+v, want job %d", first, jobID)
	}

	reclaimed, err := backlog.ClaimNext(context.Background(), "worker-b", now.Add(EmbeddingBacklogClaimLease+time.Second))
	if err != nil {
		t.Fatalf("ClaimNext(reclaim) error = %v", err)
	}
	if reclaimed == nil || reclaimed.ID != jobID {
		t.Fatalf("ClaimNext(reclaim) = %+v, want stale job %d", reclaimed, jobID)
	}
	if reclaimed.ClaimedBy != "worker-b" {
		t.Fatalf("ClaimedBy = %q, want worker-b", reclaimed.ClaimedBy)
	}
}

func TestEmbeddingBacklogWorkerSuccessSyncsVectorState(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)
	clock := &manualClock{now: now}
	backlog := NewInMemoryEmbeddingBacklog()
	syncer := &recordingEmbeddingSyncer{}
	embedder := &staticEmbedder{vectors: [][]float32{{1, 2, 3}}}
	_, err := backlog.Enqueue(ctx, EmbeddingBacklogJob{MemoryID: 7, UserID: "u1", Content: "uses vim", CreatedAt: now})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	worker := NewEmbeddingBacklogWorker(backlog, embedder, syncer, EmbeddingBacklogWorkerOptions{
		WorkerID: "worker-a",
		Now:      clock.Now,
	})

	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("ProcessOne() error = %v", err)
	}
	if !processed {
		t.Fatal("ProcessOne() processed = false, want true")
	}
	if syncer.memoryID != 7 {
		t.Fatalf("syncer memoryID = %d, want 7", syncer.memoryID)
	}
	if syncer.status.EmbeddingState != EmbeddingStateReady {
		t.Fatalf("EmbeddingState = %q, want %q", syncer.status.EmbeddingState, EmbeddingStateReady)
	}
	if syncer.status.VectorSpace != DefaultVectorSpaceName {
		t.Fatalf("VectorSpace = %q, want %q", syncer.status.VectorSpace, DefaultVectorSpaceName)
	}
	if len(syncer.vector) != 3 {
		t.Fatalf("synced vector len = %d, want 3", len(syncer.vector))
	}
	got, ok := backlog.Get(1)
	if !ok {
		t.Fatal("job missing after success")
	}
	if got.Status != EmbeddingBacklogStatusDone {
		t.Fatalf("job status = %q, want %q", got.Status, EmbeddingBacklogStatusDone)
	}
}

func TestEmbeddingBacklogWorkerUsesJobVectorSpace(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)
	backlog := NewInMemoryEmbeddingBacklog()
	syncer := &recordingEmbeddingSyncer{}
	_, err := backlog.Enqueue(ctx, EmbeddingBacklogJob{
		MemoryID:    8,
		UserID:      "u1",
		Content:     "uses zed",
		VectorSpace: "memory:v2",
		CreatedAt:   now,
	})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	worker := NewEmbeddingBacklogWorker(backlog, &staticEmbedder{vectors: [][]float32{{4, 5, 6}}}, syncer, EmbeddingBacklogWorkerOptions{
		WorkerID:    "worker-a",
		Now:         (&manualClock{now: now}).Now,
		VectorSpace: DefaultVectorSpaceName,
	})

	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("ProcessOne() error = %v", err)
	}
	if !processed {
		t.Fatal("ProcessOne() processed = false, want true")
	}
	if syncer.status.VectorSpace != "memory:v2" {
		t.Fatalf("VectorSpace = %q, want job vector space", syncer.status.VectorSpace)
	}
}

func TestEmbeddingBacklogWorkerRecordsMetrics(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)
	backlog := NewInMemoryEmbeddingBacklog()
	recorder := &recordingMemoryMetricRecorder{}
	syncer := &recordingEmbeddingSyncer{}
	_, err := backlog.Enqueue(ctx, EmbeddingBacklogJob{MemoryID: 11, UserID: "u1", Content: "uses rg", CreatedAt: now})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	worker := NewEmbeddingBacklogWorker(backlog, &staticEmbedder{vectors: [][]float32{{7, 8, 9}}}, syncer, EmbeddingBacklogWorkerOptions{
		WorkerID: "worker-a",
		Now:      (&manualClock{now: now}).Now,
		Metrics:  recorder,
	})

	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("ProcessOne() error = %v", err)
	}
	if !processed {
		t.Fatal("ProcessOne() processed = false, want true")
	}
	if !recorder.hasMetric(MetricEmbeddingLatencySeconds, "status", "ok") {
		t.Fatalf("missing embedding latency metric: %+v", recorder.events)
	}
}

func TestEmbeddingBacklogStatsRecordsDepth(t *testing.T) {
	ctx := context.Background()
	backlog := NewInMemoryEmbeddingBacklog()
	recorder := &recordingMemoryMetricRecorder{}
	wrapped := NewInstrumentedEmbeddingBacklog(backlog, recorder)
	_, err := wrapped.Enqueue(ctx, EmbeddingBacklogJob{MemoryID: 12, UserID: "u1", Content: "uses go"})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	stats, err := wrapped.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if stats.Total != 1 {
		t.Fatalf("Stats().Total = %d, want 1", stats.Total)
	}
	if !recorder.hasMetric(MetricEmbeddingBacklogDepth, "status", string(EmbeddingBacklogStatusPending)) {
		t.Fatalf("missing backlog depth metric: %+v", recorder.events)
	}
	if !recorder.hasMetric(MetricEmbeddingBacklogDepth, "status", "total") {
		t.Fatalf("missing total backlog depth metric: %+v", recorder.events)
	}
}

func TestEmbeddingBacklogFailureExponentialBackoffAccumulatesAtLeast30sAfter10Failures(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)
	clock := &manualClock{now: now}
	backlog := NewInMemoryEmbeddingBacklog()
	_, err := backlog.Enqueue(ctx, EmbeddingBacklogJob{MemoryID: 9, UserID: "u1", Content: "will fail", CreatedAt: now})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	worker := NewEmbeddingBacklogWorker(backlog, &failingEmbedder{}, &recordingEmbeddingSyncer{}, EmbeddingBacklogWorkerOptions{
		WorkerID: "worker-a",
		Now:      clock.Now,
	})

	var totalBackoff time.Duration
	for i := 0; i < 10; i++ {
		processed, err := worker.ProcessOne(ctx)
		if err == nil {
			t.Fatalf("ProcessOne(%d) error = nil, want failure", i+1)
		}
		if !processed {
			t.Fatalf("ProcessOne(%d) processed = false, want true", i+1)
		}
		job, ok := backlog.Get(1)
		if !ok {
			t.Fatal("job missing after failure")
		}
		backoff := job.NextRunAt.Sub(clock.Now())
		if backoff <= 0 {
			t.Fatalf("failure %d backoff = %v, want positive", i+1, backoff)
		}
		totalBackoff += backoff
		clock.now = job.NextRunAt
	}

	if totalBackoff < 30*time.Second {
		t.Fatalf("total backoff = %v, want >= 30s", totalBackoff)
	}
	job, _ := backlog.Get(1)
	if job.Attempts != 10 {
		t.Fatalf("Attempts = %d, want 10", job.Attempts)
	}
}

func TestPGEmbeddingBacklogEnqueueClaimAndStats(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL 未设置，跳过 embedding backlog PG 集成测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New() error = %v", err)
	}
	defer pool.Close()
	_, err = pool.Exec(ctx, `
DROP TABLE IF EXISTS embedding_backlog;
CREATE TABLE embedding_backlog (
	id          BIGSERIAL PRIMARY KEY,
	memory_id   BIGINT NOT NULL,
	user_id     TEXT NOT NULL DEFAULT '',
	content     TEXT NOT NULL DEFAULT '',
	vector_space TEXT NOT NULL DEFAULT 'memory:default',
	status      TEXT NOT NULL DEFAULT 'pending',
	attempts    INTEGER NOT NULL DEFAULT 0,
	claimed_by TEXT NOT NULL DEFAULT '',
	claimed_at TIMESTAMPTZ,
	next_run_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	last_error  TEXT NOT NULL DEFAULT '',
	created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_embedding_backlog_status_next
	ON embedding_backlog(status, next_run_at ASC, id ASC);
`)
	if err != nil {
		t.Fatalf("create table error = %v", err)
	}

	now := time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)
	backlog := NewPGEmbeddingBacklog(pool)
	id, err := backlog.Enqueue(ctx, EmbeddingBacklogJob{MemoryID: 42, UserID: "u1", Content: "prefers go", VectorSpace: "memory:v2", CreatedAt: now})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	first, err := backlog.ClaimNext(ctx, "worker-a", now)
	if err != nil {
		t.Fatalf("ClaimNext(first) error = %v", err)
	}
	if first == nil || first.ID != id || first.Status != EmbeddingBacklogStatusClaimed {
		t.Fatalf("ClaimNext(first) = %+v, want claimed job %d", first, id)
	}
	if first.VectorSpace != "memory:v2" {
		t.Fatalf("ClaimNext(first).VectorSpace = %q, want memory:v2", first.VectorSpace)
	}
	second, err := backlog.ClaimNext(ctx, "worker-b", now)
	if err != nil {
		t.Fatalf("ClaimNext(second) error = %v", err)
	}
	if second != nil {
		t.Fatalf("ClaimNext(second) = %+v, want nil", second)
	}
	stats, err := backlog.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if stats.Total != 1 || stats.ByState[EmbeddingBacklogStatusClaimed] != 1 {
		t.Fatalf("Stats() = %+v, want one claimed", stats)
	}
	if err := backlog.MarkDone(ctx, id, now.Add(time.Second)); err != nil {
		t.Fatalf("MarkDone() error = %v", err)
	}
	got, ok, err := backlog.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !ok || got.Status != EmbeddingBacklogStatusDone {
		t.Fatalf("Get() = %+v, %v, want done", got, ok)
	}
}

type manualClock struct {
	now time.Time
}

func (c *manualClock) Now() time.Time {
	return c.now
}

type staticEmbedder struct {
	vectors [][]float32
}

func (e *staticEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return e.vectors, nil
}

func (e *staticEmbedder) Dimensions() int {
	if len(e.vectors) == 0 {
		return 0
	}
	return len(e.vectors[0])
}

type failingEmbedder struct{}

func (e *failingEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return nil, errors.New("embed failed")
}

func (e *failingEmbedder) Dimensions() int {
	return 3
}

type recordingEmbeddingSyncer struct {
	memoryID int64
	vector   []float32
	status   MemoryEmbeddingStatus
}

func (s *recordingEmbeddingSyncer) SyncMemoryEmbedding(_ context.Context, memoryID int64, vector []float32, status MemoryEmbeddingStatus) error {
	s.memoryID = memoryID
	s.vector = append([]float32(nil), vector...)
	s.status = status
	return nil
}

type recordingMemoryMetricRecorder struct {
	events []MetricEvent
}

func (r *recordingMemoryMetricRecorder) RecordMemoryMetric(_ context.Context, event MetricEvent) {
	r.events = append(r.events, event)
}

func (r *recordingMemoryMetricRecorder) hasMetric(name, labelKey, labelValue string) bool {
	for _, event := range r.events {
		if event.Name != name {
			continue
		}
		if labelKey == "" {
			return true
		}
		if got, ok := event.Labels[labelKey]; ok && got == labelValue {
			return true
		}
	}
	return false
}
