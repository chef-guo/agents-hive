package auth

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

const cacheInvalidateChannel = "auth_user_cache_invalidate"

// StartCacheInvalidationListener 监听 PG NOTIFY，跨实例失效用户缓存。
func StartCacheInvalidationListener(ctx context.Context, pool *pgxpool.Pool, engine *Engine, logger *zap.Logger) {
	if pool == nil || engine == nil {
		return
	}
	go func() {
		backoff := time.Second
		for {
			if ctx.Err() != nil {
				return
			}
			if err := listenCacheInvalidate(ctx, pool, engine, logger); err != nil && ctx.Err() == nil {
				logger.Warn("auth 缓存失效监听断开，将重连", zap.Error(err), zap.Duration("backoff", backoff))
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				if backoff < 30*time.Second {
					backoff *= 2
				}
				continue
			}
			backoff = time.Second
		}
	}()
}

func listenCacheInvalidate(ctx context.Context, pool *pgxpool.Pool, engine *Engine, logger *zap.Logger) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "LISTEN "+cacheInvalidateChannel); err != nil {
		return err
	}
	logger.Info("auth 用户缓存失效监听已启动", zap.String("channel", cacheInvalidateChannel))

	for {
		notification, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		if notification == nil || notification.Payload == "" {
			continue
		}
		engine.InvalidateUserCache(notification.Payload)
	}
}

// BroadcastUserCacheInvalidate 写库后主动 NOTIFY（本机 + 其他实例）。
func BroadcastUserCacheInvalidate(ctx context.Context, pool *pgxpool.Pool, userID string) {
	if pool == nil || userID == "" {
		return
	}
	_, _ = pool.Exec(ctx, "SELECT pg_notify($1, $2)", cacheInvalidateChannel, userID)
}

// InvalidateUserCacheCluster 本机失效 + 广播其他实例。
func (e *Engine) InvalidateUserCacheCluster(ctx context.Context, userID string) {
	e.InvalidateUserCache(userID)
	if pg, ok := e.store.(*PGStore); ok && pg.pool != nil {
		BroadcastUserCacheInvalidate(ctx, pg.pool, userID)
	}
}
