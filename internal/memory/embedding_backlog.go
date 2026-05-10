package memory

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

type EmbeddingBacklogStatus string

const (
	EmbeddingBacklogStatusPending EmbeddingBacklogStatus = "pending"
	EmbeddingBacklogStatusClaimed EmbeddingBacklogStatus = "claimed"
	EmbeddingBacklogStatusDone    EmbeddingBacklogStatus = "done"
	EmbeddingBacklogStatusFailed  EmbeddingBacklogStatus = "failed"

	EmbeddingBacklogClaimLease = 5 * time.Minute
)

type EmbeddingBacklogJob struct {
	ID          int64                  `json:"id"`
	MemoryID    int64                  `json:"memory_id"`
	UserID      string                 `json:"user_id,omitempty"`
	Content     string                 `json:"content"`
	VectorSpace string                 `json:"vector_space,omitempty"`
	Status      EmbeddingBacklogStatus `json:"status"`
	Attempts    int                    `json:"attempts"`
	CreatedAt   time.Time              `json:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at"`
	ClaimedBy   string                 `json:"claimed_by,omitempty"`
	ClaimedAt   time.Time              `json:"claimed_at,omitempty"`
	NextRunAt   time.Time              `json:"next_run_at"`
	LastError   string                 `json:"last_error,omitempty"`
}

type MemoryEmbeddingStatus struct {
	EmbeddingState EmbeddingState `json:"embedding_state"`
	VectorSpace    string         `json:"vector_space"`
	Dimensions     int            `json:"dimensions,omitempty"`
	UpdatedAt      time.Time      `json:"updated_at,omitempty"`
}

type EmbeddingBacklogStats struct {
	Total   int                            `json:"total"`
	ByState map[EmbeddingBacklogStatus]int `json:"by_state"`
}

type MemoryEmbeddingSyncer interface {
	SyncMemoryEmbedding(ctx context.Context, memoryID int64, vector []float32, status MemoryEmbeddingStatus) error
}

type EmbeddingBacklog interface {
	Enqueue(ctx context.Context, job EmbeddingBacklogJob) (int64, error)
	ClaimNext(ctx context.Context, workerID string, now time.Time) (*EmbeddingBacklogJob, error)
	MarkDone(ctx context.Context, id int64, now time.Time) error
	MarkFailed(ctx context.Context, id int64, err error, nextRunAt time.Time, now time.Time) error
	Stats(ctx context.Context) (EmbeddingBacklogStats, error)
}

type InMemoryEmbeddingBacklog struct {
	mu     sync.Mutex
	nextID int64
	jobs   map[int64]EmbeddingBacklogJob
}

func NewInMemoryEmbeddingBacklog() *InMemoryEmbeddingBacklog {
	return &InMemoryEmbeddingBacklog{jobs: map[int64]EmbeddingBacklogJob{}}
}

func (b *InMemoryEmbeddingBacklog) Enqueue(_ context.Context, job EmbeddingBacklogJob) (int64, error) {
	if job.MemoryID == 0 {
		return 0, errors.New("memory id is required")
	}
	if job.Content == "" {
		return 0, errors.New("content is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now()
	}
	if job.UpdatedAt.IsZero() {
		job.UpdatedAt = job.CreatedAt
	}
	if job.NextRunAt.IsZero() {
		job.NextRunAt = job.CreatedAt
	}
	if job.VectorSpace == "" {
		job.VectorSpace = DefaultVectorSpaceName
	}
	job.ID = b.nextID
	job.Status = EmbeddingBacklogStatusPending
	b.jobs[job.ID] = cloneEmbeddingBacklogJob(job)
	return job.ID, nil
}

func (b *InMemoryEmbeddingBacklog) ClaimNext(_ context.Context, workerID string, now time.Time) (*EmbeddingBacklogJob, error) {
	if now.IsZero() {
		now = time.Now()
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	ids := make([]int64, 0, len(b.jobs))
	for id := range b.jobs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		left := b.jobs[ids[i]]
		right := b.jobs[ids[j]]
		if !left.NextRunAt.Equal(right.NextRunAt) {
			return left.NextRunAt.Before(right.NextRunAt)
		}
		return left.ID < right.ID
	})

	for _, id := range ids {
		job := b.jobs[id]
		if job.Status == EmbeddingBacklogStatusDone {
			continue
		}
		if job.Status == EmbeddingBacklogStatusClaimed && !claimExpired(job.ClaimedAt, now) {
			continue
		}
		if job.NextRunAt.After(now) {
			continue
		}
		job.Status = EmbeddingBacklogStatusClaimed
		job.ClaimedBy = workerID
		job.ClaimedAt = now
		job.UpdatedAt = now
		b.jobs[id] = job
		claimed := cloneEmbeddingBacklogJob(job)
		return &claimed, nil
	}
	return nil, nil
}

