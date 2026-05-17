package kb

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBindingResolverNoBindingFailsClosed(t *testing.T) {
	resolver := NewBindingResolver(NewMemoryStore())
	_, err := resolver.Resolve(context.Background(), BindingResolveInput{
		DomainID:   "domain-1",
		OwnerScope: OwnerScopeTenant,
		OwnerID:    "tenant-1",
		UserID:     "user-1",
		Now:        time.Now(),
	})
	require.ErrorIs(t, err, ErrNoKBBinding)
}

func TestActiveBindingHintFindsSessionBindingAcrossDomains(t *testing.T) {
	now := time.Now()
	store := NewMemoryStore()
	require.NoError(t, store.SaveBinding(context.Background(), Binding{
		ID:            "binding-support-session",
		DomainID:      "support",
		OwnerScope:    OwnerScopeUser,
		OwnerID:       "user-1",
		NamespaceID:   "ns-support",
		BindingType:   BindingTypeSession,
		BindingTarget: "session-1",
		Enabled:       true,
		EffectiveAt:   now.Add(-time.Minute),
		CreatedAt:     now,
		UpdatedAt:     now,
	}))

	domainID, ok, err := NewService(store).ActiveBindingHint(context.Background(), ActiveBindingHintInput{
		OwnerScope:    OwnerScopeUser,
		OwnerID:       "user-1",
		BindingType:   BindingTypeSession,
		BindingTarget: "session-1",
		Now:           now,
	})

	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "support", domainID)
}

func TestBindingResolverUnionsEffectiveBindings(t *testing.T) {
	now := time.Now()
	store := NewMemoryStore()
	seedNamespace(t, store, now, "agent-ns")
	seedNamespace(t, store, now, "domain-ns")
	seedNamespace(t, store, now, "session-template-ns")
	seedNamespace(t, store, now, "session-ns")
	seedNamespace(t, store, now, "tenant-ns")
	seedNamespace(t, store, now, "user-ns")
	seedBinding(t, store, now, "b-agent", "agent-ns", BindingTypeAgent, "agent-1")
	seedBinding(t, store, now, "b-domain", "domain-ns", BindingTypeDomain, "domain-1")
	seedBinding(t, store, now, "b-template", "session-template-ns", BindingTypeSessionTemplate, "template-1")
	seedBinding(t, store, now, "b-session", "session-ns", BindingTypeSession, "session-1")
	seedBinding(t, store, now, "b-tenant", "tenant-ns", BindingTypeTenant, "tenant-1")
	seedBinding(t, store, now, "b-user", "user-ns", BindingTypeUser, "user-1")
	seedBinding(t, store, now, "b-dupe", "agent-ns", BindingTypeAgent, "agent-1")

	resolver := NewBindingResolver(store)
	namespaces, err := resolver.Resolve(context.Background(), BindingResolveInput{
		DomainID:          "domain-1",
		OwnerScope:        OwnerScopeTenant,
		OwnerID:           "tenant-1",
		UserID:            "user-1",
		TenantID:          "tenant-1",
		AgentID:           "agent-1",
		SessionTemplateID: "template-1",
		SessionID:         "session-1",
		Now:               now,
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"agent-ns", "domain-ns", "session-template-ns", "session-ns", "tenant-ns", "user-ns"}, namespaces)
}

func TestBindingResolverIgnoresDisabledFutureAndExpiredBindings(t *testing.T) {
	now := time.Now()
	store := NewMemoryStore()
	seedNamespace(t, store, now, "disabled-ns")
	seedNamespace(t, store, now, "future-ns")
	seedNamespace(t, store, now, "expired-ns")
	expiredAt := now.Add(-time.Minute)
	require.NoError(t, store.SaveBinding(context.Background(), Binding{
		ID:            "disabled",
		DomainID:      "domain-1",
		OwnerScope:    OwnerScopeTenant,
		OwnerID:       "tenant-1",
		NamespaceID:   "disabled-ns",
		BindingType:   BindingTypeAgent,
		BindingTarget: "agent-1",
		Enabled:       false,
		EffectiveAt:   now.Add(-time.Hour),
	}))
	require.NoError(t, store.SaveBinding(context.Background(), Binding{
		ID:            "future",
		DomainID:      "domain-1",
		OwnerScope:    OwnerScopeTenant,
		OwnerID:       "tenant-1",
		NamespaceID:   "future-ns",
		BindingType:   BindingTypeAgent,
		BindingTarget: "agent-1",
		Enabled:       true,
		EffectiveAt:   now.Add(time.Hour),
	}))
	require.NoError(t, store.SaveBinding(context.Background(), Binding{
		ID:            "expired",
		DomainID:      "domain-1",
		OwnerScope:    OwnerScopeTenant,
		OwnerID:       "tenant-1",
		NamespaceID:   "expired-ns",
		BindingType:   BindingTypeAgent,
		BindingTarget: "agent-1",
		Enabled:       true,
		EffectiveAt:   now.Add(-time.Hour),
		ExpiresAt:     &expiredAt,
	}))

	_, err := NewBindingResolver(store).Resolve(context.Background(), BindingResolveInput{
		DomainID:   "domain-1",
		OwnerScope: OwnerScopeTenant,
		OwnerID:    "tenant-1",
		AgentID:    "agent-1",
		Now:        now,
	})
	require.ErrorIs(t, err, ErrNoKBBinding)
}
