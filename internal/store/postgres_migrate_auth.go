package store

// pgMigrateAuthUserManagement 本地注册、邀请码与用户密码（认证方案附录 D）。
const pgMigrateAuthUserManagement = `
ALTER TABLE users ADD COLUMN IF NOT EXISTS password_hash TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS auth_invite_codes (
    id              TEXT PRIMARY KEY,
    code_lookup     BYTEA NOT NULL UNIQUE,
    code_hash       TEXT NOT NULL,
    code_hint       TEXT NOT NULL DEFAULT '',
    role            TEXT NOT NULL DEFAULT 'user',
    max_uses        INT NOT NULL DEFAULT 1,
    use_count       INT NOT NULL DEFAULT 0,
    expires_at      TIMESTAMPTZ NOT NULL,
    disabled        BOOLEAN NOT NULL DEFAULT FALSE,
    note            TEXT NOT NULL DEFAULT '',
    created_by      TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_auth_invite_codes_expires ON auth_invite_codes(expires_at);

CREATE OR REPLACE FUNCTION auth_user_cache_invalidate_notify() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        PERFORM pg_notify('auth_user_cache_invalidate', OLD.id);
        RETURN OLD;
    END IF;
    IF TG_OP = 'UPDATE' AND (OLD.role IS DISTINCT FROM NEW.role OR OLD.status IS DISTINCT FROM NEW.status) THEN
        PERFORM pg_notify('auth_user_cache_invalidate', NEW.id);
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS auth_users_cache_invalidate_trigger ON users;
CREATE TRIGGER auth_users_cache_invalidate_trigger
    AFTER UPDATE OF role, status OR DELETE ON users
    FOR EACH ROW EXECUTE FUNCTION auth_user_cache_invalidate_notify();
`
