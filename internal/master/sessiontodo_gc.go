package master

import (
	"context"
	"time"

	"github.com/chef-guo/agents-hive/internal/sessiontodo"
	"go.uber.org/zap"
)

const (
	sessionTodoMemoryGCJobName  = "sessiontodo-memory-gc"
	sessionTodoMemoryGCInterval = time.Hour
	sessionTodoMemoryMaxIdle    = 24 * time.Hour
)

type sessionTodoMemoryGCStore interface {
	GCIdleSessions(context.Context, time.Duration) int
}

func (m *Master) registerSessionTodoMemoryGC(store sessiontodo.Store) {
	gcStore, ok := store.(sessionTodoMemoryGCStore)
	if !ok || m == nil || m.stopCh == nil {
		return
	}
	if err := m.CronCreate(CronJob{
		Name:     sessionTodoMemoryGCJobName,
		Interval: sessionTodoMemoryGCInterval,
		Callback: func(ctx context.Context) error {
			removed := gcStore.GCIdleSessions(ctx, sessionTodoMemoryMaxIdle)
			if removed > 0 && m.logger != nil {
				m.logger.Info("sessiontodo 内存 Store GC 完成", zap.Int("removed", removed))
			}
			return nil
		},
	}); err != nil && m.logger != nil {
		m.logger.Warn("sessiontodo 内存 Store GC 注册失败", zap.Error(err))
	}
}
