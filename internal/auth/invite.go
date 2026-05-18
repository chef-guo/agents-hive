package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"strings"
	"time"
)

// InviteCode 邀请码行（不含明文）。
type InviteCode struct {
	ID        string    `json:"id"`
	CodeHash  string    `json:"-"`
	CodeHint  string    `json:"code_hint,omitempty"`
	Role      string    `json:"role"`
	MaxUses   int       `json:"max_uses"`
	UseCount  int       `json:"use_count"`
	ExpiresAt time.Time `json:"expires_at"`
	Disabled  bool      `json:"disabled"`
	Note      string    `json:"note,omitempty"`
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// InviteCodeCreated 创建邀请码后一次性返回明文。
type InviteCodeCreated struct {
	Invite    *InviteCode `json:"invite"`
	Plaintext string      `json:"code"`
}

// GenerateInvitePlaintext 生成邀请码明文。
func GenerateInvitePlaintext() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.TrimRight(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), "="), nil
}

// InviteLookupKey 邀请码抗枚举查找键。
func InviteLookupKey(raw string) []byte {
	sum := sha256.Sum256([]byte(strings.TrimSpace(raw)))
	return sum[:]
}

func inviteLookupKey(raw string) []byte {
	return InviteLookupKey(raw)
}

// InviteCodeHint 邀请码列表辨认后缀。
func InviteCodeHint(raw string) string {
	return inviteCodeHint(raw)
}

func inviteCodeHint(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) <= 4 {
		return raw
	}
	return raw[len(raw)-4:]
}

// NewRandomID 生成随机 ID。
func NewRandomID() string {
	return generateRandomSecret(16)
}
