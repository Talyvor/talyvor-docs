-- 0027_workspace_email_branding.sql — B39.20: Docs' emails carry the workspace's name, colour and logo.
-- ADDITIVE ONLY. Docs owns no workspaces table, so workspace_id is Track's id as TEXT, like 0026.
-- One row per branded workspace, written only by its owner (internal/email/branding.go). No row means
-- Talyvor's default header and the configured From display name. The From address never changes.
CREATE TABLE IF NOT EXISTS workspace_email_branding (
    workspace_id TEXT PRIMARY KEY,
    from_name    TEXT NOT NULL DEFAULT '',
    header_color TEXT NOT NULL DEFAULT '' CHECK (header_color = '' OR header_color ~ '^#[0-9A-Fa-f]{6}$'),
    logo_url     TEXT NOT NULL DEFAULT '' CHECK (logo_url = '' OR logo_url LIKE 'https://%'),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
