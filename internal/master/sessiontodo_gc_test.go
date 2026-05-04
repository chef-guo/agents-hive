package master

import (
	"testing"

	"github.com/chef-guo/agents-hive/internal/sessiontodo"
	"go.uber.org/zap"
)

func TestSetSessionTodoStoreRegistersMemoryGC(t *testing.T) {
	m := &Master{
		logger:   zap.NewNop(),
		stopCh:   make(chan struct{}),
		cronJobs: make(map[string]*cronJobState),
	}
	t.Cleanup(func() { close(m.stopCh) })

	m.SetSessionTodoStore(sessiontodo.NewMemoryStore())

	jobs := m.ListCrons()
	if len(jobs) != 1 {
		t.Fatalf("len(jobs) = %d, want 1", len(jobs))
	}
	if jobs[0].Name != sessionTodoMemoryGCJobName {
		t.Fatalf("job name = %q, want %q", jobs[0].Name, sessionTodoMemoryGCJobName)
	}
	if jobs[0].Interval != sessionTodoMemoryGCInterval {
		t.Fatalf("job interval = %v, want %v", jobs[0].Interval, sessionTodoMemoryGCInterval)
	}
}
