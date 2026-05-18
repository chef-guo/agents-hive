package auth

import (
	"context"
	"testing"
)

func TestSeedDefaultAdmin_skipsWhenLocalAdminExists(t *testing.T) {
	store := &mockStore{
		users: map[string]*User{
			"admin:" + localProviderName: {ID: "u1", ExternalID: "admin", AuthProvider: localProviderName, Role: "admin"},
		},
		byID: map[string]*User{"u1": {ID: "u1", ExternalID: "admin", AuthProvider: localProviderName, Role: "admin"}},
	}
	seeded, err := SeedDefaultAdmin(context.Background(), store, true)
	if err != nil {
		t.Fatal(err)
	}
	if seeded {
		t.Fatal("expected no seed when local admin exists")
	}
}

func TestSeedDefaultAdmin_createsWhenOnlyOtherUsersExist(t *testing.T) {
	store := newMockStore()
	_ = store.CreateUser(context.Background(), &User{
		ID: "u1", ExternalID: "feishu-1", AuthProvider: "feishu", Role: "user", Status: "active",
	})
	seeded, err := SeedDefaultAdmin(context.Background(), store, true)
	if err != nil {
		t.Fatal(err)
	}
	if !seeded {
		t.Fatal("expected seed when local admin missing")
	}
	u, err := store.FindUserByExternalID(context.Background(), "admin", localProviderName)
	if err != nil || u == nil {
		t.Fatalf("local admin not created: %v", err)
	}
	if u.Role != "admin" {
		t.Fatalf("role=%s", u.Role)
	}
}
