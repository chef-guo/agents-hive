package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *PGStore) FindLocalUserByLogin(ctx context.Context, login string) (*User, string, error) {
	u := &User{}
	var hash string
	err := s.pool.QueryRow(ctx,
		`SELECT id, external_id, auth_provider, display_name, email, avatar_url, department,
		        role, status, last_login_at, last_login_ip, created_at, updated_at, password_hash
		 FROM users WHERE external_id = $1 AND auth_provider = $2`,
		login, localProviderName,
	).Scan(&u.ID, &u.ExternalID, &u.AuthProvider, &u.DisplayName, &u.Email,
		&u.AvatarURL, &u.Department, &u.Role, &u.Status,
		&u.LastLoginAt, &u.LastLoginIP, &u.CreatedAt, &u.UpdatedAt, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	return u, hash, nil
}

func (s *PGStore) CreateUserWithPassword(ctx context.Context, user *User, passwordHash string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO users (id, external_id, auth_provider, display_name, email, avatar_url, department, role, status, password_hash)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		user.ID, user.ExternalID, user.AuthProvider, user.DisplayName,
		user.Email, user.AvatarURL, user.Department, user.Role, user.Status, passwordHash,
	)
	return err
}

func (s *PGStore) RegisterUserWithInvite(ctx context.Context, user *User, passwordHash, inviteID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var disabled bool
	var useCount, maxUses int
	var expiresAt time.Time
	err = tx.QueryRow(ctx,
		`SELECT disabled, use_count, max_uses, expires_at FROM auth_invite_codes WHERE id = $1 FOR UPDATE`,
		inviteID,
	).Scan(&disabled, &useCount, &maxUses, &expiresAt)
	if err != nil {
		return err
	}
	if disabled || useCount >= maxUses || !expiresAt.After(time.Now()) {
		return fmt.Errorf("invite invalid")
	}

	tag, err := tx.Exec(ctx,
		`UPDATE auth_invite_codes SET use_count = use_count + 1, updated_at = NOW()
		 WHERE id = $1 AND use_count < max_uses AND disabled = FALSE AND expires_at > NOW()`,
		inviteID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("invite invalid")
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO users (id, external_id, auth_provider, display_name, email, avatar_url, department, role, status, password_hash)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		user.ID, user.ExternalID, user.AuthProvider, user.DisplayName,
		user.Email, user.AvatarURL, user.Department, user.Role, user.Status, passwordHash,
	)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PGStore) DeleteUser(ctx context.Context, userID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *PGStore) CountActiveAdmins(ctx context.Context) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM users WHERE role = 'admin' AND status = 'active'`,
	).Scan(&n)
	return n, err
}

func (s *PGStore) CreateInviteCode(ctx context.Context, invite *InviteCode, lookup []byte, hash string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO auth_invite_codes (id, code_lookup, code_hash, code_hint, role, max_uses, use_count, expires_at, disabled, note, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, 0, $7, $8, $9, $10)`,
		invite.ID, lookup, hash, invite.CodeHint, invite.Role, invite.MaxUses,
		invite.ExpiresAt, invite.Disabled, invite.Note, invite.CreatedBy,
	)
	return err
}

func (s *PGStore) GetInviteCodeByID(ctx context.Context, id string) (*InviteCode, error) {
	return scanInviteCode(s.pool.QueryRow(ctx,
		`SELECT id, code_hash, code_hint, role, max_uses, use_count, expires_at, disabled, note, created_by, created_at, updated_at
		 FROM auth_invite_codes WHERE id = $1`, id))
}

func (s *PGStore) ListInviteCodes(ctx context.Context) ([]*InviteCode, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, code_hash, code_hint, role, max_uses, use_count, expires_at, disabled, note, created_by, created_at, updated_at
		 FROM auth_invite_codes ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*InviteCode
	for rows.Next() {
		ic, err := scanInviteCodeRow(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, ic)
	}
	return list, rows.Err()
}

func (s *PGStore) UpdateInviteCode(ctx context.Context, id string, disabled *bool, note *string, expiresAt *time.Time) error {
	if disabled != nil {
		if _, err := s.pool.Exec(ctx,
			`UPDATE auth_invite_codes SET disabled = $1, updated_at = NOW() WHERE id = $2`,
			*disabled, id,
		); err != nil {
			return err
		}
	}
	if note != nil {
		if _, err := s.pool.Exec(ctx,
			`UPDATE auth_invite_codes SET note = $1, updated_at = NOW() WHERE id = $2`,
			*note, id,
		); err != nil {
			return err
		}
	}
	if expiresAt != nil {
		if _, err := s.pool.Exec(ctx,
			`UPDATE auth_invite_codes SET expires_at = $1, updated_at = NOW() WHERE id = $2 AND expires_at > NOW()`,
			*expiresAt, id,
		); err != nil {
			return err
		}
	}
	return nil
}

func (s *PGStore) DeleteInviteCode(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM auth_invite_codes WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *PGStore) FindInviteByLookup(ctx context.Context, lookup []byte) (*InviteCode, error) {
	return scanInviteCode(s.pool.QueryRow(ctx,
		`SELECT id, code_hash, code_hint, role, max_uses, use_count, expires_at, disabled, note, created_by, created_at, updated_at
		 FROM auth_invite_codes WHERE code_lookup = $1`, lookup))
}

type inviteScanner interface {
	Scan(dest ...any) error
}

func scanInviteCode(row inviteScanner) (*InviteCode, error) {
	ic := &InviteCode{}
	err := row.Scan(&ic.ID, &ic.CodeHash, &ic.CodeHint, &ic.Role, &ic.MaxUses, &ic.UseCount,
		&ic.ExpiresAt, &ic.Disabled, &ic.Note, &ic.CreatedBy, &ic.CreatedAt, &ic.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return ic, nil
}

func scanInviteCodeRow(rows pgx.Rows) (*InviteCode, error) {
	ic := &InviteCode{}
	err := rows.Scan(&ic.ID, &ic.CodeHash, &ic.CodeHint, &ic.Role, &ic.MaxUses, &ic.UseCount,
		&ic.ExpiresAt, &ic.Disabled, &ic.Note, &ic.CreatedBy, &ic.CreatedAt, &ic.UpdatedAt)
	return ic, err
}