func (b *InMemoryEmbeddingBacklog) MarkDone(_ context.Context, id int64, now time.Time) error {
	if now.IsZero() {
		now = time.Now()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	job, ok := b.jobs[id]
	if !ok {
		return errors.New("embedding backlog job not found")
	}
	job.Status = EmbeddingBacklogStatusDone
	job.UpdatedAt = now
	job.NextRunAt = time.Time{}
	job.LastError = ""
	b.jobs[id] = job
	return nil
}

func (b *InMemoryEmbeddingBacklog) MarkFailed(_ context.Context, id int64, err error, nextRunAt time.Time, now time.Time) error {
	if now.IsZero() {
		now = time.Now()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	job, ok := b.jobs[id]
	if !ok {
		return errors.New("embedding backlog job not found")
	}
	job.Status = EmbeddingBacklogStatusFailed
	job.Attempts++
	job.UpdatedAt = now
	job.NextRunAt = nextRunAt
	if err != nil {
		job.LastError = err.Error()
	}
	b.jobs[id] = job
	return nil
}

func (b *InMemoryEmbeddingBacklog) Get(id int64) (EmbeddingBacklogJob, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	job, ok := b.jobs[id]
	if !ok {
		return EmbeddingBacklogJob{}, false
	}
	return cloneEmbeddingBacklogJob(job), true
}

func (b *InMemoryEmbeddingBacklog) Stats(ctx context.Context) (EmbeddingBacklogStats, error) {
	if err := ctx.Err(); err != nil {
		return EmbeddingBacklogStats{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	stats := EmbeddingBacklogStats{ByState: map[EmbeddingBacklogStatus]int{}}
	for _, job := range b.jobs {
		stats.Total++
		stats.ByState[job.Status]++
	}
	return stats, nil
}

type InstrumentedEmbeddingBacklog struct {
	inner   EmbeddingBacklog
	metrics MetricRecorder
}

func NewInstrumentedEmbeddingBacklog(inner EmbeddingBacklog, metrics MetricRecorder) *InstrumentedEmbeddingBacklog {
	return &InstrumentedEmbeddingBacklog{inner: inner, metrics: metrics}
}

func (b *InstrumentedEmbeddingBacklog) Enqueue(ctx context.Context, job EmbeddingBacklogJob) (int64, error) {
	return b.inner.Enqueue(ctx, job)
}

func (b *InstrumentedEmbeddingBacklog) ClaimNext(ctx context.Context, workerID string, now time.Time) (*EmbeddingBacklogJob, error) {
	return b.inner.ClaimNext(ctx, workerID, now)
}

func (b *InstrumentedEmbeddingBacklog) MarkDone(ctx context.Context, id int64, now time.Time) error {
	return b.inner.MarkDone(ctx, id, now)
}

func (b *InstrumentedEmbeddingBacklog) MarkFailed(ctx context.Context, id int64, err error, nextRunAt time.Time, now time.Time) error {
	return b.inner.MarkFailed(ctx, id, err, nextRunAt, now)
}

func (b *InstrumentedEmbeddingBacklog) Stats(ctx context.Context) (EmbeddingBacklogStats, error) {
	stats, err := b.inner.Stats(ctx)
	if err == nil {
		recordBacklogDepth(ctx, b.metrics, stats)
	}
	return stats, err
}

type EmbeddingBacklogWorkerOptions struct {
	WorkerID     string
	Now          func() time.Time
	VectorSpace  string
	BackoffBase  time.Duration
	BackoffLimit time.Duration
	Metrics      MetricRecorder
}

type EmbeddingBacklogWorker struct {
	backlog  EmbeddingBacklog
	embedder EmbeddingProvider
	syncer   MemoryEmbeddingSyncer
	opts     EmbeddingBacklogWorkerOptions
}

func NewEmbeddingBacklogWorker(backlog EmbeddingBacklog, embedder EmbeddingProvider, syncer MemoryEmbeddingSyncer, opts EmbeddingBacklogWorkerOptions) *EmbeddingBacklogWorker {
	if opts.WorkerID == "" {
		opts.WorkerID = "embedding-worker"
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.VectorSpace == "" {
		opts.VectorSpace = DefaultVectorSpaceName
	}
	if opts.BackoffBase <= 0 {
		opts.BackoffBase = time.Second
	}
	if opts.BackoffLimit <= 0 {
		opts.BackoffLimit = 5 * time.Minute
	}
	return &EmbeddingBacklogWorker{backlog: backlog, embedder: embedder, syncer: syncer, opts: opts}
}

func (w *EmbeddingBacklogWorker) ProcessOne(ctx context.Context) (bool, error) {
	now := w.opts.Now()
	job, err := w.backlog.ClaimNext(ctx, w.opts.WorkerID, now)
	if err != nil || job == nil {
		return false, err
	}

	start := time.Now()
	vectors, err := w.embedder.Embed(ctx, []string{job.Content})
	if err != nil {
		recordEmbeddingDropped(ctx, w.opts.Metrics, EmbeddingDroppedReasonProviderError)
		recordEmbeddingLatency(ctx, w.opts.Metrics, "backlog", "error", time.Since(start))
		_ = w.backlog.MarkFailed(ctx, job.ID, err, now.Add(w.nextBackoff(job.Attempts+1)), now)
		return true, err
	}
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		err = errors.New("embedding provider returned empty vector")
		recordEmbeddingDropped(ctx, w.opts.Metrics, EmbeddingDroppedReasonEmptyVector)
		recordEmbeddingLatency(ctx, w.opts.Metrics, "backlog", "empty", time.Since(start))
		_ = w.backlog.MarkFailed(ctx, job.ID, err, now.Add(w.nextBackoff(job.Attempts+1)), now)
		return true, err
	}

	vector := append([]float32(nil), vectors[0]...)
	vectorSpace := job.VectorSpace
	if vectorSpace == "" {
		vectorSpace = w.opts.VectorSpace
	}
	vectorSpace = metricVectorSpace(vectorSpace)
	if err := w.syncer.SyncMemoryEmbedding(ctx, job.MemoryID, vector, MemoryEmbeddingStatus{
		EmbeddingState: EmbeddingStateReady,
		VectorSpace:    vectorSpace,
		Dimensions:     len(vector),
		UpdatedAt:      now,
	}); err != nil {
		recordEmbeddingDropped(ctx, w.opts.Metrics, EmbeddingDroppedReasonStoreError)
		if isVectorSpaceMismatchError(err) {
			recordMetric(ctx, w.opts.Metrics, MetricVectorSpaceMismatchTotal, 1, map[string]any{"operation": "backlog"})
		}
		recordEmbeddingLatency(ctx, w.opts.Metrics, "backlog", "error", time.Since(start))
		_ = w.backlog.MarkFailed(ctx, job.ID, err, now.Add(w.nextBackoff(job.Attempts+1)), now)
		return true, err
	}
	if err := w.backlog.MarkDone(ctx, job.ID, now); err != nil {
		recordEmbeddingLatency(ctx, w.opts.Metrics, "backlog", "error", time.Since(start))
		return true, err
	}
	recordEmbeddingLatency(ctx, w.opts.Metrics, "backlog", "ok", time.Since(start))
	return true, nil
}

func (w *EmbeddingBacklogWorker) nextBackoff(attempts int) time.Duration {
	if attempts <= 0 {
		attempts = 1
	}
	backoff := w.opts.BackoffBase
	for i := 1; i < attempts; i++ {
		if backoff >= w.opts.BackoffLimit/2 {
			return w.opts.BackoffLimit
		}
		backoff *= 2
	}
	if backoff > w.opts.BackoffLimit {
		return w.opts.BackoffLimit
	}
	return backoff
}

func cloneEmbeddingBacklogJob(job EmbeddingBacklogJob) EmbeddingBacklogJob {
	return job
}

func claimExpired(claimedAt time.Time, now time.Time) bool {
	if claimedAt.IsZero() {
		return true
	}
	return !claimedAt.Add(EmbeddingBacklogClaimLease).After(now)
}
