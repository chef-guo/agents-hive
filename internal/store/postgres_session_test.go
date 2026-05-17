package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/store"
)

func setupSessionDB(t *testing.T) *store.PostgresStore {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL 未设置，跳过 session PG 集成测试")
	}
	pg, err := store.NewPostgresStore(context.Background(), store.PostgresConfig{DSN: dsn, MaxConns: 2}, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = pg.Close() })
	return pg
}

func TestPostgresStore_SaveSessionDoesNotClearKBDomainOnEmptySnapshot(t *testing.T) {
	pg := setupSessionDB(t)
	ctx := context.Background()
	now := time.Now().Format(time.RFC3339)
	sessionID := "sess-" + uuid.NewString()
	require.NoError(t, pg.CreateSession(ctx, &store.SessionRecord{
		ID:             sessionID,
		Name:           "kb session",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastAccessedAt: now,
		KBDomainID:     "generic",
		UserID:         "user-1",
		Tags:           []string{},
	}))
	t.Cleanup(func() { _ = pg.DeleteSession(context.Background(), sessionID) })

	require.NoError(t, pg.SaveSession(ctx, &store.SessionRecord{
		ID:             sessionID,
		Name:           "renamed",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastAccessedAt: now,
		KBDomainID:     "",
		UserID:         "user-1",
		Tags:           []string{},
	}))
	got, err := pg.LoadSession(ctx, sessionID)
	require.NoError(t, err)
	require.Equal(t, "generic", got.KBDomainID)
}
