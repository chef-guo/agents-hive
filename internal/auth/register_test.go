package auth

import (
	"testing"
)

func TestValidateLocalPassword(t *testing.T) {
	if err := ValidateLocalPassword("short"); err == nil {
		t.Fatal("expected error for short password")
	}
	if err := ValidateLocalPassword("longenough"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNormalizeLocalLogin(t *testing.T) {
	if got := NormalizeLocalLogin("  User@Example.COM "); got != "user@example.com" {
		t.Fatalf("got %q", got)
	}
}

func TestInviteLookupKeyDeterministic(t *testing.T) {
	a := InviteLookupKey("ABC123")
	b := InviteLookupKey("ABC123")
	if len(a) != 32 || string(a) != string(b) {
		t.Fatal("lookup key should be stable sha256")
	}
}
